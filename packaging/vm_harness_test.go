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
	// vmSSHLibPath owns the guest's SSH lifecycle; vmDiagnosticsLibPath owns
	// the failure-time diagnostics discriminator.
	vmCloudInitLibPath   = vmLibDir + "/cloudinit.sh"
	vmVMLibPath          = vmLibDir + "/vm.sh"
	vmSSHLibPath         = vmLibDir + "/ssh.sh"
	vmDiagnosticsLibPath = vmLibDir + "/diagnostics.sh"

	// vmOrchestratorPath is the harness's one entry point: the host-side
	// program every stage runs from. It is EXECUTED, so it is committed
	// executable and is additionally invoked through an explicit interpreter.
	vmOrchestratorPath = vmHarnessDir + "/vm-boot-test.sh"

	// vmGuestDir is the single directory every guest-side assertion script
	// lives in, and vmGuestLibPath is the sourced library they share.
	vmGuestDir     = vmHarnessDir + "/guest"
	vmGuestLibPath = vmGuestDir + "/lib.sh"

	// vmGuestStagingDir is the administrator-writable staging directory in the
	// guest. The single SSH identity cannot write /root, cannot install
	// packages and cannot read a 0600 root-owned file, so every guest-bound
	// copy lands here and privilege is obtained explicitly afterwards.
	vmGuestStagingDir = "~/vm-boot"

	// vmGuestScriptInvocation is the ONLY form in which a guest script may be
	// run: explicit interpreter and explicit escalation, against the staged
	// path.
	vmGuestScriptInvocation = "guest_run sudo -n sh " + vmGuestStagingDir + "/guest/"
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
	vmDiagnosticsLibPath,
	vmGuestLibPath,
}

// vmSourcedBashLibraries are the sourced libraries that are bash, so they carry
// no shebang and open with the fail-fast options.
var vmSourcedBashLibraries = []string{
	vmImagesLibPath,
	vmCloudInitLibPath,
	vmVMLibPath,
	vmSSHLibPath,
	vmDiagnosticsLibPath,
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

	// Host-side code runs on ubuntu-latest only, so it is bash; guest-side code
	// must run on both Debian's dash and Fedora's bash, so it is POSIX sh. Each
	// file is checked under the dialect it is actually written in.
	bash := append(append([]string{}, vmSourcedBashLibraries...), vmOrchestratorPath)
	posix := append([]string{vmGuestLibPath}, vmGuestScripts(t)...)

	for dialect, paths := range map[string][]string{"bash": bash, "sh": posix} {
		for _, path := range paths {
			t.Run(dialect+"/"+filepath.Base(path), func(t *testing.T) {
				out, err := exec.Command(shellcheck, "--shell="+dialect, path).CombinedOutput()
				require.NoErrorf(t, err, "shellcheck --shell=%s %s reported problems:\n%s", dialect, path, out)
				require.Emptyf(t, strings.TrimSpace(string(out)),
					"shellcheck --shell=%s %s must emit no warnings, got:\n%s", dialect, path, out)
			})
		}
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
	require.Contains(t, dump, "for variable in QEMU_CONSOLE_LOG QEMU_STDERR_LOG",
		"%s: the host-side dump must cover the console log and QEMU's stderr", vmVMLibPath)
	require.Contains(t, dump, "(not created: the run failed before start_vm launched qemu)",
		"%s: the host-side dump must print a section for a log whose path is not set yet; diagnostics are armed before start_vm, so skipping an unset path would leave an early failure with no output at all", vmVMLibPath)
	require.NotContains(t, dump, `[ -n "$log" ] || continue`,
		"%s: the host-side dump must not skip a log whose path is unset; an absent log is evidence that the run died before qemu started", vmVMLibPath)

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

	// The scan covers every file under test/vm, not just the library
	// directory: the orchestrator is where the escalations actually happen, and
	// a guard bounded to the directory where the defect was first noticed would
	// let the next call site reintroduce it one directory over.
	for _, path := range vmHarnessFiles(t) {
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
	require.Contains(t, gone, `[ "$misses" -ge "$SSH_GONE_CONFIRMATIONS" ]`,
		"%s: wait_for_ssh_gone must require consecutive unanswered probes; one transient refusal against a guest that never rebooted would otherwise end the wait", vmSSHLibPath)

	reboot := shellFunctionBody(t, vmSSHLibPath, script, "reboot_guest")
	require.Contains(t, reboot, "guest_sudo systemctl reboot",
		"%s: reboot_guest must issue the reboot through the escalation wrapper, which passes -n", vmSSHLibPath)

	require.NotContains(t, reboot, "|| true",
		"%s: reboot_guest must not discard the reboot command's status; a rejected non-interactive escalation would then be indistinguishable from the expected disconnect and would surface as a misleading shutdown timeout", vmSSHLibPath)
	require.NotContains(t, reboot, ">/dev/null 2>&1",
		"%s: reboot_guest must capture the reboot command's output so a real failure can be reported with its stderr", vmSSHLibPath)
	require.Contains(t, reboot, `[ "$status" -ne 0 ] && [ "$status" -ne 255 ]`,
		"%s: reboot_guest must treat only the connection-drop status as the expected symptom and fail on every other non-zero status", vmSSHLibPath)

	require.Contains(t, reboot, `[ "$after" = "$before" ]`,
		"%s: reboot_guest must compare the guest's boot_id across the reboot; the SSH probes alone cannot distinguish a real reboot from an sshd that merely restarted", vmSSHLibPath)
	require.Contains(t, script, "/proc/sys/kernel/random/boot_id",
		"%s must read the guest's boot_id, which changes on every boot, as the deterministic proof that the machine restarted", vmSSHLibPath)

	away := strings.Index(reboot, "wait_for_ssh_gone")
	back := strings.Index(reboot, "\n    wait_for_ssh\n")
	require.GreaterOrEqual(t, away, 0, "%s: reboot_guest must wait for the pre-reboot sshd to go away", vmSSHLibPath)
	require.GreaterOrEqual(t, back, 0, "%s: reboot_guest must wait for sshd to return", vmSSHLibPath)
	require.Less(t, away, back,
		"%s: reboot_guest must wait for the pre-reboot sshd to go away BEFORE waiting for sshd to return", vmSSHLibPath)

	require.Less(t, back, strings.Index(reboot, `[ "$after" = "$before" ]`),
		"%s: reboot_guest must compare boot_id only after the guest is reachable again", vmSSHLibPath)
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
			requireNoPrivateKeyPrint(t, path, content)
		})
	}
}

// vmPrintsSomething matches a command that writes to stdout, and
// vmPrivateKeyRef matches either spelling of the harness private key: the
// literal filename or the variable the scripts actually use. The public key
// beside it is not a credential, so a reference followed by `.pub` is exempt.
var (
	vmPrintsSomething = regexp.MustCompile(`(?i)\b(echo|printf|cat)\b`)
	vmPrivateKeyRef   = regexp.MustCompile(`(?i)(id_ed25519|\$\{?VM_SSH_KEY\}?)`)
)

// requireNoPrivateKeyPrint fails when a line both invokes a printing command
// and names the private key. It scans line by line rather than matching the
// whole file: an anchored pattern only matches at end of text without (?m),
// so a single trailing-anchor regex silently passes every occurrence that is
// not the last thing in the file.
func requireNoPrivateKeyPrint(t *testing.T, path, content string) {
	t.Helper()

	for number, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !vmPrintsSomething.MatchString(line) {
			continue
		}

		for _, match := range vmPrivateKeyRef.FindAllStringIndex(line, -1) {
			rest := line[match[1]:]
			if strings.HasPrefix(rest, ".pub") || strings.HasPrefix(rest, `}.pub`) {
				continue
			}

			t.Fatalf("%s:%d must never print the private key: %s",
				path, number+1, strings.TrimSpace(line))
		}
	}
}

// vmGuestScripts lists every EXECUTED guest-side script: everything under
// test/vm/guest except the sourced lib.sh. The set is discovered on disk rather
// than hand-kept, so a script added by a later change cannot escape the mode,
// dialect, require-root and invocation-form guards below.
//
// The walk is RECURSIVE. A single-level os.ReadDir would let
// test/vm/guest/subdir/new.sh escape every one of those guards while this
// helper still reported success, which is precisely the silent gap a
// discovered-on-disk set exists to close.
func vmGuestScripts(t *testing.T) []string {
	t.Helper()

	var scripts []string

	require.NoError(t, filepath.WalkDir(vmGuestDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
			return nil
		}

		if path == filepath.FromSlash(vmGuestLibPath) {
			return nil
		}

		scripts = append(scripts, path)

		return nil
	}))

	require.NotEmptyf(t, scripts, "%s must hold at least one guest script", vmGuestDir)

	return scripts
}

// vmUnquote removes shell quoting so a call site can be compared against the
// exact command form it must use. The tilde paths in the orchestrator are
// single-quoted on purpose — they are expanded by the guest's shell, not the
// runner's — and that quoting is not part of the form being pinned.
func vmUnquote(line string) string {
	return strings.TrimSpace(strings.NewReplacer("'", "", `"`, "").Replace(line))
}

// vmCodeLines returns the script's non-comment, non-blank lines, unquoted, so
// a form written only in a header comment cannot satisfy a call-site guard.
func vmCodeLines(script string) []string {
	var code []string

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		code = append(code, vmUnquote(trimmed))
	}

	return code
}

// TestVMOrchestratorIsAnExecutableBashProgram pins the executed half of the
// executed-versus-sourced discipline for the harness's entry point. Both
// mechanisms are required and neither is sufficient alone: the file is
// committed executable, and it is additionally invoked through an explicit
// interpreter, because scp does not preserve the executable bit without -p and
// a harness that trusted a copied mode would carry the same defect one layer
// down.
func TestVMOrchestratorIsAnExecutableBashProgram(t *testing.T) {
	info, err := os.Stat(vmOrchestratorPath)
	require.NoErrorf(t, err, "stat %s", vmOrchestratorPath)
	require.NotZerof(t, info.Mode().Perm()&0o111,
		"%s is executed as a program and must be committed executable (100755); mode is %v",
		vmOrchestratorPath, info.Mode())

	script := readVMHarnessFile(t, vmOrchestratorPath)
	require.True(t, strings.HasPrefix(script, "#!/usr/bin/env bash\n"),
		"%s must carry a bash shebang", vmOrchestratorPath)
	require.Equal(t, "set -euo pipefail", effectiveLines(script)[0],
		"the first effective line of %s must be `set -euo pipefail`", vmOrchestratorPath)
}

// TestVMOrchestratorAcceptsOnlyTheTwoFamilies pins the command line: --family
// (debian or fedora) and --artifact-dir, with any other family rejected by
// name rather than defaulted.
func TestVMOrchestratorAcceptsOnlyTheTwoFamilies(t *testing.T) {
	script := readVMHarnessFile(t, vmOrchestratorPath)
	parse := shellFunctionBody(t, vmOrchestratorPath, script, "parse_arguments")

	require.Contains(t, parse, "--family)",
		"%s must accept --family", vmOrchestratorPath)
	require.Contains(t, parse, "--artifact-dir)",
		"%s must accept --artifact-dir", vmOrchestratorPath)
	require.Contains(t, parse, "debian | fedora) ;;",
		"%s must accept exactly the two families this tier covers", vmOrchestratorPath)
	require.Contains(t, parse, `orchestrator_fail "unknown family '$FAMILY': expected debian or fedora"`,
		"%s must reject any other family by name rather than falling back to a default", vmOrchestratorPath)
	require.Contains(t, parse, `orchestrator_fail "artifact directory '$ARTIFACT_DIR' does not exist or is not a directory"`,
		"%s must reject a missing artifact directory by name", vmOrchestratorPath)
}

// TestVMGuestScriptsAreExecutablePOSIXShellScripts is the table-driven mode and
// dialect guard for the executed guest set. It walks the directory rather than
// a hand-kept list, so a guest script added later is held to the same rules:
// committed executable, POSIX sh, fail-fast, sourcing the shared library and
// calling require_root before it does anything else.
func TestVMGuestScriptsAreExecutablePOSIXShellScripts(t *testing.T) {
	for _, path := range vmGuestScripts(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			info, err := os.Stat(path)
			require.NoErrorf(t, err, "stat %s", path)
			require.NotZerof(t, info.Mode().Perm()&0o111,
				"%s is executed as a program and must be committed executable (100755); mode is %v",
				path, info.Mode())

			script := readVMHarnessFile(t, path)
			require.Truef(t, strings.HasPrefix(script, "#!/bin/sh\n"),
				"%s must start with `#!/bin/sh`: Debian's /bin/sh is dash and Fedora's is bash, and the same script runs on both", path)

			lines := effectiveLines(script)
			require.GreaterOrEqual(t, len(lines), 3, "%s is too short to be a guest script", path)
			require.Equalf(t, "set -eu", lines[0],
				"the first effective line of %s must be `set -eu`, so it aborts on its first failed assertion", path)
			require.Equalf(t, `. "$(dirname "$0")/lib.sh"`, lines[1],
				"%s must source the shared guest library from its own staged directory", path)
			require.Equalf(t, "require_root", lines[2],
				"require_root must be %s's first effective statement: a call site that lost its `sudo -n` must fail here, not three assertions deep", path)

			require.Containsf(t, script, `fail "`,
				"%s must report failures through the shared fail(), which names the assertion and exits non-zero", path)
			require.NotContainsf(t, script, "fail() {",
				"%s must not define its own fail(): there is exactly one, in %s", path, vmGuestLibPath)
		})
	}
}

// TestVMGuestLibraryIsSourcedPOSIXSh pins the sourced half inside the guest
// directory. lib.sh is sourced by every guest script and is never invoked, so
// it carries no shebang and no executable bit — the mode guard for that lives
// in TestVMSourcedScriptsAreNotExecutable, which now covers it, so the two
// categories cannot blur in either direction.
func TestVMGuestLibraryIsSourcedPOSIXSh(t *testing.T) {
	script := readVMHarnessFile(t, vmGuestLibPath)

	require.False(t, strings.HasPrefix(script, "#!"),
		"%s is sourced and must not carry a shebang", vmGuestLibPath)
	require.Contains(t, script, "# shellcheck shell=sh",
		"%s must declare its POSIX sh dialect for shellcheck", vmGuestLibPath)
	require.Equal(t, "set -eu", effectiveLines(script)[0],
		"the first effective line of %s must be `set -eu`", vmGuestLibPath)
}

// TestVMGuestLibraryDefinesRequireRootAndOneFail pins the two functions the
// whole guest-side discipline rests on: exactly one fail(), which names the
// failing assertion and exits non-zero, and require_root(), which fails when
// `id -u` is not 0.
func TestVMGuestLibraryDefinesRequireRootAndOneFail(t *testing.T) {
	script := readVMHarnessFile(t, vmGuestLibPath)

	require.Equal(t, 1, strings.Count(script, "\nfail() {\n"),
		"%s must define exactly one fail()", vmGuestLibPath)

	failBody := shellFunctionBody(t, vmGuestLibPath, script, "fail")
	require.Contains(t, failBody, `printf 'assertion failed: %s\n' "$*" >&2`,
		"%s: fail() must print the failing assertion by name", vmGuestLibPath)
	require.Contains(t, failBody, "exit 1",
		"%s: fail() must exit non-zero, so `set -eu` aborts the script at its first failed assertion", vmGuestLibPath)

	requireRoot := shellFunctionBody(t, vmGuestLibPath, script, "require_root")
	require.Contains(t, requireRoot, `require_root_uid="$(id -u)"`,
		"%s: require_root must read the effective uid", vmGuestLibPath)
	require.Contains(t, requireRoot, `[ "$require_root_uid" = "0" ] ||`,
		"%s: require_root must fail unless the uid is 0", vmGuestLibPath)
	require.Contains(t, requireRoot, "fail \"this script must run as root",
		"%s: require_root must name the assertion it failed", vmGuestLibPath)

	// The converse direction: every executed guest script calls it.
	for _, path := range vmGuestScripts(t) {
		require.Containsf(t, readVMHarnessFile(t, path), "\nrequire_root\n",
			"%s must call require_root: it is what makes a dropped `sudo -n` fail immediately and legibly", path)
	}
}

// TestVMGuestCurlWrappersCarryNoInnerEscalation pins the single-escalation
// model. The guest script already runs as root under `sudo -n sh`, and
// require_root proves it at run time, so the curl wrappers carry no inner
// escalation: one boundary is auditable, one per request is not.
func TestVMGuestCurlWrappersCarryNoInnerEscalation(t *testing.T) {
	script := readVMHarnessFile(t, vmGuestLibPath)

	for _, name := range []string{"broker_curl", "web_curl"} {
		body := shellFunctionBody(t, vmGuestLibPath, script, name)
		require.NotContainsf(t, body, "sudo",
			"%s: %s must carry no inner escalation — the script is already root, and require_root is what makes that safe",
			vmGuestLibPath, name)
	}

	broker := shellFunctionBody(t, vmGuestLibPath, script, "broker_curl")
	require.Contains(t, broker, `--unix-socket "$BROKER_SOCKET"`,
		"%s: broker_curl must talk to the broker's Unix socket", vmGuestLibPath)
}

// TestVMAllGuestAssertionScriptsLiveInOneDirectory pins the "single directory"
// fence: every shell file under test/vm is either the one orchestrator, a
// sourced host-side library under lib/, or a guest-side script under guest/.
func TestVMAllGuestAssertionScriptsLiveInOneDirectory(t *testing.T) {
	for _, path := range vmHarnessFiles(t) {
		if !strings.HasSuffix(path, ".sh") {
			continue
		}

		slashed := filepath.ToSlash(path)
		if slashed == vmOrchestratorPath {
			continue
		}

		require.Truef(t,
			strings.HasPrefix(slashed, vmLibDir+"/") || strings.HasPrefix(slashed, vmGuestDir+"/"),
			"%s is neither the orchestrator, a host-side library under %s, nor a guest script under %s: every guest-side assertion script lives in one directory",
			path, vmLibDir, vmGuestDir)
	}
}

// TestVMOrchestratorInvokesGuestScriptsWithInterpreterAndEscalation enumerates
// every guest-script invocation in the orchestrator and requires the full form:
// explicit interpreter AND explicit escalation, against the staged path. A bare
// path would depend on a copied executable bit that scp does not preserve, and
// a missing `sudo -n` would fail obscurely inside the script instead of at the
// call site.
func TestVMOrchestratorInvokesGuestScriptsWithInterpreterAndEscalation(t *testing.T) {
	reference := regexp.MustCompile(regexp.QuoteMeta(vmGuestStagingDir+"/guest/") + `([A-Za-z0-9._-]+\.sh)`)

	invoked := map[string]bool{}

	for _, line := range vmCodeLines(readVMHarnessFile(t, vmOrchestratorPath)) {
		match := reference.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		require.Equalf(t, vmGuestScriptInvocation+match[1], line,
			"%s runs a guest script as `%s`; the only permitted form is `%s`",
			vmOrchestratorPath, line, vmGuestScriptInvocation+match[1])

		invoked[match[1]] = true
	}

	// The converse direction: a guest script that exists but is never invoked
	// would be a check nothing runs.
	for _, path := range vmGuestScripts(t) {
		require.Truef(t, invoked[filepath.Base(path)],
			"%s is never invoked by %s", path, vmOrchestratorPath)
	}
}

// vmArtifactGlob extracts the glob the orchestrator selects each format with,
// as text. Nothing here executes the harness: the pattern is then matched
// against a synthetic file list in Go.
var vmArtifactGlob = regexp.MustCompile(`set -- "\$artifact_dir"/(\S+) ;;`)

// TestVMOrchestratorSelectsTheArchQualifiedArtifact pins artifact selection.
// The packages job uploads both an amd64 and an arm64 file per format, and the
// runner is x86_64 with KVM requiring guest == host architecture, so selection
// is arch-qualified. The globs are not merely asserted as text: they are
// matched against a directory listing that holds both architectures, which is
// the case an unqualified glob would silently get wrong.
func TestVMOrchestratorSelectsTheArchQualifiedArtifact(t *testing.T) {
	script := readVMHarnessFile(t, vmOrchestratorPath)

	require.Contains(t, script, `set -- "$artifact_dir"/*_amd64.deb ;;`,
		"%s must select the Debian artifact with the amd64-qualified glob", vmOrchestratorPath)
	require.Contains(t, script, `set -- "$artifact_dir"/*.x86_64.rpm ;;`,
		"%s must select the Fedora artifact with the x86_64-qualified glob", vmOrchestratorPath)

	for _, bare := range []string{"*.deb", "*.rpm", "*_arm64.deb", "*.aarch64.rpm"} {
		require.NotContainsf(t, script, bare,
			"%s must not use the unqualified or wrong-architecture glob %s: the directory holds both architectures",
			vmOrchestratorPath, bare)
	}

	globs := vmArtifactGlob.FindAllStringSubmatch(script, -1)
	require.Len(t, globs, 2, "%s must declare exactly one glob per format", vmOrchestratorPath)

	listing := map[string][]string{
		"*_amd64.deb":  {"pilothouse_0.5.0_amd64.deb", "pilothouse_0.5.0_arm64.deb"},
		"*.x86_64.rpm": {"pilothouse-0.5.0.x86_64.rpm", "pilothouse-0.5.0.aarch64.rpm"},
	}

	for _, glob := range globs {
		files, ok := listing[glob[1]]
		require.Truef(t, ok, "%s declares an unexpected selection glob %q", vmOrchestratorPath, glob[1])

		matched := 0

		for _, file := range files {
			hit, err := filepath.Match(glob[1], file)
			require.NoError(t, err)

			if hit {
				matched++
			}
		}

		require.Equalf(t, 1, matched,
			"%s: glob %q must match exactly one file in a directory holding both an amd64 and an arm64 artifact of that format, matched %d",
			vmOrchestratorPath, glob[1], matched)
	}

	selection := shellFunctionBody(t, vmOrchestratorPath, script, "select_artifact")
	require.Contains(t, selection, `if [ "$count" -ne 1 ]; then`,
		"%s: selection must require exactly one match", vmOrchestratorPath)
	require.Contains(t, selection, `orchestrator_fail "expected exactly one amd64 $format artifact in $artifact_dir, found ${count}:${names:-" (none)"}"`,
		"%s: an ambiguous or empty match must fail naming the count and the matched basenames", vmOrchestratorPath)
	require.Contains(t, selection, `names="$names $(basename "$candidate")"`,
		"%s: the failure message's basenames must be collected from the matches themselves", vmOrchestratorPath)
}

// TestVMOrchestratorStagesCredentialsPrivileged pins the credential path end to
// end: scp into the administrator-writable staging directory, then a privileged
// install into /root with the owner, group and mode stated, then removal of the
// staged copy so the generated root credential does not linger where the
// unprivileged account can read it. No credential is ever a command-line
// argument — only the path of the file holding it.
func TestVMOrchestratorStagesCredentialsPrivileged(t *testing.T) {
	script := readVMHarnessFile(t, vmOrchestratorPath)
	body := vmUnquote(shellFunctionBody(t, vmOrchestratorPath, script, "install_guest_credentials"))

	steps := []string{
		"guest_copy $VM_CREDS_ENV " + vmGuestStagingDir + "/creds.env",
		"guest_run sudo -n install -o root -g root -m 0600 " + vmGuestStagingDir + "/creds.env /root/.pilothouse-vm-creds",
		"guest_run rm -f " + vmGuestStagingDir + "/creds.env",
	}

	previous := -1

	for _, step := range steps {
		at := strings.Index(body, step)
		require.GreaterOrEqualf(t, at, 0,
			"%s: install_guest_credentials must run `%s`", vmOrchestratorPath, step)
		require.Greaterf(t, at, previous,
			"%s: install_guest_credentials must stage, install privileged, then remove the staged copy, in that order", vmOrchestratorPath)

		previous = at
	}

	// Only the PATH of the file holding the credentials is ever handed to a
	// guest command; the values themselves are never arguments, where they
	// would land in the guest's process table and in any command echo.
	for _, line := range vmCodeLines(readVMHarnessFile(t, vmOrchestratorPath)) {
		for _, credential := range []string{"PH_ADMIN_PASSWORD", "PH_ROOT_PASSWORD"} {
			require.NotContainsf(t, line, credential,
				"%s runs `%s`: no credential may be passed to a guest command as an argument", vmOrchestratorPath, line)
		}
	}
}

// vmRawCodeLines returns the script's non-comment, non-blank lines with their
// original quoting intact. vmCodeLines strips quotes, which is right for
// comparing a call against a pinned form but destroys the only thing that
// distinguishes a real invocation from the same words inside a string literal.
func vmRawCodeLines(script string) []string {
	var code []string

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		code = append(code, trimmed)
	}

	return code
}

// vmQuotedAt reports whether the byte at position sits inside a quoted string.
func vmQuotedAt(line string, position int) bool {
	var single, double bool

	for index, char := range line {
		if index >= position {
			break
		}

		switch char {
		case '\'':
			if !double {
				single = !single
			}
		case '"':
			if !single {
				double = !double
			}
		}
	}

	return single || double
}

// vmInvokes reports whether line actually calls name, as opposed to merely
// naming it inside a string. A usage message such as
// `ssh_fail "usage: guest_copy <local> <remote>"` mentions the function
// without calling it, and a guard that cannot tell the two apart either
// reports a phantom violation or has to be narrowed until it stops covering
// the files that matter.
func vmInvokes(line, name string) bool {
	for index := 0; index < len(line); {
		offset := strings.Index(line[index:], name+" ")
		if offset < 0 {
			return false
		}

		at := index + offset
		if !vmQuotedAt(line, at) {
			return true
		}

		index = at + len(name)
	}

	return false
}

// TestVMOrchestratorCopiesOnlyIntoTheStagingDirectory pins the fence around
// every guest-bound copy. The one SSH identity is the administrator account, so
// each guest_copy must name exactly one guest-side path and that path must be
// inside the staging directory; the host-side side of the copy is the job
// workspace and is deliberately unconstrained.
//
// The scan covers EVERY file in the harness, not just the orchestrator. A
// fence checked only on the one script that happens to call guest_copy today
// is not a fence: any library under test/vm/lib could call the same primitive,
// and the invariant is phrased harness-wide.
func TestVMOrchestratorCopiesOnlyIntoTheStagingDirectory(t *testing.T) {
	for _, path := range vmHarnessFiles(t) {
		t.Run(strings.TrimPrefix(path, vmHarnessDir+"/"), func(t *testing.T) {
			for _, line := range vmRawCodeLines(readVMHarnessFile(t, path)) {
				if !vmInvokes(line, "guest_copy") {
					continue
				}

				staged := 0

				for _, field := range strings.Fields(vmUnquote(line)) {
					if !strings.HasPrefix(field, "~") {
						continue
					}

					require.Truef(t, strings.HasPrefix(field, vmGuestStagingDir+"/"),
						"%s: `%s` addresses the guest path %q, which is outside the staging directory %s",
						path, line, field, vmGuestStagingDir)

					staged++
				}

				require.Equalf(t, 1, staged,
					"%s: `%s` must name exactly one guest-side path, inside %s", path, line, vmGuestStagingDir)
			}
		})
	}

	// scp is allowed in exactly one place: inside guest_copy itself, which is
	// the single site where a guest destination is constructed. Anywhere else
	// in the harness it is a way around the fence above.
	for _, path := range vmHarnessFiles(t) {
		body := readVMHarnessFile(t, path)

		exempt := map[string]bool{}
		if path == filepath.FromSlash(vmSSHLibPath) {
			for _, line := range vmRawCodeLines(shellFunctionBody(t, vmSSHLibPath, body, "guest_copy")) {
				exempt[line] = true
			}
		}

		for _, line := range vmRawCodeLines(body) {
			if !vmInvokes(line, "scp") || exempt[line] {
				continue
			}

			t.Fatalf("%s runs `%s`: every copy must go through guest_copy, which is the one place a guest destination is constructed",
				path, line)
		}
	}

	// The one login identity is the administrator account, so no command
	// anywhere in the harness may address the guest as root.
	for _, path := range vmHarnessFiles(t) {
		require.NotContainsf(t, readVMHarnessFile(t, path), "root@",
			"%s must never address the guest as root: privilege is obtained by escalation, not by a second identity", path)
	}

	// The staging directory exists, owned by the administrator account and
	// readable only by it, before anything is copied into it.
	staging := shellFunctionBody(t, vmOrchestratorPath, readVMHarnessFile(t, vmOrchestratorPath), "create_guest_staging")
	require.Contains(t, vmUnquote(staging), "guest_run mkdir -p "+vmGuestStagingDir+"/guest",
		"%s: the staging directory and its guest/ subdirectory must be created as the administrator account", vmOrchestratorPath)
	require.Contains(t, vmUnquote(staging), "guest_run chmod 0700 "+vmGuestStagingDir,
		"%s: the staging directory must be mode 0700", vmOrchestratorPath)
	require.NotContains(t, staging, "sudo",
		"%s: the staging directory is the administrator account's own, so creating it needs no escalation", vmOrchestratorPath)
}

// TestVMOrchestratorProbesEscalationBeforeItStagesAnything pins the order of
// the run. The escalation probe comes immediately after the guest is reachable
// — boot_guest ends with wait_for_ssh — so a broken NOPASSWD grant is reported
// once, at the top, by name, instead of surfacing three scripts deep. Staging,
// selection, credential installation and the guest bootstrap all follow it.
func TestVMOrchestratorProbesEscalationBeforeItStagesAnything(t *testing.T) {
	script := readVMHarnessFile(t, vmOrchestratorPath)

	probe := shellFunctionBody(t, vmOrchestratorPath, script, "require_passwordless_escalation")
	require.Contains(t, probe, "if ! guest_run sudo -n true; then",
		"%s: the probe must run `sudo -n true` in the guest", vmOrchestratorPath)
	require.Contains(t, probe, `orchestrator_fail "assertion failed: 'sudo -n true' was rejected in the guest`,
		"%s: a rejected probe must fail with a named assertion", vmOrchestratorPath)

	main := shellFunctionBody(t, vmOrchestratorPath, script, "main")

	order := []string{
		"install_failure_diagnostics",
		"boot_guest ",
		"require_passwordless_escalation",
		"create_guest_staging",
		"select_artifact ",
		"stage_artifact ",
		"stage_guest_scripts",
		"install_guest_credentials",
		vmGuestScriptInvocation,
	}

	previous := -1

	for _, step := range order {
		at := strings.Index(vmUnquote(main), step)
		require.GreaterOrEqualf(t, at, 0, "%s: main must call `%s`", vmOrchestratorPath, step)
		require.Greaterf(t, at, previous,
			"%s: main must run its stages in order; `%s` is out of place", vmOrchestratorPath, step)

		previous = at
	}

	// "Immediately" is asserted literally: boot_guest ends with wait_for_ssh,
	// so the probe must be the very next statement. Anything wedged between
	// them would be a step running before passwordless escalation was known to
	// work.
	code := vmCodeLines(main)
	for i, line := range code {
		if !strings.HasPrefix(line, "boot_guest ") {
			continue
		}

		require.Lessf(t, i+1, len(code), "%s: main must do something after boot_guest", vmOrchestratorPath)
		require.Equalf(t, "require_passwordless_escalation", code[i+1],
			"%s: the escalation probe must be the statement immediately after the guest becomes reachable, not merely somewhere later",
			vmOrchestratorPath)
	}
}

// TestVMGuardsNeverExecuteTheHarness is the meta-guard for this file: the
// booted-VM harness needs KVM and a network, so no test here may run any part
// of it. The only process any of these tests spawns is shellcheck.
func TestVMGuardsNeverExecuteTheHarness(t *testing.T) {
	const self = "vm_harness_test.go"

	source := readVMHarnessFile(t, self)

	for _, match := range regexp.MustCompile(`exec\.Command\(([^,)]+)`).FindAllStringSubmatch(source, -1) {
		require.Equalf(t, "shellcheck", strings.TrimSpace(match[1]),
			"%s spawns `%s`: these guards read the harness as text and stat its modes, and must never execute it", self, match[0])
	}

	// Spelled in two pieces so this assertion does not match itself.
	require.NotContains(t, source, "exec.Command"+"Context",
		"%s must spawn nothing but the skip-if-absent shellcheck runner", self)
}

// TestVMDiagnosticsDiscriminateOnLiveSSH pins the failure-time discriminator:
// whether the guest answers SSH AT THE MOMENT OF FAILURE, not whether it ever
// did. A reachable guest yields its own unit status and journals for both
// units; an unreachable one yields the host-side QEMU stderr and serial console
// logs. Neither branch is silent, and the guest is stopped only after the dump.
func TestVMDiagnosticsDiscriminateOnLiveSSH(t *testing.T) {
	script := readVMHarnessFile(t, vmDiagnosticsLibPath)

	install := shellFunctionBody(t, vmDiagnosticsLibPath, script, "install_failure_diagnostics")
	require.Contains(t, install, "trap 'dump_failure_diagnostics $?' ERR",
		"%s must install an ERR trap", vmDiagnosticsLibPath)
	require.Contains(t, install, "trap 'diagnostics_on_exit $?' EXIT",
		"%s must install an EXIT trap", vmDiagnosticsLibPath)

	probe := shellFunctionBody(t, vmDiagnosticsLibPath, script, "guest_is_reachable_now")
	require.Contains(t, probe, "( guest_answers_ssh )",
		"%s: reachability must be probed at failure time, in a subshell so a library exit cannot abandon the dump", vmDiagnosticsLibPath)

	dump := shellFunctionBody(t, vmDiagnosticsLibPath, script, "dump_failure_diagnostics")
	reachable := strings.Index(dump, "if guest_is_reachable_now; then")
	require.GreaterOrEqual(t, reachable, 0,
		"%s: the dump must branch on whether the guest answers ssh now", vmDiagnosticsLibPath)
	require.Contains(t, dump, "dump_guest_unit_diagnostics",
		"%s: the reachable branch must collect the guest's own unit state", vmDiagnosticsLibPath)
	require.Contains(t, dump, "dump_boot_diagnostics",
		"%s: the unreachable branch must fall back to the host-side qemu stderr and serial console logs", vmDiagnosticsLibPath)
	require.Contains(t, dump, `diagnostics_log "the guest answers ssh now`,
		"%s: the reachable branch must say so", vmDiagnosticsLibPath)
	require.Contains(t, dump, `diagnostics_log "the guest does not answer ssh now`,
		"%s: the unreachable branch must say so — neither branch may be silent", vmDiagnosticsLibPath)

	units := shellFunctionBody(t, vmDiagnosticsLibPath, script, "dump_guest_unit_diagnostics")
	require.Contains(t, script, "DIAGNOSTIC_UNITS=(pilothoused.service pilothouse.service)",
		"%s must dump both units: they are separate processes with separate journals, so naming one would hide the other", vmDiagnosticsLibPath)
	require.Contains(t, units, `guest_sudo systemctl status --no-pager --full "$unit"`,
		"%s: unit status needs privilege, so it goes through the escalation wrapper", vmDiagnosticsLibPath)
	require.Contains(t, units, `guest_sudo journalctl --no-pager --lines "$DIAGNOSTIC_JOURNAL_LINES" -u "$unit"`,
		"%s: the journal read needs privilege too, and must be bounded", vmDiagnosticsLibPath)
	require.Contains(t, units, "did not complete in the guest",
		"%s: a collection command that fails must be reported by name, not swallowed", vmDiagnosticsLibPath)

	exitPath := shellFunctionBody(t, vmDiagnosticsLibPath, script, "diagnostics_on_exit")
	require.Less(t, strings.Index(exitPath, "dump_failure_diagnostics"), strings.Index(exitPath, "stop_vm"),
		"%s: diagnostics must be collected before the guest is stopped; a dump from a killed guest is not a dump", vmDiagnosticsLibPath)
}

// TestVMInstallPackageInstallsTheStagedArtifact pins the guest bootstrap: the
// artifact comes from the staging directory under a fixed name (selection
// already happened on the host, arch-qualified), the format follows the guest's
// own package manager, and curl and jq are installed as test fixtures.
func TestVMInstallPackageInstallsTheStagedArtifact(t *testing.T) {
	path := filepath.Join(vmGuestDir, "install-package.sh")
	script := readVMHarnessFile(t, path)

	require.Contains(t, script, `staging_dir="$(cd "$(dirname "$0")/.." && pwd)"`,
		"%s must resolve the staging directory from its own staged path", path)
	require.Contains(t, script, `artifact="$staging_dir/pilothouse-artifact.$format"`,
		"%s must install the artifact staged by the orchestrator", path)

	for _, command := range []string{
		"apt-get install -y curl jq",
		"dnf -y install curl jq",
		`apt-get install -y "$artifact"`,
		`dnf -y install "$artifact"`,
	} {
		require.Containsf(t, script, command, "%s must run `%s`", path, command)
	}

	// Layer A (#77) owns these against the same artifacts, in containers. This
	// script must not restate any of them: the install here is a prerequisite
	// for the booted-host checks, not a second copy of the container tier.
	for _, layerA := range []string{
		"systemd-analyze", "ldd ", "--reinstall", "dpkg -P", "dpkg -r",
		"rpm -e", "apt-get remove", "sysusers", "pam.d",
	} {
		require.NotContainsf(t, script, layerA,
			"%s must not re-assert Layer A's %q check: #77 already covers it in containers", path, layerA)
	}
}

// TestVMHarnessNeverWeakensSELinuxOrRetainsArtifacts pins two negatives across
// the whole harness. The Fedora guest boots SELinux-enforcing and stays that
// way — a failing install is a real failure, not something to be worked around
// by relaxing the policy — and nothing the run produces is uploaded: the disks,
// the seed, the logs and the credentials live in the job workspace and are
// never retained.
func TestVMHarnessNeverWeakensSELinuxOrRetainsArtifacts(t *testing.T) {
	for _, path := range vmHarnessFiles(t) {
		content := readVMHarnessFile(t, path)

		for _, forbidden := range []string{"setenforce", "permissive"} {
			require.NotContainsf(t, content, forbidden,
				"%s must not change the guest's SELinux mode (%s): the Fedora guest stays enforcing", path, forbidden)
		}

		for _, forbidden := range []string{"upload-artifact", "actions/upload"} {
			require.NotContainsf(t, content, forbidden,
				"%s must not upload anything (%s): disks, seeds, logs and credentials stay in the job workspace", path, forbidden)
		}
	}
}
