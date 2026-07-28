#!/usr/bin/env bash
# Own the complete ephemeral issue-80 image-test lifecycle: acquire the last
# released RPM, compose signed uCore fixtures, consume them in a VM, reset the
# exact private Podman store, and only then remove the workspace.
set -euo pipefail

readonly LOG_KIB=4096
readonly LOG_BYTES=$((LOG_KIB * 1024))

SCRIPT_PATH="$(readlink -f -- "${BASH_SOURCE[0]}")"
readonly SCRIPT_PATH
SCRIPT_DIR="$(dirname -- "$SCRIPT_PATH")"
readonly SCRIPT_DIR
REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd -P)"
readonly REPOSITORY_ROOT

usage() {
    echo "usage: ucore-image-test.sh --run-id LOWERCASE_ID" >&2
}

fail() {
    echo "ucore-image-test: $*" >&2
    exit 1
}

log() {
    echo "ucore-image-test: $*"
}

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

for tool in bash go mktemp podman readlink rm setsid tail timeout; do
    command -v "$tool" >/dev/null 2>&1 ||
        fail "required tool is unavailable: $tool"
done

workspace_parent="${RUNNER_TEMP:-/tmp}"
[[ "$workspace_parent" == /* && -d "$workspace_parent" && ! -L "$workspace_parent" ]] ||
    fail "RUNNER_TEMP must name a real absolute directory"
workspace_parent="$(cd -- "$workspace_parent" && pwd -P)"
readonly workspace_parent
workspace="$(mktemp -d -- "${workspace_parent}/pilothouse-ucore-image.XXXXXXXX")"
readonly workspace
readonly image_dir="${workspace}/fixture-ucore-images"
readonly storage_root="${image_dir}/storage"
readonly image_store="${image_dir}/imagestore"
readonly run_root="${image_dir}/runroot"
readonly podman_tmpdir="${image_dir}/libpod-tmp"
readonly image_tmpdir="${image_dir}/image-tmp"
readonly storage_config="${image_dir}/storage.conf"
current_phase_pid=""

handle_signal() {
    local signal_name="$1"
    local exit_status="$2"
    local phase_pid="$current_phase_pid"

    if [[ -n "$phase_pid" ]]; then
        kill "-${signal_name}" -- "-${phase_pid}" 2>/dev/null || true
        wait "$phase_pid" 2>/dev/null || true
        current_phase_pid=""
    fi
    exit "$exit_status"
}

run_bounded() {
    local phase="$1"
    local duration="$2"
    shift 2
    local phase_log="${workspace}/${phase}.log"
    local status
    local pending_signal=""

    log "starting ${phase}"
    trap 'pending_signal=INT' INT
    trap 'pending_signal=TERM' TERM
    (
        ulimit -f "$LOG_KIB"
        exec setsid timeout --signal=TERM --kill-after=30s "$duration" "$@"
    ) >"$phase_log" 2>&1 &
    current_phase_pid=$!
    local phase_pid="$current_phase_pid"
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
    current_phase_pid=""

    printf '%s\n' "----- ${phase} log (maximum ${LOG_BYTES} bytes) -----"
    tail -c "$LOG_BYTES" -- "$phase_log" || status=1
    printf '%s\n' "----- end ${phase} log -----"
    return "$status"
}

reset_private_store() {
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
}

cleanup() {
    local cleanup_status=0

    reset_private_store || cleanup_status=1
    rm -rf --one-file-system -- "$workspace" || cleanup_status=1
    return "$cleanup_status"
}

cleanup_on_exit() {
    local status="$1"
    local cleanup_status=0

    trap - EXIT INT TERM
    cleanup || cleanup_status=$?
    if ((status == 0 && cleanup_status != 0)); then
        status="$cleanup_status"
    fi
    exit "$status"
}

trap 'cleanup_on_exit $?' EXIT
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM

cd -- "$REPOSITORY_ROOT"
run_bounded acquire-release-rpm 5m \
    go run ./test/image/releaserpm --workspace "$workspace"
run_bounded compose-ucore 75m \
    bash "$SCRIPT_DIR/compose-ucore.sh" --workspace "$workspace" --run-id "$run_id"
run_bounded validate-ucore-vm 75m \
    bash "$SCRIPT_DIR/ucore-vm-test.sh" --workspace "$workspace"

cleanup || fail "exact-store reset or workspace removal failed"
trap - EXIT INT TERM
log "PASS: uCore image lifecycle validated and removed"
