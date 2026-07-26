package packaging

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// verifyInstallPath is the install-validation script this file guards. It is
// NOT a packaged file: it is never embedded by contract.go, never named by a
// packaging.Verify table, and never listed as an nfpm content entry.
const verifyInstallPath = "verify-install.sh"

// installedSysusersConf is the installed sysusers declaration the script must
// read the account expectations from, instead of hardcoding a copy of
// packaging/pilothouse.sysusers.
const installedSysusersConf = "/usr/lib/sysusers.d/pilothouse.conf"

// bootedHostCommands are the commands that would require a booted host: a
// running service manager, a restarted machine, or an SELinux policy in
// enforcing mode. Layer A runs in a container, so none of them may appear.
var bootedHostCommands = []string{"systemctl", "reboot", "setenforce", "getenforce", "systemd-run"}

func readVerifyInstall(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(verifyInstallPath)
	require.NoErrorf(t, err, "read %s", verifyInstallPath)

	return string(raw)
}

// TestVerifyInstallIsAnExecutablePOSIXShellScript pins the two properties the
// make target and CI job that will invoke the script depend on: it is
// executable, and it is a POSIX sh script.
func TestVerifyInstallIsAnExecutablePOSIXShellScript(t *testing.T) {
	info, err := os.Stat(verifyInstallPath)
	require.NoErrorf(t, err, "stat %s", verifyInstallPath)
	require.NotZerof(t, info.Mode().Perm()&0o111, "%s must be executable, mode is %v", verifyInstallPath, info.Mode())

	require.True(t, strings.HasPrefix(readVerifyInstall(t), "#!/bin/sh\n"),
		"%s must be a POSIX sh script", verifyInstallPath)
}

// TestVerifyInstallPreamble enforces the fail-fast opener: before any command
// that could fail, the script turns on errexit and nounset. It reuses
// postinstall_test.go's effective-lines helper so a `set -eu` merely mentioned
// in the header comment cannot satisfy it.
func TestVerifyInstallPreamble(t *testing.T) {
	lines := effectiveLines(readVerifyInstall(t))
	require.NotEmpty(t, lines)
	require.Equal(t, "set -eu", lines[0],
		"the first non-comment, non-blank, non-shebang line of %s must be `set -eu`", verifyInstallPath)
}

// TestVerifyInstallShellcheck runs the real shellcheck in POSIX sh mode. There
// is no hand-written substitute: when shellcheck is absent the test skips with
// a logged reason, and .docker/Dockerfile installs it so `make docker-ci` runs
// this check for real.
func TestVerifyInstallShellcheck(t *testing.T) {
	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skipf("skipping: shellcheck is not on PATH (%v); `.docker/Dockerfile` installs it so this check runs under `make docker-ci`", err)
	}

	t.Logf("using shellcheck at %s", shellcheck)

	out, err := exec.Command(shellcheck, "--shell=sh", verifyInstallPath).CombinedOutput()
	require.NoErrorf(t, err, "shellcheck --shell=sh %s reported problems:\n%s", verifyInstallPath, out)
	require.Emptyf(t, strings.TrimSpace(string(out)),
		"shellcheck --shell=sh %s must emit no warnings, got:\n%s", verifyInstallPath, out)
}

// TestVerifyInstallFailsOnFirstAssertion asserts the abort contract: fail()
// prints a `verify-install: ` prefixed message to standard error and exits
// non-zero, so the first failed assertion ends the run.
func TestVerifyInstallFailsOnFirstAssertion(t *testing.T) {
	script := readVerifyInstall(t)

	require.Contains(t, script, "fail() {\n    printf 'verify-install: %s\\n' \"$1\" >&2\n    exit 1\n}",
		"%s must define fail() as a stderr message followed by an immediate non-zero exit", verifyInstallPath)
}

// TestVerifyInstallRequiresAnArtifactDirectoryOperand asserts the usage
// contract: there is no default artifact directory, and a missing or
// non-directory operand is an actionable failure that names the usage.
func TestVerifyInstallRequiresAnArtifactDirectoryOperand(t *testing.T) {
	script := readVerifyInstall(t)

	require.Contains(t, script, "usage: packaging/verify-install.sh <artifact-dir>",
		"%s must print its usage", verifyInstallPath)

	lines := effectiveLines(script)
	require.Contains(t, lines, `if [ "$#" -ne 1 ]; then`,
		"%s must reject anything but exactly one operand", verifyInstallPath)
	require.Contains(t, lines, `if [ ! -d "$1" ]; then`,
		"%s must reject an operand that is not a directory", verifyInstallPath)
}

// TestVerifyInstallDetectsFormatFromThePackageManager enforces that the format
// comes from the container's own package manager and never from an artifact's
// file name, and that exactly the amd64 artifacts are selected.
func TestVerifyInstallDetectsFormatFromThePackageManager(t *testing.T) {
	lines := effectiveLines(readVerifyInstall(t))

	for _, want := range []string{
		"if command -v apt-get >/dev/null 2>&1; then",
		"format=deb",
		"elif command -v dnf >/dev/null 2>&1; then",
		"format=rpm",
	} {
		require.Containsf(t, lines, want,
			"%s must detect the package format from the container's package manager (%q)", verifyInstallPath, want)
	}

	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, `"${artifact_dir}"/*_amd64.deb`,
		"%s must select the amd64 deb artifact", verifyInstallPath)
	require.Contains(t, joined, `"${artifact_dir}"/*.x86_64.rpm`,
		"%s must select the x86_64 rpm artifact", verifyInstallPath)
	require.NotContains(t, joined, "arm64",
		"arm64 artifacts are out of scope: %s must not select them", verifyInstallPath)
}

// TestVerifyInstallInstallsThroughThePackageManager is check 1's guard: the
// install must go through the distro package manager with real dependency
// resolution, never through a bare `dpkg -i` or `rpm -i` installer, which
// would not prove the per-format dependency lists resolve.
func TestVerifyInstallInstallsThroughThePackageManager(t *testing.T) {
	lines := effectiveLines(readVerifyInstall(t))
	joined := strings.Join(lines, "\n")

	require.Contains(t, joined, "apt-get update", "%s must refresh the deb repositories", verifyInstallPath)
	require.Contains(t, joined, `apt-get install -y "${artifact}"`,
		"%s must install the deb through apt-get", verifyInstallPath)
	require.Contains(t, joined, `dnf install -y "${artifact}"`,
		"%s must install the rpm through dnf", verifyInstallPath)

	bare := regexp.MustCompile(`(^|[^-a-z])(dpkg -i|rpm -i)`)
	require.NotRegexpf(t, bare, joined,
		"%s must not install with a bare `dpkg -i`/`rpm -i`: check 1 exists to resolve the per-format dependency lists", verifyInstallPath)
}

// TestVerifyInstallAccountCheckReadsTheInstalledSysusersFile enforces that the
// account expectations come from the installed sysusers declaration, the live
// source of truth, rather than from a hardcoded copy of the values in
// packaging/pilothouse.sysusers.
func TestVerifyInstallAccountCheckReadsTheInstalledSysusersFile(t *testing.T) {
	script := readVerifyInstall(t)

	require.Containsf(t, script, installedSysusersConf,
		"%s must read the account declaration from %s", verifyInstallPath, installedSysusersConf)

	for _, hardcoded := range []string{"/nonexistent", "/usr/sbin/nologin"} {
		require.NotContainsf(t, script, hardcoded,
			"%s must not hardcode %s: it is parsed from %s at run time",
			verifyInstallPath, hardcoded, installedSysusersConf)
	}

	lines := effectiveLines(script)
	joined := strings.Join(lines, "\n")

	for _, want := range []string{
		`getent passwd "${account}"`,
		`getent group "${account}"`,
		"check_account() {",
		"check_owner_mode() {",
	} {
		require.Containsf(t, joined, want, "%s must contain %q", verifyInstallPath, want)
	}

	require.Contains(t, joined, `[ "${account_uid}" -lt 1000 ]`,
		"%s must assert the account's uid is in the system range", verifyInstallPath)
	require.Contains(t, joined, `[ "${account_gid}" = "${group_gid}" ]`,
		"%s must assert the account's primary group is the pilothouse group", verifyInstallPath)
}

// TestVerifyInstallReadsOwnershipFromTheFilesystem enforces that check 3 reads
// owner, group and mode with stat from the installed filesystem, never from
// package metadata.
func TestVerifyInstallReadsOwnershipFromTheFilesystem(t *testing.T) {
	joined := strings.Join(effectiveLines(readVerifyInstall(t)), "\n")

	require.Contains(t, joined, `stat -c '%U %G %04a'`,
		"%s must read owner, group and mode from the installed filesystem with stat", verifyInstallPath)

	for _, metadata := range []string{"dpkg-query", "dpkg-deb", "rpm -q", "rpm --query"} {
		require.NotContainsf(t, joined, metadata,
			"%s must not read ownership from package metadata (%q)", verifyInstallPath, metadata)
	}
}

// TestVerifyInstallNeedsNoBootedHost enforces the container-only invariant:
// the script never runs a command that would require systemd as PID 1, a
// restarted machine, or an SELinux policy in enforcing mode.
func TestVerifyInstallNeedsNoBootedHost(t *testing.T) {
	for _, line := range effectiveLines(readVerifyInstall(t)) {
		for _, command := range bootedHostCommands {
			require.NotRegexpf(t, regexp.MustCompile(`(^|[^-a-zA-Z])`+regexp.QuoteMeta(command)),
				line, "%s must not use %s: it runs in a container, not on a booted host", verifyInstallPath, command)
		}
	}
}

// TestVerifyInstallNeverAssertsSystemdManagedPaths enforces that the two
// directories systemd's RuntimeDirectory=/StateDirectory= own are never
// mentioned: they are deliberately unpackaged, and a container has no running
// systemd to create them.
func TestVerifyInstallNeverAssertsSystemdManagedPaths(t *testing.T) {
	script := readVerifyInstall(t)

	for _, managed := range systemdManagedPaths {
		require.NotContainsf(t, script, managed,
			"%s must never assert %s: systemd owns it and it is deliberately unpackaged", verifyInstallPath, managed)
	}
}

// ownerMode is one expected owner/group/mode triple for a packaged
// destination, in the script's textual form.
type ownerMode struct {
	Owner string
	Group string
	Mode  string
}

func (o ownerMode) String() string {
	return fmt.Sprintf("%s %s %s", o.Owner, o.Group, o.Mode)
}

// expectOwnerModeLine matches one `expect_owner_mode <path> <owner> <group>
// <mode>` call with literal arguments. The trailing anchor rejects a call with
// extra operands, and the leading anchor rejects the function's own definition.
var expectOwnerModeLine = regexp.MustCompile(`^expect_owner_mode (/\S+) (\S+) (\S+) (\S+)$`)

// scriptOwnerModeExpectations parses every expectation line out of the script.
func scriptOwnerModeExpectations(t *testing.T) map[string]ownerMode {
	t.Helper()

	expectations := make(map[string]ownerMode)

	for _, line := range effectiveLines(readVerifyInstall(t)) {
		match := expectOwnerModeLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		_, duplicate := expectations[match[1]]
		require.Falsef(t, duplicate, "%s expects %s twice", verifyInstallPath, match[1])

		expectations[match[1]] = ownerMode{Owner: match[2], Group: match[3], Mode: match[4]}
	}

	return expectations
}

// configOwnerModeExpectations reads the live ../.goreleaser.yaml and returns
// every content entry of the given format that carries a file_info block,
// keyed by destination.
func configOwnerModeExpectations(t *testing.T, format string) map[string]ownerMode {
	t.Helper()

	override := loadOverride(t, loadNFPMEntry(t), format)
	expectations := make(map[string]ownerMode)

	for _, entry := range override.Contents {
		if entry.FileInfo == nil {
			continue
		}

		_, duplicate := expectations[entry.Dst]
		require.Falsef(t, duplicate, "%s declares %s twice for format %s", goreleaserConfigPath, entry.Dst, format)

		expectations[entry.Dst] = ownerMode{
			Owner: entry.FileInfo.Owner,
			Group: entry.FileInfo.Group,
			// The YAML mode is an octal literal, so it decodes to an
			// int; render it the way the script writes it.
			Mode: fmt.Sprintf("%04o", entry.FileInfo.Mode),
		}
	}

	return expectations
}

func sortedKeys(expectations map[string]ownerMode) []string {
	keys := make([]string, 0, len(expectations))
	for key := range expectations {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

// TestVerifyInstallOwnerModeExpectationsMatchGoreleaserConfig is the
// bidirectional drift guard between the script's expectation lines and the
// live ../.goreleaser.yaml. It is a set equality in both directions: an
// unauthorized expectation line in the script fails it, and a packaged
// file_info entry with no expectation line fails it too. The comparison runs
// against BOTH the deb and the rpm override, so a format-specific divergence
// cannot hide behind the other format.
func TestVerifyInstallOwnerModeExpectationsMatchGoreleaserConfig(t *testing.T) {
	script := scriptOwnerModeExpectations(t)
	require.NotEmpty(t, script, "%s must declare expect_owner_mode expectations", verifyInstallPath)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			config := configOwnerModeExpectations(t, format)

			require.Equal(t, sortedKeys(config), sortedKeys(script),
				"the destinations %s expects must be exactly the %s destinations %s packages with a file_info block",
				verifyInstallPath, format, goreleaserConfigPath)

			for _, dst := range sortedKeys(config) {
				require.Equalf(t, config[dst].String(), script[dst].String(),
					"%s expects %q for %s, but %s packages it as %q in the %s override",
					verifyInstallPath, script[dst], dst, goreleaserConfigPath, config[dst], format)
			}
		})
	}
}
