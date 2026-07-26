package packaging

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

// TestVerifyInstallIsAnExecutablePOSIXShellScript pins two properties any
// caller of the script depends on: it is executable, and it is a POSIX sh
// script.
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

// TestVerifyInstallAccountCheckPinsTheAccountName enforces the one thing check
// 2 must NOT take from the installed file on trust. Every other account
// property is deliberately parsed from the shipped sysusers declaration, so
// without this pin the check degenerates into "some valid system account
// exists and is self-consistent" -- a package declaring an entirely different
// user would satisfy every remaining assertion.
func TestVerifyInstallAccountCheckPinsTheAccountName(t *testing.T) {
	joined := strings.Join(effectiveLines(readVerifyInstall(t)), "\n")

	require.Containsf(t, joined, "expected_account=pilothouse",
		"%s must pin the expected account name", verifyInstallPath)

	require.Containsf(t, joined, `[ "${account}" = "${expected_account}" ] ||`,
		"%s must assert the declared account name is the expected one", verifyInstallPath)

	require.Containsf(t, joined, `removal_account="${expected_account}"`,
		"%s must assert removal against the pinned account, not a re-parse of a file the removal deletes",
		verifyInstallPath)

	require.NotContainsf(t, joined, "removal_account=$(sysusers_field name)",
		"%s must not re-derive the removal account from the declaration being removed",
		verifyInstallPath)
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

// installedPAMPolicy is the installed PAM policy check 4 must derive both of
// its expectation lists from, instead of hardcoding a per-distro table.
const installedPAMPolicy = "/etc/pam.d/pilothouse"

// packagedUnitPrefix is the directory both nfpm overrides install unit files
// into. The unit drift guard compares the script's expectations against
// exactly the live destinations under it.
const packagedUnitPrefix = "/usr/lib/systemd/system"

// perDistroPAMLiterals are the stack names, module names and multiarch module
// directory a per-distro table would have to spell out. Check 4 derives all of
// them from the installed policy at run time, so none may appear in the script.
var perDistroPAMLiterals = []string{
	"common-auth",
	"common-account",
	"password-auth",
	"pam_nologin.so",
	"x86_64-linux-gnu/security",
	"aarch64-linux-gnu/security",
}

// expectUnitLine matches one `expect_unit <path>` call with a literal
// argument. As with expectOwnerModeLine the anchors reject both the function's
// own definition and a call with extra operands.
var expectUnitLine = regexp.MustCompile(`^expect_unit (/\S+)$`)

// expectLinkedLine matches one `expect_linked <path>` call with a literal
// argument.
var expectLinkedLine = regexp.MustCompile(`^expect_linked (/\S+)$`)

// scriptPaths collects the literal path argument of every line of the script
// matching pattern, rejecting a duplicate expectation.
func scriptPaths(t *testing.T, pattern *regexp.Regexp) []string {
	t.Helper()

	var paths []string

	for _, line := range effectiveLines(readVerifyInstall(t)) {
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		require.NotContainsf(t, paths, match[1], "%s expects %s twice", verifyInstallPath, match[1])
		paths = append(paths, match[1])
	}

	sort.Strings(paths)

	return paths
}

// TestVerifyInstallPAMCheckReadsTheInstalledPolicy is check 4's structural
// guard. The check must name the installed policy and must contain no literal
// stack name, module name or multiarch module directory: every expectation is
// parsed out of whichever policy the format's override actually shipped.
func TestVerifyInstallPAMCheckReadsTheInstalledPolicy(t *testing.T) {
	script := readVerifyInstall(t)

	require.Containsf(t, script, installedPAMPolicy,
		"%s must read the PAM expectations from the installed %s", verifyInstallPath, installedPAMPolicy)

	for _, source := range []string{"packaging/pilothouse.pam", "packaging/rpm/pilothouse.pam"} {
		require.NotContainsf(t, script, source,
			"%s must parse the INSTALLED policy, not the repository source %s", verifyInstallPath, source)
	}

	for _, literal := range perDistroPAMLiterals {
		require.NotContainsf(t, script, literal,
			"%s must not hardcode the per-distro PAM expectation %q: check 4 derives it from %s at run time",
			verifyInstallPath, literal, installedPAMPolicy)
	}

	joined := strings.Join(effectiveLines(script), "\n")

	for _, want := range []string{
		"check_pam() {",
		"pam_stacks() {",
		"pam_modules() {",
		"pam_module_dirs() {",
		`[ -f "/etc/pam.d/${stack}" ]`,
		`[ -f "${dir}/${module}" ]`,
	} {
		require.Containsf(t, joined, want, "%s must contain %q", verifyInstallPath, want)
	}
}

// TestVerifyInstallPAMCheckRejectsAnEmptyParse asserts the explicit emptiness
// guard: a policy that yields no stacks or no modules is a failure, so a
// mis-parse cannot pass vacuously by asserting nothing at all. Each guard's
// fallback must be `fail` itself — a guard whose right-hand side is anything
// else (a `true`, a log line) aborts nothing. The guard is asserted by parsing
// the script text; nothing here executes it.
func TestVerifyInstallPAMCheckRejectsAnEmptyParse(t *testing.T) {
	joined := strings.Join(effectiveLines(readVerifyInstall(t)), "\n")

	for _, list := range []string{"stacks", "modules", "module_dirs"} {
		guard := regexp.MustCompile(`\[ -n "\$\{` + list + `\}" \] \|\|\n?\s*fail `)
		require.Regexpf(t, guard, joined,
			"%s must call fail when the installed policy yields no %s", verifyInstallPath, list)
	}
}

// TestVerifyInstallVerifiesUnitsWithSystemdAnalyze is check 5's structural
// guard: the units are validated with the distro's own offline validator, and
// never by driving a service manager — which a container has none of.
func TestVerifyInstallVerifiesUnitsWithSystemdAnalyze(t *testing.T) {
	joined := strings.Join(effectiveLines(readVerifyInstall(t)), "\n")

	require.Contains(t, joined, `systemd-analyze verify "$1"`,
		"%s must validate the installed units with systemd-analyze verify", verifyInstallPath)

	require.NotRegexpf(t, regexp.MustCompile(`(^|[^-a-z])systemctl`), joined,
		"%s must never use systemctl: systemd-analyze verify is offline, systemctl needs PID 1", verifyInstallPath)
}

// TestVerifyInstallUnitExpectationsMatchGoreleaserConfig is the bidirectional
// drift guard for check 5: the set of paths the script verifies equals the set
// of destinations under /usr/lib/systemd/system the live ../.goreleaser.yaml
// packages, in both directions and for both overrides. A newly packaged unit
// with no expectation line fails it, and a stray expectation line does too.
func TestVerifyInstallUnitExpectationsMatchGoreleaserConfig(t *testing.T) {
	script := scriptPaths(t, expectUnitLine)
	require.NotEmpty(t, script, "%s must declare expect_unit expectations", verifyInstallPath)

	entry := loadNFPMEntry(t)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			var packaged []string

			for _, content := range loadOverride(t, entry, format).Contents {
				if content.Dst == packagedUnitPrefix || strings.HasPrefix(content.Dst, packagedUnitPrefix+"/") {
					require.NotContainsf(t, packaged, content.Dst,
						"%s packages %s twice in the %s override", goreleaserConfigPath, content.Dst, format)
					packaged = append(packaged, content.Dst)
				}
			}

			sort.Strings(packaged)

			require.Equalf(t, packaged, script,
				"the units %s verifies must be exactly the %s destinations %s packages under %s",
				verifyInstallPath, format, goreleaserConfigPath, packagedUnitPrefix)
		})
	}
}

// The types below model only what the linkage guard needs from
// .goreleaser.yaml's builds section: each build's binary name and its env
// block. drift_test.go's buildTarget decodes `binary` alone and no existing
// test file may be modified, so this guard declares its own local types with
// non-colliding names.

type cgoBuildTarget struct {
	Binary string   `yaml:"binary"`
	Env    []string `yaml:"env"`
}

type cgoBuildsConfig struct {
	Builds []cgoBuildTarget `yaml:"builds"`
}

// binaryDestinationsByCGO parses the live config and returns the /usr/bin
// destinations of the builds that enable cgo and of those that disable it.
func binaryDestinationsByCGO(t *testing.T) (enabled, disabled []string) {
	t.Helper()

	raw, err := os.ReadFile(goreleaserConfigPath)
	require.NoErrorf(t, err, "read %s", goreleaserConfigPath)

	var cfg cgoBuildsConfig
	require.NoErrorf(t, yaml.Unmarshal(raw, &cfg), "parse %s", goreleaserConfigPath)
	require.NotEmptyf(t, cfg.Builds, "%s must declare builds", goreleaserConfigPath)

	for _, build := range cfg.Builds {
		require.NotEmpty(t, build.Binary, "every build must name a binary")

		dest := usrBinDir + "/" + build.Binary

		switch {
		case slices.Contains(build.Env, "CGO_ENABLED=1"):
			enabled = append(enabled, dest)
		case slices.Contains(build.Env, "CGO_ENABLED=0"):
			disabled = append(disabled, dest)
		default:
			t.Fatalf("build %q declares neither CGO_ENABLED=1 nor CGO_ENABLED=0; %s cannot classify it", build.Binary, verifyInstallPath)
		}
	}

	sort.Strings(enabled)
	sort.Strings(disabled)

	return enabled, disabled
}

// TestVerifyInstallLinkedBinariesMatchBuilds is the bidirectional drift guard
// for check 6. The set of paths the script runs ldd against must equal the set
// of /usr/bin destinations of the live config's cgo-enabled builds — today
// exactly /usr/bin/pilothoused — so flipping the other binary to cgo forces
// its addition here. The converse fact is asserted too: no build that disables
// cgo may appear, because ldd exits non-zero on a static binary and requiring
// it to succeed would fail the gate for a reason unrelated to the dependency
// lists.
func TestVerifyInstallLinkedBinariesMatchBuilds(t *testing.T) {
	script := scriptPaths(t, expectLinkedLine)
	enabled, disabled := binaryDestinationsByCGO(t)

	require.NotEmptyf(t, enabled, "%s must declare at least one cgo-enabled build", goreleaserConfigPath)
	require.Equalf(t, enabled, script,
		"the binaries %s runs ldd against must be exactly the %s destinations of the CGO_ENABLED=1 builds in %s",
		verifyInstallPath, usrBinDir, goreleaserConfigPath)

	for _, static := range disabled {
		require.NotContainsf(t, script, static,
			"%s must not run ldd against %s: it is built with CGO_ENABLED=0, so ldd exits non-zero for it",
			verifyInstallPath, static)
	}
}

// installedConfigDir and installedPAMDir are the two directories the packaged
// config files live in. Check 8 sweeps them for a stray `.rpmsave`; nothing in
// the script may assert that the first one is itself pruned.
const (
	installedConfigDir = "/etc/pilothouse"
	installedPAMDir    = "/etc/pam.d"
)

// removalSectionAnchor is the line that opens check 8. Every structural
// assertion about the removal matrix runs against the lines after it, so the
// `deb)`/`rpm)` case labels of the artifact-selection, install and reinstall
// blocks cannot be mistaken for the removal block's.
const removalSectionAnchor = `removal_account="${expected_account}"`

// expectConffileLine and expectRemovedLine match one `expect_conffile <path>` /
// `expect_removed <path>` call with a literal argument. As with the c1 and c2
// expectation regexps the anchors reject both the function's own definition and
// a call carrying extra operands.
var (
	expectConffileLine = regexp.MustCompile(`^expect_conffile (/\S+)$`)
	expectRemovedLine  = regexp.MustCompile(`^expect_removed (/\S+)$`)
)

// removalVerbLine matches one removal verb applied to the package name.
var removalVerbLine = regexp.MustCompile(`^(dpkg -r|dpkg -P|rpm -e) "\$\{package_name\}" \|\|$`)

// removalCheckLine matches one `check_<name> "<verb>"` assertion invoked after
// a removal verb. The matrix guard collects every one of them per segment and
// compares the complete list against the cell's want, so an unexpected or
// duplicated assertion fails the cell exactly as a missing one does.
var removalCheckLine = regexp.MustCompile(`^check_\w+ "[^"]*"$`)

// namedNFPMEntry and namedNFPMConfig decode only nfpms[0].package_name.
// goreleaser_config_test.go's nfpmEntry does not carry that field and no
// existing test file may be modified, so the removal guard declares its own.

type namedNFPMEntry struct {
	PackageName string `yaml:"package_name"`
}

type namedNFPMConfig struct {
	NFPMs []namedNFPMEntry `yaml:"nfpms"`
}

// livePackageName reads nfpms[0].package_name out of the live configuration.
func livePackageName(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(goreleaserConfigPath)
	require.NoErrorf(t, err, "read %s", goreleaserConfigPath)

	var cfg namedNFPMConfig
	require.NoErrorf(t, yaml.Unmarshal(raw, &cfg), "parse %s", goreleaserConfigPath)
	require.Lenf(t, cfg.NFPMs, 1, "%s must declare exactly one nfpms entry", goreleaserConfigPath)
	require.NotEmptyf(t, cfg.NFPMs[0].PackageName, "%s must declare nfpms[0].package_name", goreleaserConfigPath)

	return cfg.NFPMs[0].PackageName
}

// removalSection returns the script's effective lines from the removal anchor
// onward.
func removalSection(t *testing.T) []string {
	t.Helper()

	lines := effectiveLines(readVerifyInstall(t))

	index := slices.Index(lines, removalSectionAnchor)
	require.GreaterOrEqualf(t, index, 0,
		"%s must open check 8 with %q so the guard can scope the removal matrix", verifyInstallPath, removalSectionAnchor)

	return lines[index:]
}

// functionBody returns the effective lines between `name() {` and its closing
// brace, so a guard can assert what a named check actually asserts rather than
// that the script mentions it somewhere.
func functionBody(t *testing.T, name string) []string {
	t.Helper()

	lines := effectiveLines(readVerifyInstall(t))

	start := slices.Index(lines, name+"() {")
	require.GreaterOrEqualf(t, start, 0, "%s must define %s()", verifyInstallPath, name)

	end := slices.Index(lines[start:], "}")
	require.Greaterf(t, end, 0, "%s must close %s()", verifyInstallPath, name)

	return lines[start+1 : start+end]
}

// TestVerifyInstallReinstallsAndRepeatsTheAccountAndOwnershipChecks is check
// 7's guard. The reinstall must go through the package manager's own reinstall
// verb applied to the SAME artifact, and checks 2 and 3 must then be
// RE-INVOKED — the same functions called a second time, not a second copy of
// their assertions.
func TestVerifyInstallReinstallsAndRepeatsTheAccountAndOwnershipChecks(t *testing.T) {
	lines := effectiveLines(readVerifyInstall(t))
	joined := strings.Join(lines, "\n")

	require.Contains(t, joined, `apt-get install -y --reinstall "${artifact}"`,
		"%s must reinstall the same deb artifact through apt-get", verifyInstallPath)
	require.Contains(t, joined, `dnf reinstall -y "${artifact}"`,
		"%s must reinstall the same rpm artifact through dnf", verifyInstallPath)

	for _, check := range []string{"check_account", "check_owner_mode"} {
		invocations := 0

		for _, line := range lines {
			if line == check {
				invocations++
			}
		}

		require.Greaterf(t, invocations, 1,
			"%s must invoke %s more than once (once after install, once after the reinstall), got %d invocation(s)",
			verifyInstallPath, check, invocations)
	}
}

// TestVerifyInstallPackageNameMatchesGoreleaserConfig keeps the name the
// removal verbs operate on equal to the live configuration's package_name. A
// rename there would otherwise leave check 8 removing a package that does not
// exist.
func TestVerifyInstallPackageNameMatchesGoreleaserConfig(t *testing.T) {
	require.Containsf(t, effectiveLines(readVerifyInstall(t)), "package_name="+livePackageName(t),
		"%s must remove the package %s declares as package_name", verifyInstallPath, goreleaserConfigPath)
}

// overrideDestinations returns the live destinations of the given format's
// override that satisfy keep, rejecting a duplicate declaration.
func overrideDestinations(t *testing.T, format string, keep func(contentEntry) bool) []string {
	t.Helper()

	var destinations []string

	for _, entry := range loadOverride(t, loadNFPMEntry(t), format).Contents {
		if !keep(entry) {
			continue
		}

		require.NotContainsf(t, destinations, entry.Dst,
			"%s declares %s twice in the %s override", goreleaserConfigPath, entry.Dst, format)
		destinations = append(destinations, entry.Dst)
	}

	sort.Strings(destinations)

	return destinations
}

// TestVerifyInstallConffilesMatchGoreleaserConfig is the bidirectional drift
// guard for the conffile set. The paths the script treats as conffiles must be
// exactly the destinations the live ../.goreleaser.yaml marks `type: config`,
// in both directions and in both overrides: a stray expectation line fails it,
// and a newly marked config file with no expectation line fails it too.
func TestVerifyInstallConffilesMatchGoreleaserConfig(t *testing.T) {
	script := scriptPaths(t, expectConffileLine)
	require.NotEmpty(t, script, "%s must declare expect_conffile expectations", verifyInstallPath)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			packaged := overrideDestinations(t, format, func(entry contentEntry) bool {
				return entry.Type == "config"
			})

			require.Equalf(t, packaged, script,
				"the conffiles %s expects must be exactly the %s destinations %s marks `type: config`",
				verifyInstallPath, format, goreleaserConfigPath)
		})
	}
}

// TestVerifyInstallRemovedPathsMatchGoreleaserConfig is the bidirectional drift
// guard for the non-config set: exactly the live packaged destinations that are
// neither `type: config` nor `type: dir`, plus the two /usr/bin build outputs.
// Directories are excluded because whether an emptied packaged directory is
// pruned is deliberately not pinned, and config files because their fate
// depends on the removal verb.
func TestVerifyInstallRemovedPathsMatchGoreleaserConfig(t *testing.T) {
	script := scriptPaths(t, expectRemovedLine)
	require.NotEmpty(t, script, "%s must declare expect_removed expectations", verifyInstallPath)

	cgoEnabled, cgoDisabled := binaryDestinationsByCGO(t)
	binaries := append(slices.Clone(cgoEnabled), cgoDisabled...)
	require.NotEmptyf(t, binaries, "%s must declare builds", goreleaserConfigPath)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			packaged := overrideDestinations(t, format, func(entry contentEntry) bool {
				return entry.Type != "config" && entry.Type != "dir"
			})

			want := append(slices.Clone(packaged), binaries...)
			sort.Strings(want)

			require.Equalf(t, want, script,
				"the paths %s expects to be gone after removal must be exactly the non-config, non-dir %s destinations in %s plus the %s build outputs",
				verifyInstallPath, format, goreleaserConfigPath, usrBinDir)
		})
	}
}

// removalCell is one cell of the format x verb x expectation matrix resolution
// 9 specifies.
type removalCell struct {
	format string
	verb   string
	// want are the check invocations that must follow this verb.
	want []string
	// forbidden are the check invocations that must NOT follow it, so a cell
	// cannot pass by asserting the opposite of what it should.
	forbidden []string
	// why records the measured behaviour the cell encodes.
	why string
}

// removalMatrix enumerates every cell rather than spot-checking one. dpkg and
// rpm disagree about config files on removal, so each verb carries its own row.
var removalMatrix = []removalCell{
	{
		format:    "deb",
		verb:      `dpkg -r "${package_name}"`,
		want:      []string{`check_removed_paths_gone "dpkg -r"`, `check_conffiles_present "dpkg -r"`, `check_account_survives_removal "dpkg -r"`},
		forbidden: []string{`check_conffiles_gone "dpkg -r"`, `check_no_rpmsave "dpkg -r"`},
		why:       "a dpkg remove is not a purge: the conffiles survive it, everything else goes",
	},
	{
		format:    "deb",
		verb:      `dpkg -P "${package_name}"`,
		want:      []string{`check_conffiles_gone "dpkg -P"`, `check_account_survives_removal "dpkg -P"`},
		forbidden: []string{`check_conffiles_present "dpkg -P"`, `check_no_rpmsave "dpkg -P"`},
		why:       "a purge takes the conffiles the remove kept, and it runs on the removed-but-unpurged package so no reinstall is needed between the verbs",
	},
	{
		format:    "rpm",
		verb:      `rpm -e "${package_name}"`,
		want:      []string{`check_removed_paths_gone "rpm -e"`, `check_conffiles_gone "rpm -e"`, `check_no_rpmsave "rpm -e"`, `check_account_survives_removal "rpm -e"`},
		forbidden: []string{`check_conffiles_present "rpm -e"`},
		why:       "rpm erases an unmodified config file outright; a .rpmsave would mean the postinstall modified a config file the package shipped",
	},
}

// TestVerifyInstallRemovalMatrix is check 8's structural guard. It walks the
// full format x verb x expectation matrix: every verb appears exactly once, in
// its own format's branch, followed by exactly the assertions resolution 9
// specifies for it and by none of the assertions that would contradict them.
func TestVerifyInstallRemovalMatrix(t *testing.T) {
	section := removalSection(t)

	var verbOrder []string

	segments := make(map[string][]string)

	for index, line := range section {
		match := removalVerbLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		verb := match[1] + ` "${package_name}"`
		require.NotContainsf(t, verbOrder, verb, "%s runs %s more than once", verifyInstallPath, verb)
		verbOrder = append(verbOrder, verb)
		segments[verb] = section[index:]
	}

	// Every later verb's lines are a suffix of an earlier verb's, so each
	// segment is trimmed at the next verb.
	for index, verb := range verbOrder {
		if index+1 < len(verbOrder) {
			next := slices.IndexFunc(segments[verb][1:], func(line string) bool {
				return strings.HasPrefix(line, verbOrder[index+1])
			})
			require.GreaterOrEqualf(t, next, 0, "%s must run %s after %s", verifyInstallPath, verbOrder[index+1], verb)
			segments[verb] = segments[verb][:next+1]
		}
	}

	require.Lenf(t, verbOrder, len(removalMatrix), "%s must run exactly the %d removal verbs the matrix enumerates, got %v",
		verifyInstallPath, len(removalMatrix), verbOrder)

	debBranch := slices.Index(section, "deb)")
	rpmBranch := slices.Index(section, "rpm)")
	require.GreaterOrEqual(t, debBranch, 0, "%s must remove the deb in its own case branch", verifyInstallPath)
	require.Greaterf(t, rpmBranch, debBranch, "%s must remove the rpm in its own case branch", verifyInstallPath)

	for _, cell := range removalMatrix {
		t.Run(cell.format+"/"+strings.Fields(cell.verb)[1], func(t *testing.T) {
			segment, ok := segments[cell.verb]
			require.Truef(t, ok, "%s must run `%s` (%s)", verifyInstallPath, cell.verb, cell.why)

			verbIndex := slices.IndexFunc(section, func(line string) bool {
				return strings.HasPrefix(line, cell.verb)
			})

			switch cell.format {
			case "deb":
				require.Truef(t, verbIndex > debBranch && verbIndex < rpmBranch,
					"%s must run `%s` inside the deb branch of the removal case", verifyInstallPath, cell.verb)
			case "rpm":
				require.Greaterf(t, verbIndex, rpmBranch,
					"%s must run `%s` inside the rpm branch of the removal case", verifyInstallPath, cell.verb)
			}

			var asserted []string

			for _, line := range segment {
				if removalCheckLine.MatchString(line) {
					asserted = append(asserted, line)
				}
			}

			require.Equalf(t, cell.want, asserted,
				"after `%s`, %s must assert exactly %v and nothing else: %s",
				cell.verb, verifyInstallPath, cell.want, cell.why)

			for _, forbidden := range cell.forbidden {
				require.NotContainsf(t, segment, forbidden,
					"after `%s`, %s must not assert %s: %s", cell.verb, verifyInstallPath, forbidden, cell.why)
			}
		})
	}
}

// TestVerifyInstallRemovalChecksAssertTheRightPolarity pins what each check
// invoked by the matrix actually does, so a cell cannot be satisfied by a
// correctly named function whose body asserts the opposite.
func TestVerifyInstallRemovalChecksAssertTheRightPolarity(t *testing.T) {
	for _, tc := range []struct {
		function string
		want     string
	}{
		{function: "check_removed_paths_gone", want: `[ ! -e "${path}" ] ||`},
		{function: "check_conffiles_present", want: `[ -e "${path}" ] ||`},
		{function: "check_conffiles_gone", want: `[ ! -e "${path}" ] ||`},
		{function: "check_no_rpmsave", want: `[ -z "${saved}" ] ||`},
	} {
		t.Run(tc.function, func(t *testing.T) {
			require.Containsf(t, functionBody(t, tc.function), tc.want,
				"%s's %s must assert %q", verifyInstallPath, tc.function, tc.want)
		})
	}

	body := functionBody(t, "check_account_survives_removal")

	for _, want := range []string{
		`getent passwd "${removal_account}" >/dev/null ||`,
		`getent group "${removal_account}" >/dev/null ||`,
	} {
		require.Containsf(t, body, want,
			"%s must assert the account outlives removal (%q): systemd-sysusers created it and neither manager owns it",
			verifyInstallPath, want)
	}

	require.Containsf(t, functionBody(t, "check_no_rpmsave"), `saved=$(find ${rpmsave_search_dirs} -name '*.rpmsave' 2>/dev/null || true)`,
		"%s must sweep the packaged config directories for a stray .rpmsave", verifyInstallPath)
	require.Containsf(t, effectiveLines(readVerifyInstall(t)), `rpmsave_search_dirs='`+installedConfigDir+" "+installedPAMDir+`'`,
		"%s must sweep exactly %s and %s for a .rpmsave", verifyInstallPath, installedConfigDir, installedPAMDir)
}

// TestVerifyInstallNeverAssertsTheConfigDirectoryIsPruned is the negative
// guard for resolution 9's explicit non-goal. Whether an emptied
// /etc/pilothouse is removed varies between managers and versions, so the
// script must never assert its absence — only the absence of files INSIDE it.
func TestVerifyInstallNeverAssertsTheConfigDirectoryIsPruned(t *testing.T) {
	for _, line := range effectiveLines(readVerifyInstall(t)) {
		require.NotRegexpf(t, regexp.MustCompile(`\[ ! -[edf] "?`+regexp.QuoteMeta(installedConfigDir)+`"? \]`), line,
			"%s must never assert that %s itself is pruned: %s", verifyInstallPath, installedConfigDir, line)
	}

	for _, expectation := range scriptPaths(t, expectRemovedLine) {
		require.NotEqualf(t, installedConfigDir, expectation,
			"%s must not expect %s itself to be removed", verifyInstallPath, installedConfigDir)
	}

	for _, expectation := range scriptPaths(t, expectConffileLine) {
		require.NotEqualf(t, installedConfigDir, expectation,
			"%s must not treat %s itself as a conffile", verifyInstallPath, installedConfigDir)
	}
}
