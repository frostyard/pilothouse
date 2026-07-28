#!/usr/bin/env bash
# Run one phase command and retain only the tail of its combined output. The
# caller places this script, the command, and tail beneath one timeout-owned
# process group, so the wall-clock bound covers output collection too.
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
