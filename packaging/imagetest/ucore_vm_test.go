package imagetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	imageVMRunnerPath = "test/image/ucore-vm-test.sh"
	imageVMGuestPath  = "test/image/guest/validate-ucore.sh"
)

func imageReadHarness(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(imageRepositoryRoot(t), path))
	require.NoError(t, err)

	return string(content)
}

func imageShellFunction(t *testing.T, path, script, name string) string {
	t.Helper()

	opener := "\n" + name + "() {\n"
	start := strings.Index(script, opener)
	require.GreaterOrEqualf(t, start, 0, "%s must define %s()", path, name)

	rest := script[start+len(opener):]
	end := strings.Index(rest, "\n}\n")
	require.GreaterOrEqualf(t, end, 0, "%s must close %s() at column zero", path, name)

	return rest[:end]
}

func TestUCoreVMHarnessModesAndShellcheck(t *testing.T) {
	root := imageRepositoryRoot(t)

	runner, err := os.Stat(filepath.Join(root, imageVMRunnerPath))
	require.NoError(t, err)
	require.NotZerof(t, runner.Mode().Perm()&0o111,
		"%s is the directly executed entry point", imageVMRunnerPath)

	guest, err := os.Stat(filepath.Join(root, imageVMGuestPath))
	require.NoError(t, err)
	require.Zerof(t, guest.Mode().Perm()&0o111,
		"%s is copied and invoked through an explicit sh interpreter", imageVMGuestPath)

	for path, dialect := range map[string]string{
		imageVMRunnerPath: "bash",
		imageVMGuestPath:  "sh",
	} {
		sandbox := newImageSandbox(t)
		result := imageRunChild(t, sandbox, imageRequireTool(t, "shellcheck"),
			"--shell="+dialect, filepath.Join(root, path))
		require.NoErrorf(t, result.Err, "shellcheck %s failed:\n%s", path, result.Stderr)
		require.Empty(t, strings.TrimSpace(result.Stdout))
		require.Empty(t, strings.TrimSpace(result.Stderr))
	}
}

func TestUCoreVMRunnerConsumesOnlyThePrivateComposedFixture(t *testing.T) {
	script := imageReadHarness(t, imageVMRunnerPath)

	for _, path := range []string{
		`readonly IMAGE_DIR="${workspace}/fixture-ucore-images"`,
		`readonly IMAGE_MANIFEST="${IMAGE_DIR}/fixture.json"`,
		`assert_storage_path "$storage_root" "${IMAGE_DIR}/storage"`,
		`assert_storage_path "$image_store" "${IMAGE_DIR}/imagestore"`,
		`assert_storage_path "$run_root" "${IMAGE_DIR}/runroot"`,
		`assert_storage_path "$podman_tmpdir" "${IMAGE_DIR}/libpod-tmp"`,
		`assert_storage_path "$image_tmpdir" "${IMAGE_DIR}/image-tmp"`,
		`assert_storage_path "$storage_config" "${IMAGE_DIR}/storage.conf"`,
	} {
		require.Contains(t, script, path)
	}
	require.Contains(t, script, `.kind == "pilothouse-ucore-image-fixture"`)
	require.Contains(t, script, `.source == "ghcr.io/ublue-os/ucore:latest"`)
	require.Contains(t, script, `[[ "$actual_id" == "$expected_id" ]]`,
		"both local refs must be rechecked against their manifested image IDs")
	require.Contains(t, script, `[[ "${baseline_ref%:*}" == "${update_ref%:*}" ]]`,
		"the two slots must belong to one private fixture prefix")

	podman := imageShellFunction(t, imageVMRunnerPath, script, "podman_fixture")
	require.Contains(t, podman, `TMPDIR="$image_tmpdir"`)
	require.Contains(t, podman, `timeout --signal=TERM --kill-after=30s`)
	require.Contains(t, podman, `podman "${podman_args[@]}"`)
	for _, option := range []string{
		"--remote=false",
		`--root "$storage_root"`,
		`--imagestore "$image_store"`,
		`--runroot "$run_root"`,
		`--tmpdir "$podman_tmpdir"`,
		"--events-backend none",
		"--storage-driver overlay",
	} {
		require.Contains(t, script, option)
	}

	require.NotContains(t, script, "podman push")
	require.NotContains(t, script, "skopeo copy")
	require.NotContains(t, script, "docker push")
	require.NotContains(t, script, "rm -rf",
		"the fixture consumer must leave recursive workspace cleanup to the exact-store owner")
}

func TestUCoreVMRunnerUsesComposefsAndLocalUpdateTransport(t *testing.T) {
	script := imageReadHarness(t, imageVMRunnerPath)

	for _, argument := range []string{
		"bootc install to-disk",
		"--generic-image",
		"--via-loopback",
		"--skip-fetch-check",
		"--composefs-backend",
		"--filesystem btrfs",
		"--karg console=ttyS0",
	} {
		require.Contains(t, script, argument)
	}
	require.Contains(t, script,
		`podman_fixture 20m save --format oci-archive --output "$UPDATE_ARCHIVE" "$update_ref"`)
	require.Contains(t, script,
		`guest_run podman load --input /var/tmp/pilothouse-image-update.oci`)
	require.Contains(t, script,
		`guest_run bootc switch --quiet --transport containers-storage "$update_ref"`)
	require.NotContains(t, script, "registry:2")
	require.NotContains(t, script, "bootc switch docker://")

	for _, continuity := range []string{
		`[[ "$(guest_status_digest booted)" == "$staged_digest" ]]`,
		`[[ "$(guest_status_digest rollback)" == "$baseline_booted" ]]`,
		`[[ "$(guest_status_digest booted)" == "$pre_rollback_target" ]]`,
		`[[ "$(guest_status_digest rollback)" == "$pre_rollback_booted" ]]`,
	} {
		require.Contains(t, script, continuity)
	}
	require.Contains(t, script, "run_guest_validation baseline")
	require.Contains(t, script, "run_guest_validation update")
	require.Equal(t, 2, strings.Count(script, "run_guest_validation baseline"),
		"the baseline must be checked both before update and after rollback")
}

func TestUCoreVMRunnerOwnsAndWaitsForEveryLiveResource(t *testing.T) {
	script := imageReadHarness(t, imageVMRunnerPath)

	for _, escape := range []string{"setsid", "nohup", "disown", "daemonize"} {
		require.NotContainsf(t, script, escape,
			"%s must not let a helper escape the teardown owner", imageVMRunnerPath)
	}
	require.Contains(t, script, "qemu_pid=$!")
	stop := imageShellFunction(t, imageVMRunnerPath, script, "stop_qemu")
	require.Contains(t, stop, `kill "$pid"`)
	require.Contains(t, stop, `kill -KILL "$pid"`)
	require.Contains(t, stop, `wait "$pid"`)

	cleanup := imageShellFunction(t, imageVMRunnerPath, script, "cleanup")
	qemu := strings.Index(cleanup, "stop_qemu")
	container := strings.Index(cleanup, "remove_install_container")
	disk := strings.Index(cleanup, "detach_disk")
	require.True(t, qemu >= 0 && container > qemu && disk > container,
		"cleanup must stop/wait QEMU, remove/wait the named install container, then detach host mounts")

	detach := imageShellFunction(t, imageVMRunnerPath, script, "detach_disk")
	require.Contains(t, detach, `umount "$MOUNT_DIR"`)
	require.Contains(t, detach, `losetup --detach "$loop_device"`)
	require.Contains(t, detach, `mountpoint -q "$MOUNT_DIR"`)
	require.Contains(t, detach, `losetup -j "$DISK_IMAGE"`)

	require.Contains(t, script, "-serial stdio",
		"QEMU output must remain on the caller-owned bounded sink, not an unbounded workspace file")
	require.NotContains(t, script, "console.log")
	require.NotContains(t, script, "qemu-stderr.log")
}

func TestUCoreGuestChecksOnlyImageHostDeltas(t *testing.T) {
	script := imageReadHarness(t, imageVMGuestPath)

	require.Contains(t, script,
		`[ "$(cat /usr/lib/pilothouse-image-test/slot)" = "$expected_slot" ]`)
	require.Contains(t, script, `[ "$(getenforce)" = Enforcing ]`)
	require.Contains(t, script, `bootc status --json >/dev/null`)
	require.Contains(t, script, "advertised capabilities do not exactly match independently observed image capabilities")
	require.Contains(t, script, `grep -qx bootc "$work_dir/actual"`)

	for _, expectedProbe := range []string{
		"systemctl show-environment",
		"journalctl --no-pager --lines 0",
		"systemd-sysext list",
		"bootc status --json",
		"rpm-ostree status --json",
		"bootc-fetch-apply-updates.timer",
		"bootc-fetch-apply-updates.service",
		"rpm-ostreed-automatic.timer",
		"rpm-ostreed-automatic.service",
	} {
		require.Contains(t, script, expectedProbe)
	}
	for _, optedOut := range []string{"updex >>", "podman >>", "docker >>", "incus >>"} {
		require.NotContains(t, script, optedOut,
			"unconfigured optional dependencies must not enter the expected advertised set")
	}

	require.Contains(t, script, `.result.bootc_available == true`)
	require.Contains(t, script, `.result.booted.image`)
	require.Contains(t, script, `.result.booted.digest`)

	require.Contains(t, script, `--after-cursor="$journal_cursor"`)
	require.Contains(t, script, `'avc:[[:space:]]+denied'`)
	require.Contains(t, script, `grep -Eiq 'pilothouse|pilothoused|/run/pilothouse|/var/lib/pilothouse'`)
	require.Contains(t, script,
		"not a claim that the RPM provides a dedicated Pilothouse SELinux domain")

	for _, duplicate := range []string{
		"audit.db",
		"/run/pilothouse/.vm-boot-sentinel",
		"wrong password",
		"root login",
		"journal marker",
	} {
		require.NotContainsf(t, script, duplicate,
			"the image tier must not repeat the plain-VM assertion %q", duplicate)
	}
}

func TestUCoreVMRunnerRejectsRelativeWorkspaceBeforeMutation(t *testing.T) {
	sandbox := newImageSandbox(t)
	result := imageRunChild(t, sandbox, imageRequireTool(t, "bash"),
		filepath.Join(imageRepositoryRoot(t), imageVMRunnerPath),
		"--workspace", "relative",
	)

	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.Contains(t, result.Stderr, "--workspace must name an existing absolute directory")
	entries, err := os.ReadDir(sandbox.cwd)
	require.NoError(t, err)
	require.Empty(t, entries)
}
