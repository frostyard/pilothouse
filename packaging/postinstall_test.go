package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	postinstallPath = "postinstall.sh"
	sysusersPath    = "pilothouse.sysusers"

	// sysusersConf is where the package installs pilothouse.sysusers. The
	// postinstall names it explicitly so systemd-sysusers processes only this
	// package's declaration instead of every configuration on the host.
	sysusersConf = "/usr/lib/sysusers.d/pilothouse.conf"

	// rootPrefix is the test seam the script prefixes its filesystem operands
	// with. It defaults to empty, so production behavior is unchanged.
	rootPrefix = "${PILOTHOUSE_ROOT}"
)

// Production paths, written out by hand from the spec rather than derived from
// the script, so a typo in the script cannot move the expectation with it.
const (
	prodConfigDir      = "/etc/pilothouse"
	prodCredentialsDir = "/etc/pilothouse/storage/credentials"
	prodWebEnv         = "/etc/pilothouse/pilothouse.env"
	prodBrokerEnv      = "/etc/pilothouse/pilothoused.env"
)

func readPostinstall(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(postinstallPath)
	require.NoErrorf(t, err, "read %s", postinstallPath)

	return string(raw)
}

// effectiveLines returns the script's lines with the shebang, blank lines, and
// comment lines removed. Structural assertions run against these so a path
// merely *mentioned* in a comment can never satisfy (or break) a check about
// what the script actually executes.
func effectiveLines(script string) []string {
	var effective []string

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		effective = append(effective, trimmed)
	}

	return effective
}

// logicalLines joins continuation lines (a trailing `\`, `||`, `&&`, or `|`)
// with the line that follows, so a guard and the command it guards are a single
// string to assert against regardless of how the script wraps them.
func logicalLines(script string) []string {
	var (
		logical []string
		pending string
	)

	for _, line := range effectiveLines(script) {
		if pending != "" {
			line = pending + " " + line
			pending = ""
		}

		switch {
		case strings.HasSuffix(line, "\\"):
			pending = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		case strings.HasSuffix(line, "||"), strings.HasSuffix(line, "&&"), strings.HasSuffix(line, "|"):
			pending = line
		default:
			logical = append(logical, line)
		}
	}

	if pending != "" {
		logical = append(logical, pending)
	}

	return logical
}

// TestPostinstallSetEIsFirstEffectiveLine enforces D3/K1's fail-fast opener:
// the first thing the script does, before any command that could fail, is turn
// on errexit.
func TestPostinstallSetEIsFirstEffectiveLine(t *testing.T) {
	script := readPostinstall(t)

	require.True(t, strings.HasPrefix(script, "#!/bin/sh\n"),
		"%s must be a POSIX sh script", postinstallPath)

	lines := effectiveLines(script)
	require.NotEmpty(t, lines)
	require.Equal(t, "set -e", lines[0],
		"the first non-comment, non-shebang line of %s must be `set -e`", postinstallPath)
}

// TestPostinstallShellcheck runs the real shellcheck in POSIX sh mode. Per J1
// there is no hand-written substitute: when shellcheck is absent the test skips
// with a logged reason, and .docker/Dockerfile installs it so `make docker-ci`
// runs this check for real.
func TestPostinstallShellcheck(t *testing.T) {
	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skipf("skipping: shellcheck is not on PATH (%v); `.docker/Dockerfile` installs it so this check runs under `make docker-ci`", err)
	}

	t.Logf("using shellcheck at %s", shellcheck)

	out, err := exec.Command(shellcheck, "--shell=sh", postinstallPath).CombinedOutput()
	require.NoErrorf(t, err, "shellcheck --shell=sh %s reported problems:\n%s", postinstallPath, out)
	require.Emptyf(t, strings.TrimSpace(string(out)),
		"shellcheck --shell=sh %s must emit no warnings, got:\n%s", postinstallPath, out)
}

// TestPostinstallSysusersInvocationIsPackageScoped enforces H4: every
// systemd-sysusers invocation names this package's shipped configuration file,
// and none is bare.
func TestPostinstallSysusersInvocationIsPackageScoped(t *testing.T) {
	script := readPostinstall(t)

	var invocations []string

	for _, line := range logicalLines(script) {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "systemd-sysusers" {
			invocations = append(invocations, line)
		}
	}

	require.NotEmpty(t, invocations,
		"%s must invoke systemd-sysusers", postinstallPath)

	for _, invocation := range invocations {
		fields := strings.Fields(invocation)
		require.Greaterf(t, len(fields), 1,
			"systemd-sysusers is invoked bare (no arguments) in %q", invocation)
		require.Containsf(t, fields[1:], sysusersConf,
			"systemd-sysusers invocation %q must name %s explicitly", invocation, sysusersConf)
	}

	require.Contains(t, script, "command -v systemd-sysusers >/dev/null 2>&1",
		"the sysusers branch must be guarded by a systemd-sysusers availability check")
}

// TestPostinstallPrefixesOnlyFilesystemOperands enforces both halves of K1: the
// three filesystem operands carry the root prefix, and the sysusers positional
// configuration path, the fallback account's home, and its shell do not.
func TestPostinstallPrefixesOnlyFilesystemOperands(t *testing.T) {
	lines := effectiveLines(readPostinstall(t))

	prefixed := regexp.MustCompile(regexp.QuoteMeta(rootPrefix) + `/etc/pilothouse`)

	for _, line := range lines {
		remainder := prefixed.ReplaceAllString(line, "")
		require.NotContainsf(t, remainder, "/etc/pilothouse",
			"every /etc/pilothouse operand must be prefixed by %s, but %q has an unprefixed one",
			rootPrefix, line)
	}

	for _, unprefixed := range []string{sysusersConf, "/nonexistent", "/usr/sbin/nologin"} {
		for _, line := range lines {
			require.NotContainsf(t, line, rootPrefix+unprefixed,
				"%s must not be prefixed by %s (it is not a filesystem operand this script acts on): %q",
				unprefixed, rootPrefix, line)
		}
	}
}

// TestPostinstallOperandsExpandToProductionPaths is J1's static substitute for
// running the script with the prefix unset (which would target the real /etc).
// Expanding each operand expression with an empty PILOTHOUSE_ROOT must yield
// exactly the production path.
func TestPostinstallOperandsExpandToProductionPaths(t *testing.T) {
	script := readPostinstall(t)

	for _, tc := range []struct {
		name       string
		expression string
		production string
	}{
		{"config directory", rootPrefix + prodConfigDir, prodConfigDir},
		{"credentials directory", rootPrefix + prodCredentialsDir, prodCredentialsDir},
		{"web env file", rootPrefix + prodWebEnv, prodWebEnv},
		{"broker env file", rootPrefix + prodBrokerEnv, prodBrokerEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Containsf(t, script, `"`+tc.expression+`"`,
				"%s must use the quoted operand expression %q", postinstallPath, tc.expression)

			// Statically expand ${PILOTHOUSE_ROOT} to its documented default.
			require.Equal(t, tc.production, strings.ReplaceAll(tc.expression, rootPrefix, ""),
				"with PILOTHOUSE_ROOT unset, %q must expand to the production path", tc.expression)
		})
	}

	require.Contains(t, script, `: "${PILOTHOUSE_ROOT=}"`,
		"%s must document PILOTHOUSE_ROOT's empty default", postinstallPath)
}

// TestPostinstallRepairsBothEnvFilesInsidePresenceGuard enforces F1/G3: both
// env files are iterated, and their chown/chmod sit inside an `[ -e ... ]`
// guard so a deliberately deleted file does not break an upgrade under set -e.
func TestPostinstallRepairsBothEnvFilesInsidePresenceGuard(t *testing.T) {
	script := readPostinstall(t)

	start := strings.Index(script, "for env_file in")
	require.GreaterOrEqual(t, start, 0, "%s must loop over the env files", postinstallPath)

	end := strings.Index(script[start:], "\ndone")
	require.GreaterOrEqual(t, end, 0, "the env-file loop must be closed with `done`")

	loop := script[start : start+end]

	for _, operand := range []string{rootPrefix + prodWebEnv, rootPrefix + prodBrokerEnv} {
		require.Containsf(t, loop, `"`+operand+`"`,
			"the env-file loop must iterate over %q", operand)
	}

	guard := strings.Index(loop, `if [ -e "${env_file}" ]; then`)
	chown := strings.Index(loop, `chown root:pilothouse "${env_file}"`)
	chmod := strings.Index(loop, `chmod 0640 "${env_file}"`)

	// Anchored to a line of its own so the "fi" inside `env_file` cannot be
	// mistaken for the guard's closer.
	closerMatch := regexp.MustCompile(`(?m)^\s*fi\s*$`).FindStringIndex(loop)
	require.NotNil(t, closerMatch, "the presence guard must be closed with `fi`")

	closer := closerMatch[0]

	require.GreaterOrEqual(t, guard, 0, "the env-file repair must sit behind an `[ -e ... ]` presence guard")
	require.Greater(t, chown, guard, "the env-file chown must be inside the presence guard")
	require.Greater(t, chmod, chown, "the env-file chmod must follow the chown inside the presence guard")
	require.Greater(t, closer, chmod, "the presence guard must close after both repair commands")
}

// TestPostinstallContainsAllSixRepairCommands asserts every chown/chmod pair
// D3/F1 require is textually present with the right owner and mode.
func TestPostinstallContainsAllSixRepairCommands(t *testing.T) {
	lines := effectiveLines(readPostinstall(t))

	for _, want := range []string{
		`chown root:pilothouse "` + rootPrefix + prodConfigDir + `"`,
		`chmod 0750 "` + rootPrefix + prodConfigDir + `"`,
		`chown root:root "` + rootPrefix + prodCredentialsDir + `"`,
		`chmod 0700 "` + rootPrefix + prodCredentialsDir + `"`,
		`chown root:pilothouse "${env_file}"`,
		`chmod 0640 "${env_file}"`,
	} {
		require.Containsf(t, lines, want, "%s must contain the repair command %q", postinstallPath, want)
	}
}

// sysusersDeclaration parses packaging/pilothouse.sysusers, the live source of
// truth for the account the fallback must reproduce.
func sysusersDeclaration(t *testing.T) (name, gecos, home, shell string) {
	t.Helper()

	raw, err := os.ReadFile(sysusersPath)
	require.NoErrorf(t, err, "read %s", sysusersPath)

	re := regexp.MustCompile(`(?m)^u\s+(\S+)\s+\S+\s+"([^"]*)"\s+(\S+)\s+(\S+)\s*$`)

	match := re.FindStringSubmatch(string(raw))
	require.Lenf(t, match, 5, "%s does not declare a parseable user line: %s", sysusersPath, raw)

	return match[1], match[2], match[3], match[4]
}

// TestPostinstallFallbackIsGuardedAndMatchesSysusers is H4's structural proof
// for the groupadd/useradd branch. Per J1 no getent- or id-backed fake
// simulates a pre-existing account database and real account creation is never
// exercised here; the fallback's runtime idempotency is deferred to #70's
// install-based verification.
func TestPostinstallFallbackIsGuardedAndMatchesSysusers(t *testing.T) {
	name, gecos, home, shell := sysusersDeclaration(t)
	require.Equal(t, "pilothouse", name, "the sysusers file must declare the pilothouse account")

	var groupadd, useradd string

	for _, line := range logicalLines(readPostinstall(t)) {
		if strings.Contains(line, "groupadd") {
			groupadd = line
		}

		if strings.Contains(line, "useradd") {
			useradd = line
		}
	}

	require.NotEmpty(t, groupadd, "%s must have a groupadd fallback", postinstallPath)
	require.NotEmpty(t, useradd, "%s must have a useradd fallback", postinstallPath)

	guardedGroupadd := regexp.MustCompile(`getent\s+group\s+` + name + `\s+>/dev/null 2>&1\s+\|\|\s+groupadd\b`)
	require.Regexpf(t, guardedGroupadd, groupadd,
		"groupadd must be preceded by its own existence-check guard, got %q", groupadd)

	guardedUseradd := regexp.MustCompile(`getent\s+passwd\s+` + name + `\s+>/dev/null 2>&1\s+\|\|\s+useradd\b`)
	require.Regexpf(t, guardedUseradd, useradd,
		"useradd must be preceded by its own existence-check guard, got %q", useradd)

	require.Contains(t, groupadd, "--system "+name, "the fallback group must be a system group")

	// Every property packaging/pilothouse.sysusers declares, read from that
	// file rather than restated here, so the two can never silently diverge.
	for _, want := range []string{
		"--system",
		"--gid " + name,
		"--home-dir " + home,
		"--shell " + shell,
		`--comment "` + gecos + `"`,
	} {
		require.Containsf(t, useradd, want,
			"the fallback useradd must declare %q to reproduce %s, got %q", want, sysusersPath, useradd)
	}

	require.True(t, strings.HasSuffix(useradd, " "+name),
		"the fallback useradd must create the %s account, got %q", name, useradd)
	require.Equal(t, "/nonexistent", home, "the sysusers home must remain /nonexistent")
	require.Equal(t, "/usr/sbin/nologin", shell, "the sysusers shell must remain a nologin shell")
}

// fakeCommand is the stand-in installed on PATH for chown, chmod, and
// systemd-sysusers. It appends its own name and arguments to a shared log and,
// when told to, fails for one specific command/operand pair. It calls no
// external program, so it works with a PATH that contains only the fake
// directory.
const fakeCommand = `#!/bin/sh
name="${0##*/}"
line="${name}"
for arg in "$@"; do
    line="${line} ${arg}"
done
printf '%s\n' "${line}" >> "${PILOTHOUSE_FAKE_LOG}"

if [ -n "${PILOTHOUSE_FAIL_CMD}" ] && [ "${name}" = "${PILOTHOUSE_FAIL_CMD}" ]; then
    for arg in "$@"; do
        case "${arg}" in
            *"${PILOTHOUSE_FAIL_ARG}") exit 7 ;;
        esac
    done
fi

exit 0
`

// fakeEnv is a temporary install root plus the PATH-injected fakes that stand
// in for the privileged commands the scriptlet runs.
type fakeEnv struct {
	root    string
	binDir  string
	logPath string
}

// newFakeEnv builds the install root the package would have unpacked, creating
// only the env files named in present so the absent-file branch is reachable.
func newFakeEnv(t *testing.T, present ...string) *fakeEnv {
	t.Helper()

	env := &fakeEnv{
		root:    t.TempDir(),
		binDir:  t.TempDir(),
		logPath: filepath.Join(t.TempDir(), "calls.log"),
	}

	require.NoError(t, os.MkdirAll(filepath.Join(env.root, prodCredentialsDir), 0o755))

	for _, name := range present {
		require.NoError(t, os.WriteFile(filepath.Join(env.root, prodConfigDir, name), []byte("# test\n"), 0o600))
	}

	for _, name := range []string{"chown", "chmod", "systemd-sysusers"} {
		require.NoError(t, os.WriteFile(filepath.Join(env.binDir, name), []byte(fakeCommand), 0o700)) //nolint:gosec // PATH-injected fake, temp dir only
	}

	return env
}

// run executes the real packaging/postinstall.sh against the fake root. When
// failCmd is non-empty the fake with that name exits non-zero for the argument
// ending in failArg, which is how the forced-failure cases are injected.
func (e *fakeEnv) run(t *testing.T, failCmd, failArg string) error {
	t.Helper()

	script, err := filepath.Abs(postinstallPath)
	require.NoError(t, err)

	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(),
		"PILOTHOUSE_ROOT="+e.root,
		"PATH="+e.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PILOTHOUSE_FAKE_LOG="+e.logPath,
		"PILOTHOUSE_FAIL_CMD="+failCmd,
		"PILOTHOUSE_FAIL_ARG="+failArg,
	)

	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		t.Logf("postinstall output:\n%s", out)
	}

	return err
}

// calls returns every command the fakes have recorded so far, across all
// invocations of the script.
func (e *fakeEnv) calls(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(e.logPath)
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// expectedCalls is the exact, ordered command sequence one run of the script
// must produce against a fake root, given which env files exist.
func (e *fakeEnv) expectedCalls(present ...string) []string {
	calls := []string{
		"systemd-sysusers --root=" + e.root + " " + sysusersConf,
		"chown root:pilothouse " + e.root + prodConfigDir,
		"chmod 0750 " + e.root + prodConfigDir,
		"chown root:root " + e.root + prodCredentialsDir,
		"chmod 0700 " + e.root + prodCredentialsDir,
	}

	// The script repairs the env files in declaration order.
	for _, name := range []string{"pilothouse.env", "pilothoused.env"} {
		for _, have := range present {
			if have != name {
				continue
			}

			path := filepath.Join(e.root, prodConfigDir, name)
			calls = append(calls,
				"chown root:pilothouse "+path,
				"chmod 0640 "+path,
			)
		}
	}

	return calls
}

// TestPostinstallRepairsEverythingWhenBothEnvFilesPresent is the happy path:
// the sysusers invocation is package-scoped and root-scoped, and all six repair
// commands run against the prefixed operands.
func TestPostinstallRepairsEverythingWhenBothEnvFilesPresent(t *testing.T) {
	env := newFakeEnv(t, "pilothouse.env", "pilothoused.env")

	require.NoError(t, env.run(t, "", ""), "postinstall must exit zero when every repair succeeds")
	require.Equal(t, env.expectedCalls("pilothouse.env", "pilothoused.env"), env.calls(t))
}

// TestPostinstallSkipsAbsentEnvFile covers G3: a deliberately deleted env file
// leaves the script exiting zero and is never chowned or chmoded.
func TestPostinstallSkipsAbsentEnvFile(t *testing.T) {
	env := newFakeEnv(t, "pilothouse.env")

	require.NoError(t, env.run(t, "", ""), "an absent env file must not fail the script")

	calls := env.calls(t)
	require.Equal(t, env.expectedCalls("pilothouse.env"), calls)

	for _, call := range calls {
		require.NotContains(t, call, "pilothoused.env",
			"the absent env file must never be passed to chown or chmod")
	}
}

// TestPostinstallIsIdempotentOnRerun re-runs the same script against the same
// root with the same fakes, as an upgrade would. Both runs must exit zero and
// re-apply an identical set of package-scoped and prefixed commands, and the
// absent env file must stay absent and unrepaired across both.
func TestPostinstallIsIdempotentOnRerun(t *testing.T) {
	env := newFakeEnv(t, "pilothouse.env")

	require.NoError(t, env.run(t, "", ""), "first invocation must exit zero")
	require.NoError(t, env.run(t, "", ""), "re-running against an already-configured root must exit zero")

	oneRun := env.expectedCalls("pilothouse.env")
	require.Equal(t, append(append([]string(nil), oneRun...), oneRun...), env.calls(t),
		"both invocations must issue the identical command sequence")

	var sysusers []string

	for _, call := range env.calls(t) {
		if strings.HasPrefix(call, "systemd-sysusers ") {
			sysusers = append(sysusers, call)
		}

		require.NotContains(t, call, "pilothoused.env",
			"the env file absent on the first run must stay unrepaired on the second")
	}

	require.Len(t, sysusers, 2, "each invocation must call systemd-sysusers exactly once")
	require.Equal(t, sysusers[0], sysusers[1],
		"systemd-sysusers must receive identical package-scoped arguments on both runs")
	require.Contains(t, sysusers[0], sysusersConf)

	_, err := os.Stat(filepath.Join(env.root, prodConfigDir, "pilothoused.env"))
	require.True(t, os.IsNotExist(err), "the script must never recreate a deleted env file")
}

// TestPostinstallFailsFastOnAnyRepairCommand is D3/G2's fail-fast requirement,
// parameterized over all six repair commands rather than a hand-picked subset.
// A forced failure of any one of them must abort the script with a non-zero
// exit, and nothing after the failing command may run.
func TestPostinstallFailsFastOnAnyRepairCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failCmd string
		failArg string
	}{
		{"config directory chown", "chown", prodConfigDir},
		{"config directory chmod", "chmod", prodConfigDir},
		{"credentials directory chown", "chown", prodCredentialsDir},
		{"credentials directory chmod", "chmod", prodCredentialsDir},
		{"env file chown", "chown", prodWebEnv},
		{"env file chmod", "chmod", prodWebEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newFakeEnv(t, "pilothouse.env", "pilothoused.env")

			require.Errorf(t, env.run(t, tc.failCmd, tc.failArg),
				"a failing %s on %s must abort the script with a non-zero exit", tc.failCmd, tc.failArg)

			calls := env.calls(t)
			all := env.expectedCalls("pilothouse.env", "pilothoused.env")

			require.NotEmpty(t, calls, "the forced-failure command must still have been invoked")
			require.Less(t, len(calls), len(all),
				"the forced failure must stop the script before its remaining repairs")

			last := calls[len(calls)-1]
			require.Contains(t, last, tc.failArg,
				"the last recorded call must be the one forced to fail")
			require.True(t, strings.HasPrefix(last, tc.failCmd+" "),
				"the last recorded call must be the forced-failure command, got %q", last)
			require.Equal(t, all[:len(calls)], calls,
				"set -e must abort immediately: no command after the failing one may run")
		})
	}
}
