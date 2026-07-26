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

// The booted-VM harness (Layer B, #67) lives outside this Go package, in
// test/vm. These are structural guards over it: they read the files as text and
// stat their modes, and they never execute any part of the harness. The only
// process any test in this file spawns is shellcheck, skip-if-absent, exactly as
// verify_install_test.go does for packaging/verify-install.sh.
const (
	vmHarnessDir = "../test/vm"

	// vmImagesEnvPath is the single pinning site for the guest cloud images:
	// per family, the immutable image URL, the checksum algorithm and the
	// distributor's published digest.
	vmImagesEnvPath = vmHarnessDir + "/images.env"

	// vmImagesLibPath is the host-side fetch/verify library that consumes it.
	vmImagesLibPath = vmHarnessDir + "/lib/images.sh"
)

// vmSourcedScripts are harness files that are sourced, never invoked as
// programs, and are therefore committed non-executable. Executed scripts land in
// a separate set as they are added, so the two categories cannot blur.
var vmSourcedScripts = []string{
	vmImagesEnvPath,
	vmImagesLibPath,
}

// vmDigestLengths is the hex length a digest must have under each algorithm the
// pinning table is allowed to declare.
var vmDigestLengths = map[string]int{
	"sha256": 64,
	"sha512": 128,
}

// vmImagePin is one family's row of test/vm/images.env.
type vmImagePin struct {
	URL       string
	Algorithm string
	Digest    string
}

var vmImagesEnvAssignment = regexp.MustCompile(`^VM_IMAGE_([A-Z0-9]+)_(URL|ALGORITHM|DIGEST)="([^"]*)"$`)

func readVMHarnessFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read %s", path)

	return string(raw)
}

// loadVMImagePins parses test/vm/images.env. The file is plain `NAME="value"`
// assignments precisely so this guard can read it without sourcing it — no
// guard test may execute any part of the harness.
func loadVMImagePins(t *testing.T) map[string]vmImagePin {
	t.Helper()

	pins := map[string]vmImagePin{}

	for _, line := range strings.Split(readVMHarnessFile(t, vmImagesEnvPath), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		match := vmImagesEnvAssignment.FindStringSubmatch(line)
		require.NotNilf(t, match, "%s: line %q is neither a comment nor a VM_IMAGE_<FAMILY>_<FIELD>=\"value\" assignment", vmImagesEnvPath, line)

		family := strings.ToLower(match[1])
		pin := pins[family]

		switch match[2] {
		case "URL":
			pin.URL = match[3]
		case "ALGORITHM":
			pin.Algorithm = match[3]
		case "DIGEST":
			pin.Digest = match[3]
		}

		pins[family] = pin
	}

	return pins
}

// TestVMImagesEnvPinsBothFamilies pins that every family the harness boots has a
// complete row: a URL, a checksum algorithm and a digest.
func TestVMImagesEnvPinsBothFamilies(t *testing.T) {
	pins := loadVMImagePins(t)

	for _, family := range []string{"debian", "fedora"} {
		pin, ok := pins[family]
		require.Truef(t, ok, "%s must pin an image for %s", vmImagesEnvPath, family)
		require.NotEmptyf(t, pin.URL, "%s: %s needs an image URL", vmImagesEnvPath, family)
		require.NotEmptyf(t, pin.Algorithm, "%s: %s needs a checksum algorithm", vmImagesEnvPath, family)
		require.NotEmptyf(t, pin.Digest, "%s: %s needs a digest", vmImagesEnvPath, family)
	}

	require.Equal(t, "sha512", pins["debian"].Algorithm,
		"Debian publishes SHA512SUMS beside its cloud images")
	require.Equal(t, "sha256", pins["fedora"].Algorithm,
		"Fedora publishes SHA-256 in Fedora-Cloud-42-1.1-x86_64-CHECKSUM")
}

// TestVMImagesEnvDebianReference pins the exact dated Debian 12 genericcloud
// artifact, and the two ends of the digest the spec quotes. Re-deriving the
// digest here would defeat the pin, so only its shape and its quoted ends are
// asserted: if the image ever moves, the values are re-derived from that
// directory's SHA512SUMS, not recomputed locally.
func TestVMImagesEnvDebianReference(t *testing.T) {
	pin := loadVMImagePins(t)["debian"]

	require.Equal(t,
		"https://cloud.debian.org/images/cloud/bookworm/20260722-2547/debian-12-genericcloud-amd64-20260722-2547.qcow2",
		pin.URL)
	require.True(t, strings.HasPrefix(pin.Digest, "ddc98e22b1c0e664"),
		"Debian SHA-512 must begin ddc98e22b1c0e664, got %q", pin.Digest)
	require.True(t, strings.HasSuffix(pin.Digest, "907b97b0"),
		"Debian SHA-512 must end 907b97b0, got %q", pin.Digest)
}

// TestVMImagesEnvFedoraReference pins the archived Fedora 42 Cloud-Base-Generic
// artifact literally. The non-archive releases/42/... path 404s; it must not be
// "fixed" back.
func TestVMImagesEnvFedoraReference(t *testing.T) {
	pin := loadVMImagePins(t)["fedora"]

	require.Equal(t,
		"https://dl.fedoraproject.org/pub/archive/fedora/linux/releases/42/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2",
		pin.URL)
	require.Equal(t, "e401a4db2e5e04d1967b6729774faa96da629bcf3ba90b67d8d9cce9906bec0f", pin.Digest)
}

// TestVMImagesEnvReferencesAreImmutableAndVerifiable enforces the properties
// that make the pin worth anything: HTTPS transport, no `latest`-style path
// segment (which would move under the digest), and a digest whose length
// matches the algorithm it is declared under.
func TestVMImagesEnvReferencesAreImmutableAndVerifiable(t *testing.T) {
	hex := regexp.MustCompile(`^[0-9a-f]+$`)

	for family, pin := range loadVMImagePins(t) {
		require.Truef(t, strings.HasPrefix(pin.URL, "https://"),
			"%s: %s image URL must be https, got %q", vmImagesEnvPath, family, pin.URL)

		for _, segment := range strings.Split(pin.URL, "/") {
			require.NotEqualf(t, "latest", segment,
				"%s: %s image URL must use an immutable path, not a `latest` segment: %q",
				vmImagesEnvPath, family, pin.URL)
		}

		want, ok := vmDigestLengths[pin.Algorithm]
		require.Truef(t, ok, "%s: %s declares unsupported algorithm %q", vmImagesEnvPath, family, pin.Algorithm)
		require.Truef(t, hex.MatchString(pin.Digest),
			"%s: %s digest must be lowercase hex, got %q", vmImagesEnvPath, family, pin.Digest)
		require.Lenf(t, pin.Digest, want,
			"%s: %s declares %s, whose digest is %d hex characters, got %d",
			vmImagesEnvPath, family, pin.Algorithm, want, len(pin.Digest))
	}
}

// TestVMSourcedScriptsAreNotExecutable pins the sourced half of the
// executed-versus-sourced discipline: a library that is sourced is never invoked
// as a program, so it is committed non-executable. This is the same instrument
// verify_install_test.go uses in the opposite direction for the executed
// install-validation script.
func TestVMSourcedScriptsAreNotExecutable(t *testing.T) {
	for _, path := range vmSourcedScripts {
		t.Run(filepath.Base(path), func(t *testing.T) {
			info, err := os.Stat(path)
			require.NoErrorf(t, err, "stat %s", path)
			require.Zerof(t, info.Mode().Perm()&0o111,
				"%s is sourced, never executed, and must be committed non-executable; mode is %v", path, info.Mode())
		})
	}
}

// TestVMImagesLibIsASourcedBashLibrary pins the dialect and the fail-fast
// opener. Host-side harness code runs on ubuntu-latest only, so it is bash with
// `set -euo pipefail`; a sourced library carries no shebang, because it is never
// invoked as a program.
func TestVMImagesLibIsASourcedBashLibrary(t *testing.T) {
	script := readVMHarnessFile(t, vmImagesLibPath)

	require.False(t, strings.HasPrefix(script, "#!"),
		"%s is sourced and must not carry a shebang", vmImagesLibPath)
	require.Equal(t, "set -euo pipefail", effectiveLines(script)[0],
		"the first effective line of %s must be `set -euo pipefail`", vmImagesLibPath)
}

// TestVMImagesLibVerifiesAgainstThePin pins fetch_image's contract: it dispatches
// to the per-family algorithm, re-verifies a cached copy before reusing it, and
// on mismatch fails naming both the expected and the actual digest. It also pins
// that no URL or digest is hardcoded here — images.env is the sole pinning site.
func TestVMImagesLibVerifiesAgainstThePin(t *testing.T) {
	script := readVMHarnessFile(t, vmImagesLibPath)

	require.Contains(t, script, "fetch_image()", "%s must define fetch_image", vmImagesLibPath)

	for _, tool := range []string{"sha256sum", "sha512sum"} {
		require.Containsf(t, script, tool,
			"%s must dispatch to %s so each family is checked under its own algorithm", vmImagesLibPath, tool)
	}

	require.Contains(t, script, "expected $expected, actual $actual",
		"%s must name both the expected and the actual digest on mismatch", vmImagesLibPath)
	require.Contains(t, script, "re-verifying before use",
		"%s must re-verify a cached image before reusing it", vmImagesLibPath)
	require.Regexp(t, regexp.MustCompile(`\bverify_image "\$target"`),
		script, "%s must run the cached copy through verify_image", vmImagesLibPath)
	require.Regexp(t, regexp.MustCompile(`\bverify_image "\$partial"`),
		script, "%s must verify the freshly downloaded file before it is kept", vmImagesLibPath)

	require.NotContains(t, script, "https://",
		"%s must carry no image URL: %s is the sole pinning site", vmImagesLibPath, vmImagesEnvPath)
}

// TestVMHarnessShellcheck runs the real shellcheck in bash mode over every
// harness file it applies to. There is no hand-written substitute: when
// shellcheck is absent the test skips with a logged reason, and
// .docker/Dockerfile installs it so `make docker-ci` runs this check for real.
func TestVMHarnessShellcheck(t *testing.T) {
	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skipf("skipping: shellcheck is not on PATH (%v); `.docker/Dockerfile` installs it so this check runs under `make docker-ci`", err)
	}

	t.Logf("using shellcheck at %s", shellcheck)

	for _, path := range []string{vmImagesLibPath} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out, err := exec.Command(shellcheck, "--shell=bash", path).CombinedOutput()
			require.NoErrorf(t, err, "shellcheck --shell=bash %s reported problems:\n%s", path, out)
			require.Emptyf(t, strings.TrimSpace(string(out)),
				"shellcheck --shell=bash %s must emit no warnings, got:\n%s", path, out)
		})
	}
}
