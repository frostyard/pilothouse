package imagetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"
)

const imageOrchestratorPath = "test/image/ucore-image-test.sh"
const boundedOutputCollectorPath = "test/image/capture-bounded-output.sh"

const boundedOutputCollectorReviewedTopLevel = `
set -euo pipefail
(($# >= 3)) || {
    echo "capture-bounded-output: expected LOG_PATH LOG_BYTES COMMAND..." >&2
    exit 2
}
readonly phase_log="$1"
readonly log_bytes="$2"
shift 2
[[ "$phase_log" == /* ]] || {
    echo "capture-bounded-output: LOG_PATH must be absolute" >&2
    exit 2
}
[[ "$log_bytes" =~ ^[1-9][0-9]*$ ]] || {
    echo "capture-bounded-output: LOG_BYTES must be a positive integer" >&2
    exit 2
}
set +e
command "$@" 2>&1 |
    command env --ignore-signal=INT --ignore-signal=TERM \
        tail -c "$log_bytes" >"$phase_log"
pipeline_status=("${PIPESTATUS[@]}")
set -e
if ((pipeline_status[1] != 0)); then
    exit "${pipeline_status[1]}"
fi
exit "${pipeline_status[0]}"
`

const imageOrchestratorReviewedTopLevel = `
set -euo pipefail
readonly LOG_BYTES=4194304
readonly MIN_FREE_KIB=10485760
SCRIPT_PATH="$(readlink -f -- "${BASH_SOURCE[0]}")"
readonly SCRIPT_PATH
SCRIPT_DIR="$(dirname -- "$SCRIPT_PATH")"
readonly SCRIPT_DIR
REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd -P)"
readonly REPOSITORY_ROOT
usage() { :; }
fail() { :; }
log() { :; }
run_id=""
while (($#)); do
    case "$1" in
        --run-id)
            (($# >= 2)) || { usage; exit 2; }
            run_id="$2"
            shift 2
            ;;
        *)
            usage
            exit 2
            ;;
    esac
done
[[ "$run_id" =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]] ||
    fail "--run-id must match [a-z0-9][a-z0-9-]{0,31}"
[[ $EUID -eq 0 ]] ||
    fail "the image lifecycle must run as root"
for tool in bash df go mkdir podman readlink rm setsid sleep tail timeout; do
    command -v "$tool" >/dev/null 2>&1 ||
        fail "required tool is unavailable: $tool"
done
workspace_parent="/var/tmp"
[[ "$workspace_parent" == /* && -d "$workspace_parent" && ! -L "$workspace_parent" ]] ||
    fail "/var/tmp must be a real absolute directory"
workspace_parent="$(cd -- "$workspace_parent" && pwd -P)"
readonly workspace_parent
workspace_nonce=""
read -r workspace_nonce </proc/sys/kernel/random/uuid ||
    fail "cannot read a private workspace nonce"
[[ "$workspace_nonce" =~ ^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$ ]] ||
    fail "the private workspace nonce is malformed"
readonly workspace_nonce
workspace="${workspace_parent}/pilothouse-ucore-image.${workspace_nonce}"
readonly workspace
readonly image_dir="${workspace}/fixture-ucore-images"
readonly storage_root="${image_dir}/storage"
readonly image_store="${image_dir}/imagestore"
readonly run_root="${image_dir}/runroot"
readonly podman_tmpdir="${image_dir}/libpod-tmp"
readonly image_tmpdir="${image_dir}/image-tmp"
readonly storage_config="${image_dir}/storage.conf"
current_phase_pid=""
cleanup_active=0
termination_status=0
workspace_created=0
handle_signal() { :; }
stop_phase_group() { :; }
run_bounded() { :; }
reset_private_store() { :; }
cleanup() { :; }
cleanup_on_exit() { :; }
trap 'cleanup_on_exit $?' EXIT
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
workspace_signal=""
trap 'workspace_signal=INT' INT
trap 'workspace_signal=TERM' TERM
if mkdir -m 0700 -- "$workspace"; then
    workspace_created=1
else
    trap 'handle_signal INT 130' INT
    trap 'handle_signal TERM 143' TERM
    case "$workspace_signal" in
        INT) handle_signal INT 130 ;;
        TERM) handle_signal TERM 143 ;;
    esac
    fail "cannot create the private workspace"
fi
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
case "$workspace_signal" in
    INT) handle_signal INT 130 ;;
    TERM) handle_signal TERM 143 ;;
esac
available_kib="$(df --output=avail -k "$workspace" | tail -n 1)"
readonly available_kib
[[ "$available_kib" =~ ^[[:space:]]*[0-9]+$ ]] ||
    fail "could not determine available workspace disk"
((available_kib >= MIN_FREE_KIB)) ||
    fail "image lifecycle requires at least 10 GiB free in $workspace"
cd -- "$REPOSITORY_ROOT"
run_bounded acquire-release-rpm 5m \
    go run ./test/image/releaserpm --workspace "$workspace"
run_bounded compose-ucore 75m \
    bash "$SCRIPT_DIR/compose-ucore.sh" \
    --workspace "$workspace" \
    --bin-dir "$REPOSITORY_ROOT/bin" \
    --run-id "$run_id"
run_bounded validate-ucore-vm 75m \
    bash "$SCRIPT_DIR/ucore-vm-test.sh" --workspace "$workspace"
cleanup_status=0
cleanup || cleanup_status=$?
trap - EXIT INT TERM
if ((termination_status != 0)); then
    exit "$termination_status"
fi
((cleanup_status == 0)) ||
    fail "exact-store reset or workspace removal failed"
log "PASS: uCore image lifecycle validated and removed"
`

func TestUCoreImageOrchestratorLifecycleIsClosed(t *testing.T) {
	file := imageParseShell(t, imageOrchestratorPath, syntax.LangBash)
	allCalls := imageShellAllCalls(t, file)
	topCalls := imageShellCalls(t, imageShellTopLevel(file)...)

	require.Equal(t,
		imageOrchestratorExpectedTopLevel(t),
		imageOrchestratorNonFunctionTopLevel(t, file),
		"every non-function top-level statement, expansion and redirection must be reviewed exactly",
	)
	require.Equal(t,
		imageOrchestratorExpectedTopLevelShape(t),
		imageOrchestratorTopLevelShape(t, file),
		"function declaration placement and declaration shape must be reviewed exactly",
	)
	imageRequireUniqueFunctions(t, imageOrchestratorPath, file)
	imageRequireExactFunctionSet(t, imageOrchestratorPath, file,
		"usage", "fail", "log", "handle_signal", "run_bounded",
		"stop_phase_group", "reset_private_store", "cleanup", "cleanup_on_exit",
	)
	imageRequireExactShellMode(t, file, "set", "-euo", "pipefail")
	for _, call := range allCalls {
		require.Truef(t, imageCallHasStaticCommand(call),
			"the lifecycle command position must be static: %#v", call.args)
		require.Emptyf(t, call.assignments,
			"the lifecycle may not prefix executable calls with assignments: %#v", call.args)
		require.Falsef(t, imageCallMutatesShellResolution(call),
			"the lifecycle must not redefine shell command resolution: %#v", call.args)
	}
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "usage", `
echo "usage: ucore-image-test.sh --run-id LOWERCASE_ID" >&2
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "fail", `
echo "ucore-image-test: $*" >&2
exit 1
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "log", `
echo "ucore-image-test: $*"
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "handle_signal", `
local signal_name="$1"
local exit_status="$2"
local phase_pid="$current_phase_pid"
trap '' INT TERM
termination_status="$exit_status"
if [[ -n "$phase_pid" ]]; then
    kill "-${signal_name}" -- "-${phase_pid}" 2>/dev/null || true
    wait "$phase_pid" 2>/dev/null || true
    stop_phase_group "$phase_pid" || true
    if ! kill -0 -- "-$phase_pid" 2>/dev/null; then
        current_phase_pid=""
    fi
fi
if ((cleanup_active != 0)); then
    trap 'handle_signal INT 130' INT
    trap 'handle_signal TERM 143' TERM
    return 0
fi
exit "$exit_status"
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "stop_phase_group", `
local phase_pid="$1"
local attempts=0
if ! kill -0 -- "-$phase_pid" 2>/dev/null; then
    return 0
fi
kill -TERM -- "-$phase_pid" 2>/dev/null || true
while kill -0 -- "-$phase_pid" 2>/dev/null && ((attempts < 300)); do
    sleep 0.1
    attempts=$((attempts + 1))
done
if ! kill -0 -- "-$phase_pid" 2>/dev/null; then
    return 0
fi
kill -KILL -- "-$phase_pid" 2>/dev/null || true
attempts=0
while kill -0 -- "-$phase_pid" 2>/dev/null && ((attempts < 50)); do
    sleep 0.1
    attempts=$((attempts + 1))
done
! kill -0 -- "-$phase_pid" 2>/dev/null
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "run_bounded", `
local phase="$1"
local duration="$2"
shift 2
local phase_log="${workspace}/${phase}.log"
local status
local pending_signal=""
local phase_group_ready=0
log "starting ${phase}"
trap 'pending_signal=INT' INT
trap 'pending_signal=TERM' TERM
(
    exec setsid timeout --signal=TERM --kill-after=30s "$duration" \
        bash "$SCRIPT_DIR/capture-bounded-output.sh" \
        "$phase_log" "$LOG_BYTES" "$@"
) &
current_phase_pid=$!
local phase_pid="$current_phase_pid"
while ((phase_group_ready == 0)); do
    if kill -0 -- "-$phase_pid" 2>/dev/null; then
        phase_group_ready=1
    elif ! kill -0 -- "$phase_pid" 2>/dev/null; then
        break
    else
        sleep 0.01
    fi
done
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
case "$pending_signal" in
    INT) handle_signal INT 130 ;;
    TERM) handle_signal TERM 143 ;;
esac
if wait "$phase_pid"; then
    status=0
else
    status=$?
fi
if kill -0 -- "-$phase_pid" 2>/dev/null; then
    status=1
    stop_phase_group "$phase_pid" || true
fi
if ! kill -0 -- "-$phase_pid" 2>/dev/null; then
    current_phase_pid=""
fi
printf '%s\n' "----- ${phase} log (maximum ${LOG_BYTES} bytes) -----"
tail -c "$LOG_BYTES" -- "$phase_log" || status=1
printf '%s\n' "----- end ${phase} log -----"
return "$status"
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "cleanup", `
local cleanup_status=0
cleanup_active=1
if ((workspace_created == 0)); then
    return 0
fi
if [[ -n "$current_phase_pid" ]]; then
    echo "ucore-image-test: refusing cleanup while a phase process group survives" >&2
    return 1
fi
reset_private_store || cleanup_status=1
if [[ -n "$current_phase_pid" ]]; then
    echo "ucore-image-test: refusing workspace removal while the reset process group survives" >&2
    return 1
fi
if rm -rf --one-file-system -- "$workspace"; then
    workspace_created=0
else
    cleanup_status=1
fi
return "$cleanup_status"
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "cleanup_on_exit", `
local status="$1"
local cleanup_status=0
cleanup_active=1
trap - EXIT
cleanup || cleanup_status=$?
trap - INT TERM
if ((termination_status != 0)); then
    status="$termination_status"
elif ((status == 0 && cleanup_status != 0)); then
    status="$cleanup_status"
fi
exit "$status"
`)
	imageRequireExactFunction(t, imageOrchestratorPath, file, syntax.LangBash, "reset_private_store", `
if [[ ! -e "$storage_config" && ! -L "$storage_config" ]]; then
    [[ (! -e "$storage_root" && ! -L "$storage_root") &&
        (! -e "$image_store" && ! -L "$image_store") &&
        (! -e "$run_root" && ! -L "$run_root") ]] || {
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
			"--workspace", `"$workspace"`,
			"--bin-dir", `"$REPOSITORY_ROOT/bin"`,
			"--run-id", `"$run_id"`,
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
	require.Equal(t, []imageShellAssignment{
		{name: "LOG_BYTES", value: "4194304"},
		{name: "MIN_FREE_KIB", value: "10485760"},
		{name: "SCRIPT_PATH", value: `"$(readlink -f -- "${BASH_SOURCE[0]}")"`},
		{name: "SCRIPT_PATH", value: ""},
		{name: "SCRIPT_DIR", value: `"$(dirname -- "$SCRIPT_PATH")"`},
		{name: "SCRIPT_DIR", value: ""},
		{name: "REPOSITORY_ROOT", value: `"$(cd -- "$SCRIPT_DIR/../.."&&pwd -P)"`},
		{name: "REPOSITORY_ROOT", value: ""},
		{name: "run_id", value: `""`},
		{name: "run_id", value: `"$2"`},
		{name: "workspace_parent", value: `"/var/tmp"`},
		{name: "workspace_parent", value: `"$(cd -- "$workspace_parent"&&pwd -P)"`},
		{name: "workspace_parent", value: ""},
		{name: "workspace_nonce", value: `""`},
		{name: "workspace_nonce", value: ""},
		{name: "workspace", value: `"$workspace_parent/pilothouse-ucore-image.$workspace_nonce"`},
		{name: "workspace", value: ""},
		{name: "image_dir", value: `"$workspace/fixture-ucore-images"`},
		{name: "storage_root", value: `"$image_dir/storage"`},
		{name: "image_store", value: `"$image_dir/imagestore"`},
		{name: "run_root", value: `"$image_dir/runroot"`},
		{name: "podman_tmpdir", value: `"$image_dir/libpod-tmp"`},
		{name: "image_tmpdir", value: `"$image_dir/image-tmp"`},
		{name: "storage_config", value: `"$image_dir/storage.conf"`},
		{name: "current_phase_pid", value: `""`},
		{name: "cleanup_active", value: "0"},
		{name: "termination_status", value: "0"},
		{name: "workspace_created", value: "0"},
		{name: "workspace_signal", value: `""`},
		{name: "workspace_created", value: "1"},
		{name: "available_kib", value: `"$(df --output=avail -k "$workspace"|tail -n 1)"`},
		{name: "available_kib", value: ""},
		{name: "cleanup_status", value: "0"},
		{name: "cleanup_status", value: "$?"},
	}, imageShellAssignments(t, imageShellTopLevel(file)...),
		"the complete top-level assignment set must reject command-resolution and control-flow mutation")
	var topCommandSequence []string
	for _, call := range topCalls {
		topCommandSequence = append(
			topCommandSequence,
			filepath.Base(imageCallEffectiveCommand(call)),
		)
	}
	require.Equal(t, []string{
		"set", "readlink", "dirname", "cd", "pwd", "usage", "exit", "shift",
		"usage", "exit", "fail", "fail", ".", "fail", "fail", "cd", "pwd",
		"read", "fail", "fail", "trap", "trap", "trap", "trap", "trap",
		"mkdir", "trap", "trap", "handle_signal", "handle_signal", "fail",
		"trap", "trap", "handle_signal", "handle_signal", "df", "tail", "fail",
		"fail", "cd", "run_bounded",
		"run_bounded", "run_bounded", "cleanup", "trap", "exit", "fail", "log",
	}, topCommandSequence,
		"the complete top-level command order must keep traps ahead of all phases and forbid success shortcuts")
	imageRequireOrderedCalls(t, topCalls,
		"mkdir", "run_bounded", "run_bounded", "run_bounded", "cleanup", "trap", "log",
	)
	imageRequireExactCallCount(t, topCalls, 1, "trap", "'cleanup_on_exit $?'", "EXIT")
	imageRequireDirectCall(t, file, "trap", "'cleanup_on_exit $?'", "EXIT")
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
		"exec", "setsid", "timeout", "--signal=TERM", "--kill-after=30s",
		`"$duration"`, "bash", `"$SCRIPT_DIR/capture-bounded-output.sh"`,
		`"$phase_log"`, `"$LOG_BYTES"`, `"$@"`,
	)

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
	require.Len(t, asynchronous, 1,
		"run_bounded's timeout-owned process group must be the only asynchronous statement")
	require.Empty(t, substitutions,
		"the outer owner needs no implicit process-substitution children")

	for _, call := range imageShellAllCalls(t, file) {
		for _, forbidden := range []string{"nohup"} {
			require.Falsef(t, imageCallContainsProgram(call, forbidden),
				"the outer owner may not detach a child through %s", forbidden)
		}
	}
}

func TestUCoreImageBoundedOutputCollectorIsClosed(t *testing.T) {
	file := imageParseShell(t, boundedOutputCollectorPath, syntax.LangBash)
	expectedFile := imageParseShellSource(
		t,
		"reviewed-"+boundedOutputCollectorPath,
		boundedOutputCollectorReviewedTopLevel,
		syntax.LangBash,
	)
	require.Equal(t,
		imageOrchestratorNonFunctionTopLevel(t, expectedFile),
		imageOrchestratorNonFunctionTopLevel(t, file),
		"every collector statement, expansion and redirection must be reviewed exactly",
	)

	functionCount := 0
	var asynchronous []uint
	var substitutions []uint
	syntax.Walk(file, func(node syntax.Node) bool {
		switch typed := node.(type) {
		case *syntax.FuncDecl:
			functionCount++
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
	require.Zero(t, functionCount,
		"the collector may not hide alternate execution paths in functions")
	require.Empty(t, asynchronous,
		"the collector must keep both pipeline processes inside its caller-owned group")
	require.Empty(t, substitutions,
		"the collector may not create implicit children outside its reviewed pipeline")

	calls := imageShellAllCalls(t, file)
	imageRequireExactCallCount(t, calls, 1, "command", `"$@"`)
	imageRequireExactCallCount(t, calls, 1,
		"command", "env", "--ignore-signal=INT", "--ignore-signal=TERM",
		"tail", "-c", `"$log_bytes"`,
	)
	for _, call := range calls {
		if !imageCallHasStaticCommand(call) {
			require.Equal(t, []string{"command", `"$@"`}, call.args,
				"the reviewed argv behind the command builtin must be the only dynamic resolution")
		}
		require.Emptyf(t, call.assignments,
			"the collector may not prefix executable calls with assignments: %#v", call.args)
		require.Falsef(t, imageCallMutatesShellResolution(call),
			"the collector must not redefine shell command resolution: %#v", call.args)
		require.False(t, imageCallPublishes(call),
			"the collector may not publish output: %#v", call.args)
	}
}

func TestUCoreImageOrchestratorGuardSeesAssignmentOnlyCommandShadow(t *testing.T) {
	source := imageReadHarness(t, imageOrchestratorPath)
	shadowed := strings.Replace(
		source,
		"termination_status=0\n",
		"termination_status=0\nBASH_CMDS[setsid]=/usr/bin/true\n",
		1,
	)
	require.NotEqual(t, source, shadowed)

	baselineFile := imageParseShellSource(
		t,
		imageOrchestratorPath,
		source,
		syntax.LangBash,
	)
	shadowedFile := imageParseShellSource(
		t,
		"shadowed-"+imageOrchestratorPath,
		shadowed,
		syntax.LangBash,
	)
	baseline := imageShellAssignments(t, imageShellTopLevel(baselineFile)...)
	shadowedAssignments := imageShellAssignments(t, imageShellTopLevel(shadowedFile)...)
	require.Len(t, shadowedAssignments, len(baseline)+1)
	require.NotEqual(t, baseline, shadowedAssignments,
		"the lifecycle's exact top-level assignment guard must reject command-cache mutation")

	var shadows []imageShellAssignment
	for _, assignment := range shadowedAssignments {
		if assignment.name == "BASH_CMDS" {
			shadows = append(shadows, assignment)
		}
	}
	require.Equal(t, []imageShellAssignment{
		{name: "BASH_CMDS", value: "/usr/bin/true"},
	}, shadows)
	require.NotEqual(t,
		imageOrchestratorExpectedTopLevel(t),
		imageOrchestratorNonFunctionTopLevel(t, shadowedFile),
		"the exact top-level guard must reject assignment-only command-cache mutation",
	)
}

func TestUCoreImageOrchestratorGuardRejectsConstantWorkspaceNonce(t *testing.T) {
	source := imageReadHarness(t, imageOrchestratorPath)
	const reviewedRead = "read -r workspace_nonce </proc/sys/kernel/random/uuid"
	const constantRead = "read -r workspace_nonce <<<'00000000-0000-4000-8000-000000000000'"
	mutated := strings.Replace(source, reviewedRead, constantRead, 1)
	require.NotEqual(t, source, mutated)

	mutatedFile := imageParseShellSource(
		t,
		"constant-nonce-"+imageOrchestratorPath,
		mutated,
		syntax.LangBash,
	)
	require.NotEqual(t,
		imageOrchestratorExpectedTopLevel(t),
		imageOrchestratorNonFunctionTopLevel(t, mutatedFile),
		"the exact top-level guard must reject a predictable workspace nonce source",
	)
}

func TestUCoreImageOrchestratorGuardRejectsDecoratedFunctionDeclaration(t *testing.T) {
	source := imageReadHarness(t, imageOrchestratorPath)
	const reviewed = `return "$status"
}

reset_private_store()`
	const decorated = `return "$status"
} <"${BASH_CMDS[setsid]:=/usr/bin/true}"

reset_private_store()`
	mutated := strings.Replace(source, reviewed, decorated, 1)
	require.NotEqual(t, source, mutated)

	mutatedFile := imageParseShellSource(
		t,
		"decorated-function-"+imageOrchestratorPath,
		mutated,
		syntax.LangBash,
	)
	require.Equal(t,
		imageOrchestratorExpectedTopLevel(t),
		imageOrchestratorNonFunctionTopLevel(t, mutatedFile),
		"the mutation must exercise the function-declaration seam rather than a non-function statement",
	)
	require.NotEqual(t,
		imageOrchestratorExpectedTopLevelShape(t),
		imageOrchestratorTopLevelShape(t, mutatedFile),
		"the declaration-shape guard must reject side-effecting function redirections",
	)
}

func TestUCoreImageOrchestratorGuardRejectsLateCleanupDeclaration(t *testing.T) {
	source := imageReadHarness(t, imageOrchestratorPath)
	start := strings.Index(source, "\ncleanup_on_exit() {")
	require.NotEqual(t, -1, start)
	start++
	const closingMarker = "\n}\n\ntrap 'cleanup_on_exit $?' EXIT"
	closing := strings.Index(source[start:], closingMarker)
	require.NotEqual(t, -1, closing)
	end := start + closing + len("\n}")
	declaration := source[start:end]
	mutated := source[:start] + source[end:] + "\n" + declaration + "\n"

	mutatedFile := imageParseShellSource(
		t,
		"late-cleanup-"+imageOrchestratorPath,
		mutated,
		syntax.LangBash,
	)
	require.Equal(t,
		imageOrchestratorExpectedTopLevel(t),
		imageOrchestratorNonFunctionTopLevel(t, mutatedFile),
		"the mutation must preserve every non-function statement",
	)
	require.NotEqual(t,
		imageOrchestratorExpectedTopLevelShape(t),
		imageOrchestratorTopLevelShape(t, mutatedFile),
		"the top-level shape guard must reject a cleanup declaration after its trap and callers",
	)
}

func TestUCoreImageOrchestratorOwnsOnePrivateWorkspace(t *testing.T) {
	file := imageParseShell(t, imageOrchestratorPath, syntax.LangBash)
	allCalls := imageShellAllCalls(t, file)

	imageRequireExactCallCount(t, allCalls, 1,
		"mkdir", "-m", "0700", "--", `"$workspace"`,
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

}

func TestUCoreImageOrchestratorIsExecutable(t *testing.T) {
	info, err := os.Stat(filepath.Join(imageRepositoryRoot(t), imageOrchestratorPath))
	require.NoError(t, err)
	require.NotZerof(t, info.Mode().Perm()&0o111,
		"%s is executed as a program and must be committed executable (100755); mode is %v",
		imageOrchestratorPath, info.Mode())
}

func imageOrchestratorExpectedTopLevel(t *testing.T) []string {
	t.Helper()
	expected := imageParseShellSource(
		t,
		"reviewed-"+imageOrchestratorPath,
		imageOrchestratorReviewedTopLevel,
		syntax.LangBash,
	)
	return imageOrchestratorNonFunctionTopLevel(t, expected)
}

func imageOrchestratorExpectedTopLevelShape(t *testing.T) []string {
	t.Helper()
	expected := imageParseShellSource(
		t,
		"reviewed-shape-"+imageOrchestratorPath,
		imageOrchestratorReviewedTopLevel,
		syntax.LangBash,
	)
	return imageOrchestratorTopLevelShape(t, expected)
}

func imageOrchestratorNonFunctionTopLevel(t *testing.T, file *syntax.File) []string {
	t.Helper()

	statements := make([]string, 0, len(file.Stmts))
	for _, statement := range file.Stmts {
		if _, isFunction := statement.Cmd.(*syntax.FuncDecl); isFunction {
			continue
		}
		statements = append(statements, imageShellRender(t, statement))
	}
	return statements
}

func imageOrchestratorTopLevelShape(t *testing.T, file *syntax.File) []string {
	t.Helper()

	statements := make([]string, 0, len(file.Stmts))
	for _, statement := range file.Stmts {
		declaration, isFunction := statement.Cmd.(*syntax.FuncDecl)
		if !isFunction {
			statements = append(statements, imageShellRender(t, statement))
			continue
		}

		marker := "function:" + declaration.Name.Value
		decoratedBody := declaration.Body != nil &&
			(declaration.Body.Background || declaration.Body.Coprocess ||
				declaration.Body.Disown || declaration.Body.Negated ||
				len(declaration.Body.Redirs) != 0)
		if statement.Background || statement.Coprocess || statement.Disown ||
			statement.Negated || len(statement.Redirs) != 0 || decoratedBody {
			marker += ":decorated:" + imageShellRender(t, statement)
		}
		statements = append(statements, marker)
	}
	return statements
}
