# ssh.sh — the guest's SSH lifecycle for the booted-VM harness (#67).
#
# This is a SOURCED library, not a program: it is committed non-executable and
# is never invoked as a command. Source it from a bash host-side script:
#
#     . test/vm/lib/ssh.sh
#     wait_for_ssh
#     guest_run systemctl --version
#
# There is exactly ONE SSH identity in the guest: the administrator account
# cloud-init created from the workspace-generated public key, whose name is
# read out of creds.env. Nothing here ever addresses the guest as root — root
# has no authorized key and SSH root login is not enabled — so everything that
# needs privilege escalates through guest_sudo, which passes -n. That flag is
# load-bearing: these commands are non-interactive with no TTY, so an
# escalation that asked for a password would hang until the run's timeout
# instead of failing immediately and legibly.
#
# shellcheck shell=bash

set -euo pipefail

# VM_SSH_HOST/VM_SSH_PORT are the host side of the user-mode network's SSH
# forward. vm.sh, which creates the forward, declares them with the same
# defaults and the same `:-` idiom, so whichever library is sourced first wins
# and neither depends on the other having been sourced already. A guard test
# holds the two declarations identical, so the dialled endpoint and the
# forwarded one cannot drift apart.
VM_SSH_HOST="${VM_SSH_HOST:-127.0.0.1}"
VM_SSH_PORT="${VM_SSH_PORT:-2222}"

# SSH_READY_TIMEOUT is the bounded wait, in seconds, for sshd to answer. It
# covers first boot, which includes cloud-init creating the account and
# installing its key, so it is generous but explicit and finite.
SSH_READY_TIMEOUT="${SSH_READY_TIMEOUT:-300}"

# SSH_GONE_TIMEOUT is the bounded wait for the PRE-REBOOT sshd to stop
# answering. Without this half, a readiness check issued too soon after a
# reboot request would be satisfied by the sshd that is about to die, and the
# reboot would never actually be observed.
SSH_GONE_TIMEOUT="${SSH_GONE_TIMEOUT:-120}"

# SSH_GONE_CONFIRMATIONS is how many consecutive unanswered probes count as
# the pre-reboot sshd being gone. One is not enough: a single transient
# refusal against a guest that never rebooted would end the wait, and the
# readiness check that follows would be satisfied by that same sshd.
SSH_GONE_CONFIRMATIONS="${SSH_GONE_CONFIRMATIONS:-3}"

# GUEST_SSH_OPTS are the transport options every connection shares. The guest
# is a throwaway VM reachable only through a loopback port forward, and its
# host key is generated on first boot, so host-key persistence is off;
# BatchMode makes any prompt an immediate failure instead of a hang.
GUEST_SSH_OPTS=(
    -o BatchMode=yes
    -o StrictHostKeyChecking=no
    -o UserKnownHostsFile=/dev/null
    -o GlobalKnownHostsFile=/dev/null
    -o LogLevel=ERROR
    -o ConnectTimeout=10
)

ssh_log() {
    printf 'ssh: %s\n' "$*" >&2
}

ssh_fail() {
    printf 'ssh: %s\n' "$*" >&2
    exit 1
}

# guest_admin_user prints the single login account's name, read from the
# generated creds.env. The file is sourced in a subshell with tracing off so
# the passwords beside it cannot leak into the job log, and only the account
# name is printed.
guest_admin_user() {
    local creds="${VM_CREDS_ENV:-${VM_WORKSPACE:-}/creds.env}"
    [ -f "$creds" ] || ssh_fail "credentials file not found: $creds"

    local user
    user="$(
        set +x
        # shellcheck disable=SC1090,SC1091 # generated at run time by cloudinit.sh
        . "$creds"
        printf '%s\n' "${PH_ADMIN_USER:-}"
    )"

    [ -n "$user" ] || ssh_fail "$creds declares no PH_ADMIN_USER"
    printf '%s\n' "$user"
}

# guest_target prints <administrator>@<host>. It is the only place a guest SSH
# destination is constructed, so there is exactly one site to audit.
guest_target() {
    printf '%s@%s\n' "$(guest_admin_user)" "$VM_SSH_HOST"
}

# guest_run runs a command in the guest as the administrator account. The
# destination is resolved into a variable first: an assignment carries the
# failure status of its command substitution, so a missing or malformed
# creds.env stops the run here instead of being handed to ssh as an empty
# destination.
guest_run() {
    local key="${VM_SSH_KEY:-}"
    [ -n "$key" ] || ssh_fail "VM_SSH_KEY is unset: generate_credentials must run first"

    local target
    target="$(guest_target)"

    ssh "${GUEST_SSH_OPTS[@]}" -i "$key" -p "$VM_SSH_PORT" "$target" -- "$@"
}

# guest_sudo runs a command in the guest with privilege. -n is mandatory: with
# no TTY a prompting escalation would hang, and this turns a missing NOPASSWD
# grant into an immediate, named failure.
guest_sudo() {
    guest_run sudo -n "$@"
}

# guest_copy <source> <destination> copies one file between the runner and the
# guest as the administrator account, in either direction. Exactly one of the
# two paths names the guest, and it is recognised by its leading `~`: every
# guest-side path in this harness is inside the administrator's `~/vm-boot`
# staging directory, and no host-side path the harness constructs is written
# that way. Requiring exactly one keeps a single function — and therefore a
# single audited site where a guest destination or source is built — for both
# directions, and turns a host-to-host or guest-to-guest call into an immediate,
# named failure instead of a silent local copy.
#
# The retrieval direction is what brings the pre-reboot state file back to the
# job workspace. Its host-side end is necessarily outside the staging directory;
# the staging fence constrains the guest end of a copy, which is why exactly one
# `~` path is required rather than none or two.
guest_copy() {
    if [ "$#" -ne 2 ]; then
        ssh_fail "usage: guest_copy <source> <destination>"
    fi

    local key="${VM_SSH_KEY:-}"
    [ -n "$key" ] || ssh_fail "VM_SSH_KEY is unset: generate_credentials must run first"

    local source="$1" destination="$2"

    local guest_ends=0
    case "$source" in '~'*) guest_ends=$((guest_ends + 1)) ;; esac
    case "$destination" in '~'*) guest_ends=$((guest_ends + 1)) ;; esac

    if [ "$guest_ends" -ne 1 ]; then
        ssh_fail "usage: guest_copy <source> <destination>: exactly one end must be a guest path under the staging directory, written with a leading tilde, but ${guest_ends} were"
    fi

    local target
    target="$(guest_target)"

    case "$source" in
    '~'*)
        scp "${GUEST_SSH_OPTS[@]}" -i "$key" -P "$VM_SSH_PORT" -- "$target:$source" "$destination"
        ;;
    *)
        scp "${GUEST_SSH_OPTS[@]}" -i "$key" -P "$VM_SSH_PORT" -- "$source" "$target:$destination"
        ;;
    esac
}

# guest_answers_ssh reports whether the guest answers a trivial command now.
guest_answers_ssh() {
    guest_run true >/dev/null 2>&1
}

# wait_for_ssh polls until the guest answers, bounded by SSH_READY_TIMEOUT.
wait_for_ssh() {
    ssh_log "waiting up to ${SSH_READY_TIMEOUT}s for sshd to answer on $VM_SSH_HOST:$VM_SSH_PORT"

    local waited=0
    while [ "$waited" -lt "$SSH_READY_TIMEOUT" ]; do
        if guest_answers_ssh; then
            ssh_log "guest answered ssh after ${waited}s"
            return 0
        fi

        sleep 5
        waited=$((waited + 5))
    done

    ssh_fail "assertion failed: the guest did not answer ssh within ${SSH_READY_TIMEOUT}s"
}

# wait_for_ssh_gone polls until the guest stops answering, bounded by
# SSH_GONE_TIMEOUT. A single failed probe is not enough: a transient refusal
# on a guest that never rebooted would satisfy a one-shot check, and
# wait_for_ssh would then be satisfied by the very sshd that was supposed to
# die. SSH_GONE_CONFIRMATIONS consecutive failures are required, and the
# counter resets the moment the guest answers again.
wait_for_ssh_gone() {
    ssh_log "waiting up to ${SSH_GONE_TIMEOUT}s for the pre-reboot sshd to go away"

    local waited=0 misses=0
    while [ "$waited" -lt "$SSH_GONE_TIMEOUT" ]; do
        if guest_answers_ssh; then
            misses=0
        else
            misses=$((misses + 1))
            if [ "$misses" -ge "$SSH_GONE_CONFIRMATIONS" ]; then
                ssh_log "pre-reboot sshd stopped answering after ${waited}s (${misses} consecutive probes)"
                return 0
            fi
        fi

        sleep 2
        waited=$((waited + 2))
    done

    ssh_fail "assertion failed: the pre-reboot sshd was still answering ${SSH_GONE_TIMEOUT}s after the reboot was issued"
}

# guest_boot_id prints the guest's current boot identifier. It changes on
# every boot, so comparing it across a reboot is a deterministic proof that
# the machine actually restarted — one that cannot be satisfied by a
# still-running pre-reboot sshd no matter how the probes fall.
guest_boot_id() {
    guest_run cat /proc/sys/kernel/random/boot_id
}

# reboot_guest reboots the guest and returns when it is reachable again. The
# reboot is issued through guest_sudo, which passes -n, because the one login
# identity is the administrator account and not root.
#
# The reboot command's status is NOT discarded. A successful reboot kills the
# connection under ssh, which reports that as 255; that one status is the
# expected symptom. Every other non-zero status means the command did not
# dispatch at all — a rejected non-interactive escalation being the likely
# one — and is reported with the remote stderr instead of being left to
# surface later as a misleading "sshd never went away" timeout.
reboot_guest() {
    local before
    before="$(guest_boot_id)"
    before="${before//[$'\r\n']/}"
    [ -n "$before" ] || ssh_fail "could not read the guest's boot_id before rebooting"

    ssh_log "rebooting the guest (boot_id ${before})"

    local output status=0
    output="$(guest_sudo systemctl reboot 2>&1)" || status=$?

    if [ "$status" -ne 0 ] && [ "$status" -ne 255 ]; then
        ssh_fail "reboot command failed with status ${status} before the guest could restart: ${output}"
    fi

    wait_for_ssh_gone
    wait_for_ssh

    local after
    after="$(guest_boot_id)"
    after="${after//[$'\r\n']/}"
    [ -n "$after" ] || ssh_fail "could not read the guest's boot_id after the reboot"

    if [ "$after" = "$before" ]; then
        ssh_fail "assertion failed: boot_id is unchanged (${after}); the guest answered ssh but never rebooted"
    fi

    ssh_log "guest is back after the reboot (boot_id ${before} -> ${after})"
}
