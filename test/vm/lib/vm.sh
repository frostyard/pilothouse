# vm.sh — QEMU/KVM boot and the serial-console diagnostics channel for the
# booted-VM harness (#67).
#
# This is a SOURCED library, not a program: it is committed non-executable and
# is never invoked as a command. Source it from a bash host-side script:
#
#     . test/vm/lib/vm.sh
#     boot_guest debian "$image" "$VM_SEED_ISO" "$VM_WORKSPACE"
#
# The guest stays stock. The pinned base image downloaded by images.sh is
# opened read-only as the backing file of a qcow2 overlay created in the run
# workspace; every write lands in the overlay, so the base is never rewritten
# and can be reused across families and runs. Nothing here builds, derives or
# publishes an OS image, and there is deliberately no offline image-editing
# step: rewriting the guest's disk or kernel command line would make it no
# longer stock, which is exactly what this tier is proving against. Fedora
# therefore also stays SELinux-enforcing.
#
# Diagnostics are the other half of this file. The guest console is a file
# chardev bound to -serial and QEMU's own stderr is a second file; both paths
# are exported so a failure at any stage has something to print, including the
# case where the guest never answers SSH at all. wait_for_console_boot turns
# that from a hope into a gate: a run whose serial log stays empty fails,
# naming the assertion, instead of quietly losing its only diagnostic channel.
#
# shellcheck shell=bash

set -euo pipefail

# VM_SSH_HOST/VM_SSH_PORT are the host side of the user-mode network's SSH
# forward; ssh.sh dials them and declares the same defaults with the same `:-`
# idiom, so the two libraries can be sourced in either order.
VM_SSH_HOST="${VM_SSH_HOST:-127.0.0.1}"
VM_SSH_PORT="${VM_SSH_PORT:-2222}"

# VM_MEMORY_MB and VM_CPUS size the guest. ubuntu-latest runners are 2 vCPU /
# 7 GB, so this leaves the runner room while giving the guest enough to boot
# systemd, cloud-init and the two Pilothouse units.
VM_MEMORY_MB="${VM_MEMORY_MB:-2048}"
VM_CPUS="${VM_CPUS:-2}"

# CONSOLE_BOOT_TIMEOUT is the bounded wait, in seconds, for the guest's serial
# console to produce recognisable boot output.
CONSOLE_BOOT_TIMEOUT="${CONSOLE_BOOT_TIMEOUT:-300}"

# CONSOLE_BOOT_MARKER is the extended regular expression that counts as boot
# output. It is deliberately several alternatives: the earliest lines come from
# the guest kernel, the later ones from journald's ForwardToConsole drop-in and
# cloud-init's tee, so no single producer is load-bearing.
CONSOLE_BOOT_MARKER="${CONSOLE_BOOT_MARKER:-Linux version|systemd\[1\]|cloud-init|login:}"

vm_log() {
    printf 'vm: %s\n' "$*" >&2
}

vm_fail() {
    printf 'vm: %s\n' "$*" >&2
    exit 1
}

# dump_boot_diagnostics prints everything the host knows about a guest that
# cannot be asked anything: the serial console and QEMU's stderr.
dump_boot_diagnostics() {
    local log
    for log in "${QEMU_CONSOLE_LOG:-}" "${QEMU_STDERR_LOG:-}"; do
        [ -n "$log" ] || continue
        printf '===== %s =====\n' "$log" >&2
        if [ -f "$log" ]; then
            cat "$log" >&2
        else
            printf '(missing)\n' >&2
        fi
    done
}

# create_overlay <base-image> <workspace> creates the qcow2 overlay the guest
# actually boots and prints its path. The base is referenced as a backing file
# by absolute path and is opened read-only; qemu-img never writes to it.
create_overlay() {
    if [ "$#" -ne 2 ]; then
        vm_fail "usage: create_overlay <base-image> <workspace>"
    fi

    local base="$1" workspace="$2"
    [ -f "$base" ] || vm_fail "base image not found: $base"

    local absolute_base
    absolute_base="$(cd "$(dirname "$base")" && pwd)/$(basename "$base")"

    local overlay="$workspace/disk.qcow2"
    rm -f "$overlay"
    qemu-img create -q -f qcow2 -F qcow2 -b "$absolute_base" "$overlay" >/dev/null

    vm_log "overlay $overlay backed by $absolute_base (base is never modified)"
    printf '%s\n' "$overlay"
}

# start_vm <overlay> <seed-iso> <workspace> starts QEMU in the background and
# exports QEMU_CONSOLE_LOG, QEMU_STDERR_LOG and QEMU_PID.
start_vm() {
    if [ "$#" -ne 3 ]; then
        vm_fail "usage: start_vm <overlay> <seed-iso> <workspace>"
    fi

    local overlay="$1" seed="$2" workspace="$3"
    [ -f "$overlay" ] || vm_fail "overlay not found: $overlay"
    [ -f "$seed" ] || vm_fail "seed image not found: $seed"

    QEMU_CONSOLE_LOG="$workspace/console.log"
    QEMU_STDERR_LOG="$workspace/qemu-stderr.log"
    export QEMU_CONSOLE_LOG QEMU_STDERR_LOG
    : >"$QEMU_CONSOLE_LOG"
    : >"$QEMU_STDERR_LOG"

    # -accel kvm is required, not preferred: the runner exposes /dev/kvm and
    # software emulation would make this tier too slow to be a gate. No caller
    # may fall back to tcg.
    qemu-system-x86_64 \
        -name "pilothouse-vm-boot" \
        -accel kvm \
        -cpu host \
        -smp "$VM_CPUS" \
        -m "$VM_MEMORY_MB" \
        -display none \
        -chardev "file,id=console,path=$QEMU_CONSOLE_LOG" \
        -serial chardev:console \
        -drive "file=$overlay,if=virtio,format=qcow2" \
        -drive "file=$seed,index=1,media=cdrom,format=raw,readonly=on" \
        -netdev "user,id=net0,hostfwd=tcp:$VM_SSH_HOST:$VM_SSH_PORT-:22" \
        -device virtio-net-pci,netdev=net0 \
        </dev/null >/dev/null 2>"$QEMU_STDERR_LOG" &

    QEMU_PID=$!
    export QEMU_PID
    vm_log "started qemu (pid $QEMU_PID); console $QEMU_CONSOLE_LOG, stderr $QEMU_STDERR_LOG"
}

# stop_vm terminates the guest if it is still running. It is safe to call more
# than once and never fails the run on its own.
stop_vm() {
    local pid="${QEMU_PID:-}"
    [ -n "$pid" ] || return 0

    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        vm_log "stopped qemu (pid $pid)"
    fi
}

# wait_for_console_boot polls the serial console log for CONSOLE_BOOT_MARKER
# and fails, naming the assertion and dumping both host-side logs, if nothing
# recognisable appears within CONSOLE_BOOT_TIMEOUT seconds. This is what makes
# the console a gate rather than a convenience: a run whose serial log receives
# no output cannot pass, so the diagnostics channel is proven working on every
# successful run instead of being discovered broken on a failing one.
wait_for_console_boot() {
    local log="${QEMU_CONSOLE_LOG:-}"
    [ -n "$log" ] || vm_fail "assertion failed: wait_for_console_boot called before start_vm exported QEMU_CONSOLE_LOG"

    vm_log "waiting up to ${CONSOLE_BOOT_TIMEOUT}s for boot output on the serial console"

    local waited=0
    while [ "$waited" -lt "$CONSOLE_BOOT_TIMEOUT" ]; do
        if [ -s "$log" ] && grep -Eq "$CONSOLE_BOOT_MARKER" "$log"; then
            vm_log "serial console produced boot output after ${waited}s"
            return 0
        fi

        if [ -n "${QEMU_PID:-}" ] && ! kill -0 "$QEMU_PID" 2>/dev/null; then
            dump_boot_diagnostics
            vm_fail "assertion failed: qemu exited before the guest produced boot output on the serial console"
        fi

        sleep 5
        waited=$((waited + 5))
    done

    dump_boot_diagnostics
    vm_fail "assertion failed: no boot output matching '$CONSOLE_BOOT_MARKER' appeared on the serial console within ${CONSOLE_BOOT_TIMEOUT}s"
}

# boot_guest <family> <base-image> <seed-iso> <workspace> is the whole boot
# path: overlay, start, prove the console is alive, then wait for SSH. The
# console gate runs BEFORE wait_for_ssh on purpose — if the guest never comes
# up, the SSH timeout is the symptom and the console log is the evidence, and
# evidence that was never proven to exist is worthless. wait_for_ssh lives in
# ssh.sh, which the caller must have sourced too.
boot_guest() {
    if [ "$#" -ne 4 ]; then
        vm_fail "usage: boot_guest <family> <base-image> <seed-iso> <workspace>"
    fi

    local family="$1" base="$2" seed="$3" workspace="$4"
    local overlay
    overlay="$(create_overlay "$base" "$workspace")"

    start_vm "$overlay" "$seed" "$workspace"
    wait_for_console_boot
    wait_for_ssh

    vm_log "$family guest is booted and reachable over ssh"
}
