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

# guest_copy <local-path> <remote-path> copies a file to the guest as the
# administrator account.
guest_copy() {
    if [ "$#" -ne 2 ]; then
        ssh_fail "usage: guest_copy <local-path> <remote-path>"
    fi

    local key="${VM_SSH_KEY:-}"
    [ -n "$key" ] || ssh_fail "VM_SSH_KEY is unset: generate_credentials must run first"

    local target
    target="$(guest_target)"

    scp "${GUEST_SSH_OPTS[@]}" -i "$key" -P "$VM_SSH_PORT" -- "$1" "$target:$2"
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
# SSH_GONE_TIMEOUT.
wait_for_ssh_gone() {
    ssh_log "waiting up to ${SSH_GONE_TIMEOUT}s for the pre-reboot sshd to go away"

    local waited=0
    while [ "$waited" -lt "$SSH_GONE_TIMEOUT" ]; do
        if ! guest_answers_ssh; then
            ssh_log "pre-reboot sshd stopped answering after ${waited}s"
            return 0
        fi

        sleep 2
        waited=$((waited + 2))
    done

    ssh_fail "assertion failed: the pre-reboot sshd was still answering ${SSH_GONE_TIMEOUT}s after the reboot was issued"
}

# reboot_guest reboots the guest and returns when it is reachable again. The
# reboot is issued through guest_sudo, which passes -n, because the one login
# identity is the administrator account and not root. The connection dies with
# the guest, so a non-zero status from the reboot command itself is expected
# and is not evidence of anything; the pair of waits below is.
reboot_guest() {
    ssh_log "rebooting the guest"
    guest_sudo systemctl reboot >/dev/null 2>&1 || true

    wait_for_ssh_gone
    wait_for_ssh
    ssh_log "guest is back after the reboot"
}
