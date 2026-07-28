package imagetest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"
)

const imageOrchestratorPath = "test/image/ucore-image-test.sh"

func TestUCoreImageOrchestratorLifecycleIsClosed(t *testing.T) {
	file := imageParseShell(t, imageOrchestratorPath, syntax.LangBash)
	allCalls := imageShellAllCalls(t, file)
	topCalls := imageShellCalls(t, imageShellTopLevel(file)...)

	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "cleanup", `
local cleanup_status=0
reset_private_store || cleanup_status=1
rm -rf --one-file-system -- "$workspace" || cleanup_status=1
return "$cleanup_status"
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "cleanup_on_exit", `
local status="$1"
local cleanup_status=0
trap - EXIT INT TERM
cleanup || cleanup_status=$?
if ((status == 0 && cleanup_status != 0)); then
    status="$cleanup_status"
fi
exit "$status"
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "reset_private_store", `
if [[ ! -e "$storage_config" ]]; then
    [[ ! -e "$storage_root" && ! -e "$image_store" && ! -e "$run_root" ]] || {
        echo "ucore-image-test: private store exists without its reviewed configuration" >&2
        return 1
    }
    return 0
fi
[[ -f "$storage_config" && ! -L "$storage_config" ]] || {
    echo "ucore-image-test: private storage configuration is not a regular file" >&2
    return 1
}
run_bounded reset-private-store 10m \
    env \
    CONTAINERS_CONF=/dev/null \
    CONTAINERS_STORAGE_CONF="$storage_config" \
    TMPDIR="$image_tmpdir" \
    podman \
    --remote=false \
    --root "$storage_root" \
    --imagestore "$image_store" \
    --runroot "$run_root" \
    --tmpdir "$podman_tmpdir" \
    --events-backend none \
    --storage-driver overlay \
    system reset --force
`)

	reviewedPhases := [][]string{
		{
			"run_bounded", "acquire-release-rpm", "5m",
			"go", "run", "./test/image/releaserpm", "--workspace", `"$workspace"`,
		},
		{
			"run_bounded", "compose-ucore", "75m",
			"bash", `"$SCRIPT_DIR/compose-ucore.sh"`,
			"--workspace", `"$workspace"`, "--run-id", `"$run_id"`,
		},
		{
			"run_bounded", "validate-ucore-vm", "75m",
			"bash", `"$SCRIPT_DIR/ucore-vm-test.sh"`, "--workspace", `"$workspace"`,
		},
	}
	for _, phase := range reviewedPhases {
		imageRequireExactCallCount(t, allCalls, 1, phase...)
		imageRequireDirectCall(t, file, phase...)
	}
	imageRequireOrderedCalls(t, topCalls,
		"run_bounded", "run_bounded", "run_bounded", "cleanup", "trap", "log",
	)
	imageRequireDirectFailingCall(t, file, "cleanup")
	imageRequireExactCallCount(t, topCalls, 1, "trap", "'cleanup_on_exit $?'", "EXIT")
	imageRequireExactCallCount(t, topCalls, 1, "trap", "-", "EXIT", "INT", "TERM")
	imageRequireExactCallCount(t, topCalls, 1,
		"log", `"PASS: uCore image lifecycle validated and removed"`,
	)

	for _, call := range allCalls {
		require.False(t, imageCallPublishes(call),
			"the lifecycle may not publish any package or image: %#v", call.args)
	}
}

func TestUCoreImageOrchestratorBoundsOutputAndProcesses(t *testing.T) {
	file := imageParseShell(t, imageOrchestratorPath, syntax.LangBash)
	runNode := imageShellFunction(t, imageOrchestratorPath, file, "run_bounded")
	runCalls := imageShellCalls(t, runNode)

	imageRequireExactCallCount(t, runCalls, 1,
		"timeout", "--signal=TERM", "--kill-after=30s", `"$duration"`, `"$@"`,
	)
	imageRequireExactCallCount(t, runCalls, 1,
		"tail", "-c", `"$LOG_BYTES"`, "--", `"$phase_log"`,
	)
	imageRequireExactCallCount(t, runCalls, 1, "ulimit", "-f", `"$LOG_KIB"`)

	var asynchronous []uint
	var substitutions []uint
	syntax.Walk(file, func(node syntax.Node) bool {
		switch typed := node.(type) {
		case *syntax.Stmt:
			if typed.Background || typed.Coprocess || typed.Disown {
				asynchronous = append(asynchronous, typed.Pos().Line())
			}
		case *syntax.CoprocClause:
			asynchronous = append(asynchronous, typed.Pos().Line())
		case *syntax.ProcSubst:
			substitutions = append(substitutions, typed.Pos().Line())
		}
		return true
	})
	require.Empty(t, asynchronous,
		"the outer owner must keep every phase and reset as a foreground child")
	require.Empty(t, substitutions,
		"the outer owner needs no implicit process-substitution children")

	for _, call := range imageShellAllCalls(t, file) {
		for _, forbidden := range []string{"nohup", "setsid"} {
			require.Falsef(t, imageCallContainsProgram(call, forbidden),
				"the outer owner may not detach a child through %s", forbidden)
		}
	}
}

func TestUCoreImageOrchestratorOwnsOnePrivateWorkspace(t *testing.T) {
	file := imageParseShell(t, imageOrchestratorPath, syntax.LangBash)
	allCalls := imageShellAllCalls(t, file)
	topCalls := imageShellCalls(t, imageShellTopLevel(file)...)

	imageRequireExactCallCount(t, allCalls, 1,
		"mktemp", "-d", "--", `"$workspace_parent/pilothouse-ucore-image.XXXXXXXX"`,
	)
	imageRequireExactCallCount(t, allCalls, 1,
		"rm", "-rf", "--one-file-system", "--", `"$workspace"`,
	)
	for _, call := range allCalls {
		if imageCallRecursivelyRemoves(call) {
			require.Equal(t,
				[]string{"rm", "-rf", "--one-file-system", "--", `"$workspace"`},
				call.args,
			)
		}
	}

	expectedReadonly := map[string]string{
		"image_dir":      `"$workspace/fixture-ucore-images"`,
		"storage_root":   `"$image_dir/storage"`,
		"image_store":    `"$image_dir/imagestore"`,
		"run_root":       `"$image_dir/runroot"`,
		"podman_tmpdir":  `"$image_dir/libpod-tmp"`,
		"image_tmpdir":   `"$image_dir/image-tmp"`,
		"storage_config": `"$image_dir/storage.conf"`,
	}
	declarations := imageShellDeclarations(t, imageShellTopLevel(file)...)
	for name, value := range expectedReadonly {
		var matches []imageShellDeclaration
		for _, declaration := range declarations {
			if declaration.variant == "readonly" && declaration.name == name {
				matches = append(matches, declaration)
			}
		}
		require.Lenf(t, matches, 1, "%s must become readonly exactly once", name)
		if value != "" {
			require.Equal(t, value, matches[0].value)
		}
	}
	imageRequireContiguousAssignmentSets(t, file,
		[]string{"workspace"},
		[]string{"workspace"},
	)

	var finalCommands []string
	for _, call := range topCalls {
		command := filepath.Base(imageCallEffectiveCommand(call))
		if slices.Contains([]string{"cleanup", "trap", "log"}, command) {
			finalCommands = append(finalCommands, command)
		}
	}
	require.Equal(t, []string{"trap", "trap", "trap", "cleanup", "trap", "log"}, finalCommands)
}

func TestUCoreImageOrchestratorIsExecutable(t *testing.T) {
	info, err := os.Stat(filepath.Join(imageRepositoryRoot(t), imageOrchestratorPath))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}
