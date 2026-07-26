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

	// vmLibDir holds every sourced host-side library.
	vmLibDir = vmHarnessDir + "/lib"

	// vmCloudInitLibPath generates the run-time credentials and the NoCloud
	// seed; vmVMLibPath boots the guest and owns the serial-console channel;
	// vmSSHLibPath owns the guest's SSH lifecycle.
	vmCloudInitLibPath = vmLibDir + "/cloudinit.sh"
	vmVMLibPath        = vmLibDir + "/vm.sh"
	vmSSHLibPath       = vmLibDir + "/ssh.sh"
)

// vmSourcedScripts are harness files that are sourced, never invoked as
// programs, and are therefore committed non-executable. Executed scripts land in
// a separate set as they are added, so the two categories cannot blur.
var vmSourcedScripts = []string{
	vmImagesEnvPath,
	vmImagesLibPath,
	vmCloudInitLibPath,
	vmVMLibPath,
	vmSSHLibPath,
}

// vmSourcedBashLibraries are the sourced libraries that are bash, so they carry
// no shebang and open with the fail-fast options.
var vmSourcedBashLibraries = []string{
	vmImagesLibPath,
	vmCloudInitLibPath,
	vmVMLibPath,
	vmSSHLibPath,
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

// TestVMSourcedLibrariesAreBashLibraries pins the dialect and the fail-fast
// opener for every sourced host-side library. Host-side harness code runs on
// ubuntu-latest only, so it is bash with `set -euo pipefail`; a sourced library
// carries no shebang, because it is never invoked as a program.
func TestVMSourcedLibrariesAreBashLibraries(t *testing.T) {
	for _, path := range vmSourcedBashLibraries {
		t.Run(filepath.Base(path), func(t *testing.T) {
			script := readVMHarnessFile(t, path)

			require.False(t, strings.HasPrefix(script, "#!"),
				"%s is sourced and must not carry a shebang", path)
			require.Equal(t, "set -euo pipefail", effectiveLines(script)[0],
				"the first effective line of %s must be `set -euo pipefail`", path)
		})
	}
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

	for _, path := range vmSourcedBashLibraries {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out, err := exec.Command(shellcheck, "--shell=bash", path).CombinedOutput()
			require.NoErrorf(t, err, "shellcheck --shell=bash %s reported problems:\n%s", path, out)
			require.Emptyf(t, strings.TrimSpace(string(out)),
				"shellcheck --shell=bash %s must emit no warnings, got:\n%s", path, out)
		})
	}
}

// vmHarnessFiles lists every regular file under test/vm, so a guard that must
// hold for "any added file" cannot be evaded by adding one the tables here do
// not name.
func vmHarnessFiles(t *testing.T) []string {
	t.Helper()

	var files []string
	require.NoError(t, filepath.WalkDir(vmHarnessDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if info, statErr := entry.Info(); statErr != nil {
			return statErr
		} else if !info.Mode().IsRegular() {
			return nil
		}

		files = append(files, path)

		return nil
	}))
	require.NotEmpty(t, files, "no files found under %s", vmHarnessDir)

	return files
}

// shellFunctionBody returns the text of a shell function defined as
// `name() {` ... `\n}`. It is a text extraction, not an interpreter: guard
// tests never execute any part of the harness.
func shellFunctionBody(t *testing.T, path, script, name string) string {
	t.Helper()

	opener := "\n" + name + "() {\n"
	start := strings.Index(script, opener)
	require.GreaterOrEqualf(t, start, 0, "%s must define %s()", path, name)

	rest := script[start+len(opener):]
	end := strings.Index(rest, "\n}\n")
	require.GreaterOrEqualf(t, end, 0, "%s: %s() is not terminated by a closing brace at column zero", path, name)

	return rest[:end]
}

// TestVMLibIsEntirelySourced pins the sourced half of the executed-versus-sourced
// discipline for the whole library directory rather than for a hand-kept list:
// every file under test/vm/lib is sourced, never invoked as a program, so none
// of them may carry any executable bit.
func TestVMLibIsEntirelySourced(t *testing.T) {
	entries, err := os.ReadDir(vmLibDir)
	require.NoErrorf(t, err, "read %s", vmLibDir)
	require.NotEmpty(t, entries, "%s must not be empty", vmLibDir)

	for _, entry := range entries {
		info, err := entry.Info()
		require.NoErrorf(t, err, "stat %s/%s", vmLibDir, entry.Name())
		require.Zerof(t, info.Mode().Perm()&0o111,
			"%s/%s is a sourced library and must be committed non-executable; mode is %v",
			vmLibDir, entry.Name(), info.Mode())
	}
}

// TestVMCloudInitGeneratesCredentialsOnTheHost pins where credentials come
// from: this library, on the host, at run time, into a 0700 per-run workspace,
// with creds.env written beside the keypair. Nothing is generated in the guest,
// and nothing is generated anywhere but the workspace.
func TestVMCloudInitGeneratesCredentialsOnTheHost(t *testing.T) {
	script := readVMHarnessFile(t, vmCloudInitLibPath)

	workspace := shellFunctionBody(t, vmCloudInitLibPath, script, "create_run_workspace")
	require.Contains(t, workspace, `chmod 0700 "$1"`,
		"%s: the run workspace must be created with mode 0700", vmCloudInitLibPath)

	generate := shellFunctionBody(t, vmCloudInitLibPath, script, "generate_credentials")
	require.Contains(t, generate, `VM_SSH_KEY="$workspace/id_ed25519"`,
		"%s: the keypair must be generated inside the run workspace", vmCloudInitLibPath)
	require.Contains(t, generate, `VM_CREDS_ENV="$workspace/creds.env"`,
		"%s: creds.env must be written inside the run workspace", vmCloudInitLibPath)
	require.Contains(t, generate, "ssh-keygen -q -t ed25519 -N '' -C 'pilothouse-vm-boot' -f \"$VM_SSH_KEY\"",
		"%s: the harness identity is a run-time ed25519 keypair with no passphrase", vmCloudInitLibPath)

	for _, name := range []string{"PH_ADMIN_USER", "PH_ADMIN_PASSWORD", "PH_ROOT_PASSWORD"} {
		require.Containsf(t, generate, name+"=$",
			"%s: creds.env must carry %s, assigned from a generated value", vmCloudInitLibPath, name)
	}

	password := shellFunctionBody(t, vmCloudInitLibPath, script, "generate_password")
	require.Contains(t, password, "openssl rand -base64",
		"%s: passwords come from openssl rand", vmCloudInitLibPath)
	require.Contains(t, password, "</dev/urandom",
		"%s: passwords fall back to /dev/urandom, never to a literal", vmCloudInitLibPath)

	// Everything that touches a credential disables xtrace first, so no
	// generated value can reach the job log through shell tracing.
	for _, name := range []string{"generate_password", "generate_credentials", "write_cloud_init_seed"} {
		body := shellFunctionBody(t, vmCloudInitLibPath, script, name)
		require.Containsf(t, body, "set +x",
			"%s: %s handles credentials and must run with shell tracing disabled", vmCloudInitLibPath, name)
	}
}

// TestVMCloudInitSeedExportsItsWorkspacePaths pins how create_seed returns.
// generate_credentials and create_seed export the paths every later stage needs
// — the private key, creds.env and the seed image — and an export made inside a
// command substitution is confined to that subshell. So create_seed must be
// invoked directly and must publish the seed image through an exported variable
// rather than through standard output, or the caller would be left holding a
// path to a guest it has no key for.
func TestVMCloudInitSeedExportsItsWorkspacePaths(t *testing.T) {
	script := readVMHarnessFile(t, vmCloudInitLibPath)

	seed := shellFunctionBody(t, vmCloudInitLibPath, script, "create_seed")
	require.Contains(t, seed, `VM_SEED_ISO="$(build_seed_iso "$2")"`,
		"%s: create_seed must capture the packed seed image", vmCloudInitLibPath)
	require.Contains(t, seed, "export VM_SEED_ISO",
		"%s: create_seed must publish the seed image through an exported variable, because its own standard output cannot be captured without discarding the credential exports it also makes",
		vmCloudInitLibPath)

	generate := shellFunctionBody(t, vmCloudInitLibPath, script, "generate_credentials")
	require.Contains(t, generate, "export VM_SSH_KEY VM_CREDS_ENV",
		"%s: generate_credentials must export the credential paths ssh.sh reads", vmCloudInitLibPath)
}

// TestVMCloudInitSeedDeclaresOneAdministratorPerFamily pins the seed's account
// shape: one login account, the family's administrator group, the generated
// public key, and the NOPASSWD grant that makes non-interactive escalation
// possible at all.
func TestVMCloudInitSeedDeclaresOneAdministratorPerFamily(t *testing.T) {
	script := readVMHarnessFile(t, vmCloudInitLibPath)

	group := shellFunctionBody(t, vmCloudInitLibPath, script, "admin_group_for_family")
	require.Regexp(t, regexp.MustCompile(`debian\)\s*printf '%s\\n' 'sudo'`), group,
		"%s: the Debian branch must select the `sudo` administrator group", vmCloudInitLibPath)
	require.Regexp(t, regexp.MustCompile(`fedora\)\s*printf '%s\\n' 'wheel'`), group,
		"%s: the Fedora branch must select the `wheel` administrator group", vmCloudInitLibPath)

	seed := shellFunctionBody(t, vmCloudInitLibPath, script, "write_cloud_init_seed")
	require.Contains(t, seed, `group="$(admin_group_for_family "$family")"`,
		"%s: the seed must take the administrator group from the family argument", vmCloudInitLibPath)
	require.Contains(t, seed, "groups: [$group]",
		"%s: the account's groups must come from the family branch", vmCloudInitLibPath)
	require.Contains(t, seed, "- name: $PH_ADMIN_USER",
		"%s: the seed declares the administrator account read from creds.env", vmCloudInitLibPath)
	require.Contains(t, seed, "ssh_authorized_keys:\n      - $pubkey",
		"%s: the account's only key is the one generated in the workspace", vmCloudInitLibPath)

	require.Equal(t, 1, strings.Count(seed, "ssh_authorized_keys:"),
		"%s: the seed must declare exactly one set of authorized keys — one login identity", vmCloudInitLibPath)
	require.Equal(t, 1, strings.Count(seed, "\nusers:\n"),
		"%s: the seed must declare exactly one login account", vmCloudInitLibPath)
}

// TestVMCloudInitSeedSetsBothPasswordsThroughChpasswd pins the delivery
// mechanism for both passwords. root's password must be set and valid: the PAM
// checks that come later are only non-vacuous if root could otherwise have
// logged in.
func TestVMCloudInitSeedSetsBothPasswordsThroughChpasswd(t *testing.T) {
	seed := shellFunctionBody(t, vmCloudInitLibPath, readVMHarnessFile(t, vmCloudInitLibPath), "write_cloud_init_seed")

	require.Contains(t, seed, "chpasswd:\n  expire: false\n  users:",
		"%s: both passwords are delivered through cloud-init's chpasswd module with expire: false", vmCloudInitLibPath)
	require.Contains(t, seed, "- name: $PH_ADMIN_USER\n      password: $PH_ADMIN_PASSWORD\n      type: text",
		"%s: the administrator's generated password must be set", vmCloudInitLibPath)
	require.Contains(t, seed, "- name: root\n      password: $PH_ROOT_PASSWORD\n      type: text",
		"%s: root's generated password must be set", vmCloudInitLibPath)
}

// TestVMCloudInitSeedGivesRootNoSSHAccess pins the negative half of the single
// login identity: root gets no authorized key and SSH root login is not
// enabled. Stock cloud images restrict it, and widening that would both make
// the guest less stock and widen the credential surface.
func TestVMCloudInitSeedGivesRootNoSSHAccess(t *testing.T) {
	script := readVMHarnessFile(t, vmCloudInitLibPath)
	seed := shellFunctionBody(t, vmCloudInitLibPath, script, "write_cloud_init_seed")

	require.Contains(t, seed, "disable_root: true",
		"%s: the seed must leave root's SSH login disabled", vmCloudInitLibPath)
	require.Contains(t, seed, "ssh_pwauth: false",
		"%s: the seed must not enable password authentication over SSH", vmCloudInitLibPath)

	// The account block is the only place a login identity can be declared;
	// chpasswd sets root's password but grants it no way in.
	accounts := seed[strings.Index(seed, "\nusers:\n"):]
	accounts = accounts[:strings.Index(accounts, "\nchpasswd:")]
	require.NotContains(t, accounts, "root",
		"%s: root must not be declared as a login account in users:", vmCloudInitLibPath)

	for _, path := range vmHarnessFiles(t) {
		content := readVMHarnessFile(t, path)

		require.NotContainsf(t, content, "PermitRootLogin",
			"%s must not touch PermitRootLogin: SSH root login stays as the stock image left it", path)
		require.NotContainsf(t, content, "root_ssh_authorized_keys",
			"%s must install no authorized key for root", path)
		require.NotContainsf(t, content, "root@",
			"%s must never address the guest as root: the one login identity is the administrator account", path)
	}
}

// TestVMSeedWritesTheConsoleDiagnosticsChannel pins the layer of the serial
// console that does not depend on the guest kernel command line: journald
// forwards to /dev/ttyS0 and cloud-init tees its own output to the console.
// Without these, a guest that dies after early boot leaves nothing behind.
func TestVMSeedWritesTheConsoleDiagnosticsChannel(t *testing.T) {
	seed := shellFunctionBody(t, vmCloudInitLibPath, readVMHarnessFile(t, vmCloudInitLibPath), "write_cloud_init_seed")

	require.Contains(t, seed, "path: /etc/systemd/journald.conf.d/99-console.conf",
		"%s: the seed must write a journald drop-in", vmCloudInitLibPath)
	require.Contains(t, seed, "ForwardToConsole=yes",
		"%s: journald must forward to the console", vmCloudInitLibPath)
	require.Contains(t, seed, "TTYPath=/dev/ttyS0",
		"%s: journald's console must be the serial port QEMU is logging", vmCloudInitLibPath)
	require.Contains(t, seed, "output: {all: '| tee -a /dev/console'}",
		"%s: cloud-init's own output must reach the console too", vmCloudInitLibPath)
}

// TestVMBootsAnOverlayOverThePinnedBase pins that the pinned base image is
// read-only input: the guest boots a qcow2 overlay created in the run
// workspace, and nothing rewrites or customises the base.
func TestVMBootsAnOverlayOverThePinnedBase(t *testing.T) {
	script := readVMHarnessFile(t, vmVMLibPath)

	overlay := shellFunctionBody(t, vmVMLibPath, script, "create_overlay")
	require.Contains(t, overlay, `qemu-img create -q -f qcow2 -F qcow2 -b "$absolute_base" "$overlay"`,
		"%s: the guest disk must be a qcow2 overlay backed by the pinned base", vmVMLibPath)
	require.Contains(t, overlay, `overlay="$workspace/disk.qcow2"`,
		"%s: the overlay must live in the run workspace", vmVMLibPath)

	for _, path := range vmHarnessFiles(t) {
		content := readVMHarnessFile(t, path)

		for _, forbidden := range []string{"virt-customize", "guestfish", "guestmount", "libguestfs", "virt-sysprep"} {
			require.NotContainsf(t, content, forbidden,
				"%s must not use %s: the guest stays stock and no OS image is derived here", path, forbidden)
		}

		require.NotContainsf(t, content, "qemu-img rebase",
			"%s must not rewrite the base image", path)
	}
}

// TestVMStartsQEMUWithConsoleCaptureAndKVM pins the QEMU wiring: no display, a
// file chardev bound to -serial, QEMU's own stderr to a second file, both paths
// exported, and hardware acceleration left on.
func TestVMStartsQEMUWithConsoleCaptureAndKVM(t *testing.T) {
	script := readVMHarnessFile(t, vmVMLibPath)
	start := shellFunctionBody(t, vmVMLibPath, script, "start_vm")

	require.Contains(t, start, `QEMU_CONSOLE_LOG="$workspace/console.log"`,
		"%s: the serial console log lives in the run workspace", vmVMLibPath)
	require.Contains(t, start, `QEMU_STDERR_LOG="$workspace/qemu-stderr.log"`,
		"%s: QEMU's stderr is captured to the run workspace", vmVMLibPath)
	require.Contains(t, start, "export QEMU_CONSOLE_LOG QEMU_STDERR_LOG",
		"%s: both diagnostic paths must be exported for later stages", vmVMLibPath)
	require.Contains(t, start, "-display none",
		"%s: the guest is headless", vmVMLibPath)
	require.Contains(t, start, `-chardev "file,id=console,path=$QEMU_CONSOLE_LOG"`,
		"%s: the console must be a file chardev", vmVMLibPath)
	require.Contains(t, start, "-serial chardev:console",
		"%s: the file chardev must be bound to -serial", vmVMLibPath)
	require.Contains(t, start, `2>"$QEMU_STDERR_LOG"`,
		"%s: QEMU's stderr must be redirected to QEMU_STDERR_LOG", vmVMLibPath)
	require.Contains(t, start, "-accel kvm",
		"%s: KVM acceleration is required", vmVMLibPath)
	require.Contains(t, start, "hostfwd=tcp:$VM_SSH_HOST:$VM_SSH_PORT-:22",
		"%s: user-mode networking must forward a host port to the guest's sshd", vmVMLibPath)

	for _, path := range vmHarnessFiles(t) {
		content := readVMHarnessFile(t, path)

		for _, forbidden := range []string{"accel=tcg", "-accel tcg", "--no-kvm", "-machine accel=tcg"} {
			require.NotContainsf(t, content, forbidden,
				"%s must not disable KVM acceleration (%s)", path, forbidden)
		}
	}
}

// TestVMWaitForConsoleBootIsAFunctionalGate pins the layer that makes the
// console a gate: a bounded poll of the console log which, on expiry, names the
// assertion and dumps both host-side logs. A run whose serial log receives no
// output cannot pass.
func TestVMWaitForConsoleBootIsAFunctionalGate(t *testing.T) {
	script := readVMHarnessFile(t, vmVMLibPath)

	require.Contains(t, script, `CONSOLE_BOOT_TIMEOUT="${CONSOLE_BOOT_TIMEOUT:-300}"`,
		"%s must state the console boot timeout as an explicit bounded constant", vmVMLibPath)

	wait := shellFunctionBody(t, vmVMLibPath, script, "wait_for_console_boot")
	require.Contains(t, wait, `log="${QEMU_CONSOLE_LOG:-}"`,
		"%s: wait_for_console_boot must poll the exported console log", vmVMLibPath)
	require.Contains(t, wait, `grep -Eq "$CONSOLE_BOOT_MARKER" "$log"`,
		"%s: wait_for_console_boot must look for a boot marker", vmVMLibPath)
	require.Contains(t, wait, `[ "$waited" -lt "$CONSOLE_BOOT_TIMEOUT" ]`,
		"%s: the poll must be bounded by CONSOLE_BOOT_TIMEOUT", vmVMLibPath)
	require.Contains(t, wait, "assertion failed: no boot output matching",
		"%s: expiry must name the failing assertion", vmVMLibPath)
	require.Equal(t, 2, strings.Count(wait, "dump_boot_diagnostics"),
		"%s: both failure paths of wait_for_console_boot must dump the console log and QEMU's stderr", vmVMLibPath)

	dump := shellFunctionBody(t, vmVMLibPath, script, "dump_boot_diagnostics")
	require.Contains(t, dump, `"${QEMU_CONSOLE_LOG:-}" "${QEMU_STDERR_LOG:-}"`,
		"%s: the host-side dump must cover the console log and QEMU's stderr", vmVMLibPath)

	// The gate is only a gate if the boot path runs it, and it is only useful
	// before the SSH wait: a guest that never comes up makes the SSH timeout
	// the symptom and the console log the evidence.
	boot := shellFunctionBody(t, vmVMLibPath, script, "boot_guest")
	console := strings.Index(boot, "wait_for_console_boot")
	ssh := strings.Index(boot, "wait_for_ssh")
	require.GreaterOrEqual(t, console, 0, "%s: boot_guest must call wait_for_console_boot", vmVMLibPath)
	require.GreaterOrEqual(t, ssh, 0, "%s: boot_guest must call wait_for_ssh", vmVMLibPath)
	require.Less(t, console, ssh,
		"%s: boot_guest must gate on console output before waiting for ssh", vmVMLibPath)
}

// TestVMSSHUsesTheAdministratorIdentity pins that every guest connection is
// made as the administrator account read from creds.env, and that privilege is
// obtained by escalation rather than by a second identity.
func TestVMSSHUsesTheAdministratorIdentity(t *testing.T) {
	script := readVMHarnessFile(t, vmSSHLibPath)

	admin := shellFunctionBody(t, vmSSHLibPath, script, "guest_admin_user")
	require.Contains(t, admin, `creds="${VM_CREDS_ENV:-${VM_WORKSPACE:-}/creds.env}"`,
		"%s: the login account name must be read from the generated creds.env", vmSSHLibPath)
	require.Contains(t, admin, `printf '%s\n' "${PH_ADMIN_USER:-}"`,
		"%s: guest_admin_user must yield PH_ADMIN_USER", vmSSHLibPath)
	require.Contains(t, admin, "set +x",
		"%s: creds.env is sourced with shell tracing disabled", vmSSHLibPath)

	target := shellFunctionBody(t, vmSSHLibPath, script, "guest_target")
	require.Contains(t, target, `printf '%s@%s\n' "$(guest_admin_user)" "$VM_SSH_HOST"`,
		"%s: the guest destination must be built from the administrator account", vmSSHLibPath)

	for _, name := range []string{"guest_run", "guest_copy"} {
		body := shellFunctionBody(t, vmSSHLibPath, script, name)
		require.Containsf(t, body, `target="$(guest_target)"`,
			"%s: %s must address the guest as the administrator account, resolving the destination into a variable so a failure there stops the run",
			vmSSHLibPath, name)
	}

	sudo := shellFunctionBody(t, vmSSHLibPath, script, "guest_sudo")
	require.Contains(t, sudo, `guest_run sudo -n "$@"`,
		"%s: guest_sudo must escalate with `sudo -n`", vmSSHLibPath)
}

// TestVMSudoGrantIsLoadBearing enforces the NOPASSWD grant in both directions:
// the seed must grant it, and no guest-bound command anywhere in the library
// directory may use a password-prompting escalation form. With no TTY, a
// prompting escalation hangs; `sudo -n` fails immediately and legibly instead.
//
// The scan covers comments as well as code: a prompting form written in prose
// is a template for the next edit that reintroduces it.
func TestVMSudoGrantIsLoadBearing(t *testing.T) {
	seed := shellFunctionBody(t, vmCloudInitLibPath, readVMHarnessFile(t, vmCloudInitLibPath), "write_cloud_init_seed")
	require.Contains(t, seed, "sudo: ['ALL=(ALL) NOPASSWD:ALL']",
		"%s: the administrator account's NOPASSWD grant is load-bearing, not a convenience", vmCloudInitLibPath)

	invocation := regexp.MustCompile(`\bsudo\b[ \t]+(\S+)`)

	entries, err := os.ReadDir(vmLibDir)
	require.NoErrorf(t, err, "read %s", vmLibDir)

	for _, entry := range entries {
		path := filepath.Join(vmLibDir, entry.Name())

		for _, match := range invocation.FindAllStringSubmatch(readVMHarnessFile(t, path), -1) {
			require.Equalf(t, "-n", match[1],
				"%s invokes `%s`: every guest-side escalation must pass -n, because a non-interactive command with no TTY would hang on a password prompt",
				path, match[0])
		}
	}
}

// TestVMSSHForwardEndpointAgrees pins the one value the two libraries share:
// ssh.sh dials the endpoint vm.sh forwards. Each declares it with the same
// `:-` default so neither depends on the other being sourced first — which is
// only safe as long as the two declarations stay identical, so this guard
// requires exactly that.
func TestVMSSHForwardEndpointAgrees(t *testing.T) {
	vmScript := readVMHarnessFile(t, vmVMLibPath)
	sshScript := readVMHarnessFile(t, vmSSHLibPath)

	for _, declaration := range []string{
		`VM_SSH_HOST="${VM_SSH_HOST:-127.0.0.1}"`,
		`VM_SSH_PORT="${VM_SSH_PORT:-2222}"`,
	} {
		require.Containsf(t, vmScript, declaration,
			"%s must declare the forwarded endpoint as `%s`", vmVMLibPath, declaration)
		require.Containsf(t, sshScript, declaration,
			"%s must declare the dialled endpoint identically (`%s`), so sourcing either library first is safe and the two cannot drift",
			vmSSHLibPath, declaration)
	}
}

// TestVMSSHWaitsAreBoundedAndOrdered pins the SSH lifecycle: an explicit
// bounded readiness timeout, and a reboot that waits for the pre-reboot sshd to
// go away before waiting for sshd to return — otherwise the readiness check
// would be satisfied by the sshd that is about to die.
func TestVMSSHWaitsAreBoundedAndOrdered(t *testing.T) {
	script := readVMHarnessFile(t, vmSSHLibPath)

	require.Contains(t, script, `SSH_READY_TIMEOUT="${SSH_READY_TIMEOUT:-300}"`,
		"%s must state the SSH readiness timeout as an explicit bounded constant", vmSSHLibPath)
	require.Contains(t, script, `SSH_GONE_TIMEOUT="${SSH_GONE_TIMEOUT:-120}"`,
		"%s must state the pre-reboot disappearance timeout as an explicit bounded constant", vmSSHLibPath)

	ready := shellFunctionBody(t, vmSSHLibPath, script, "wait_for_ssh")
	require.Contains(t, ready, `[ "$waited" -lt "$SSH_READY_TIMEOUT" ]`,
		"%s: wait_for_ssh must be bounded by SSH_READY_TIMEOUT", vmSSHLibPath)
	require.Contains(t, ready, "assertion failed: the guest did not answer ssh within",
		"%s: wait_for_ssh must name the failing assertion on expiry", vmSSHLibPath)

	gone := shellFunctionBody(t, vmSSHLibPath, script, "wait_for_ssh_gone")
	require.Contains(t, gone, `[ "$waited" -lt "$SSH_GONE_TIMEOUT" ]`,
		"%s: wait_for_ssh_gone must be bounded by SSH_GONE_TIMEOUT", vmSSHLibPath)
	require.Contains(t, gone, "assertion failed: the pre-reboot sshd was still answering",
		"%s: wait_for_ssh_gone must name the failing assertion on expiry", vmSSHLibPath)

	reboot := shellFunctionBody(t, vmSSHLibPath, script, "reboot_guest")
	require.Contains(t, reboot, "guest_sudo systemctl reboot",
		"%s: reboot_guest must issue the reboot through the escalation wrapper, which passes -n", vmSSHLibPath)

	away := strings.Index(reboot, "wait_for_ssh_gone")
	back := strings.Index(reboot, "\n    wait_for_ssh\n")
	require.GreaterOrEqual(t, away, 0, "%s: reboot_guest must wait for the pre-reboot sshd to go away", vmSSHLibPath)
	require.GreaterOrEqual(t, back, 0, "%s: reboot_guest must wait for sshd to return", vmSSHLibPath)
	require.Less(t, away, back,
		"%s: reboot_guest must wait for the pre-reboot sshd to go away BEFORE waiting for sshd to return", vmSSHLibPath)
}

// vmCredentialAssignment matches an assignment whose name ends in `password` or
// `passwd`, in shell or YAML form, capturing the value.
var vmCredentialAssignment = regexp.MustCompile(`(?i)([A-Za-z_]*(?:password|passwd))[ \t]*[:=][ \t]*(\S*)`)

// vmCredentialKeyExemptions are keys whose value is not a credential:
// `lock_passwd` is a boolean, `chpasswd` opens a YAML block, and `NOPASSWD` is
// the sudoers tag whose presence a separate guard requires.
var vmCredentialKeyExemptions = map[string]bool{
	"lock_passwd": true,
	"chpasswd":    true,
	"nopasswd":    true,
}

// TestVMHarnessContainsNoCredentialLiteral is the fence that makes "generated
// at run time in the job workspace" enforceable: no key material and no
// password value may appear anywhere under test/vm. Every password-shaped
// assignment must take its value from a variable, never from a literal.
func TestVMHarnessContainsNoCredentialLiteral(t *testing.T) {
	for _, path := range vmHarnessFiles(t) {
		t.Run(strings.TrimPrefix(path, vmHarnessDir+"/"), func(t *testing.T) {
			content := readVMHarnessFile(t, path)

			for _, blob := range []string{"ssh-ed25519", "ssh-rsa", "ssh-dss", "ecdsa-sha2-"} {
				require.NotContainsf(t, content, blob,
					"%s must contain no public key blob (%s): the harness identity is generated at run time in the job workspace", path, blob)
			}

			require.NotContainsf(t, content, "-----BEGIN",
				"%s must contain no PEM block: private key material is generated at run time and never committed", path)

			for _, match := range vmCredentialAssignment.FindAllStringSubmatch(content, -1) {
				if vmCredentialKeyExemptions[strings.ToLower(match[1])] || match[2] == "" {
					continue
				}

				require.Truef(t, strings.HasPrefix(match[2], "$") || strings.HasPrefix(match[2], `"$`),
					"%s assigns `%s` a literal value; every credential must come from a run-time generated variable", path, match[0])
			}

			require.NotRegexpf(t, regexp.MustCompile(`(?i)(echo|printf)[^\n]*\$\{?[A-Za-z_]*(PASSWORD|PASSWD)`),
				content, "%s must never print a password", path)
			require.NotRegexpf(t, regexp.MustCompile(`(?i)(echo|printf|cat)[^\n]*id_ed25519[^.\n]*$`),
				content, "%s must never print the private key", path)
		})
	}
}
