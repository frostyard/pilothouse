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

# SSH_REBOOT_TIMEOUT bounds the direct proof of a reboot: the time allowed for
# the kernel boot_id to change. Watching for an SSH outage is not proof — a fast
# reboot can disappear between probes — while a changed boot_id both proves a
# new boot and proves that SSH on that new boot is reachable.
SSH_REBOOT_TIMEOUT="${SSH_REBOOT_TIMEOUT:-120}"

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

# guest_run_timeout runs a command in the guest as the administrator account
# under one explicit wall-clock bound. It is the sole raw SSH site.
guest_run_timeout() {
    local duration="$1"
    shift

    local key="${VM_SSH_KEY:-}"
    [ -n "$key" ] || ssh_fail "VM_SSH_KEY is unset: generate_credentials must run first"

    local target
    target="$(guest_target)"

    timeout --signal=TERM --kill-after=10s "$duration" \
        ssh "${GUEST_SSH_OPTS[@]}" -i "$key" -p "$VM_SSH_PORT" "$target" -- "$@"
}

# guest_run runs a command in the guest as the administrator account. The
# destination is resolved into a variable first: an assignment carries the
# failure status of its command substitution, so a missing or malformed
# creds.env stops the run here instead of being handed to ssh as an empty
# destination. Twenty minutes bounds package-manager and validation work while
# leaving their ordinary runtime unconstrained by a short readiness probe.
guest_run() {
    guest_run_timeout 20m "$@"
}

# guest_probe is the bounded sibling of guest_run for readiness and transition
# polling. ConnectTimeout alone is insufficient once SSH has connected: a
# stalled transport or remote command could otherwise make one probe exceed the
# enclosing wait's advertised deadline.
guest_probe() {
    guest_run_timeout 15s "$@"
}

# guest_sudo runs a command in the guest with privilege. -n is mandatory: with
# no TTY a prompting escalation would hang, and this turns a missing NOPASSWD
# grant into an immediate, named failure.
guest_sudo() {
    guest_run sudo -n "$@"
}

# guest_sudo_probe keeps reboot dispatch under the same short bound as the
# transition probes. A successful reboot may sever SSH or leave it connected
# until this wrapper expires; both are accepted by reboot_guest before boot_id
# continuity supplies the actual proof.
guest_sudo_probe() {
    guest_probe sudo -n "$@"
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
        timeout --signal=TERM --kill-after=10s 20m \
            scp "${GUEST_SSH_OPTS[@]}" -i "$key" -P "$VM_SSH_PORT" -- "$target:$source" "$destination"
        ;;
    *)
        timeout --signal=TERM --kill-after=10s 20m \
            scp "${GUEST_SSH_OPTS[@]}" -i "$key" -P "$VM_SSH_PORT" -- "$source" "$target:$destination"
        ;;
    esac
}

# guest_answers_ssh reports whether the guest answers a trivial command now.
guest_answers_ssh() {
    guest_probe true >/dev/null 2>&1
}

# wait_for_ssh polls until the guest answers, bounded by SSH_READY_TIMEOUT.
wait_for_ssh() {
    ssh_log "waiting up to ${SSH_READY_TIMEOUT}s for sshd to answer on $VM_SSH_HOST:$VM_SSH_PORT"

    local started=$SECONDS deadline=$((SECONDS + SSH_READY_TIMEOUT))
    while ((SECONDS < deadline)); do
        if guest_answers_ssh; then
            ssh_log "guest answered ssh after $((SECONDS - started))s"
            return 0
        fi

        sleep 5
    done

    ssh_fail "assertion failed: the guest did not answer ssh within ${SSH_READY_TIMEOUT}s"
}

# guest_boot_id prints the guest's current boot identifier. It changes on
# every boot, so comparing it across a reboot is a deterministic proof that
# the machine actually restarted — one that cannot be satisfied by a
# still-running pre-reboot sshd no matter how the probes fall.
guest_boot_id() {
    guest_probe cat /proc/sys/kernel/random/boot_id
}

# wait_for_boot_id_change <before> polls through both the unreachable interval
# and the new boot until SSH returns a non-empty boot_id different from before.
# It does not require sampling an SSH outage: sufficiently fast guests can
# reboot entirely between two probes, but they cannot retain their boot_id.
wait_for_boot_id_change() {
    local before="$1"
    ssh_log "waiting up to ${SSH_REBOOT_TIMEOUT}s for boot_id ${before} to change"

    local after deadline=$((SECONDS + SSH_REBOOT_TIMEOUT))
    while ((SECONDS < deadline)); do
        if after="$(guest_boot_id 2>/dev/null)"; then
            after="${after//[$'\r\n']/}"
            if [ -n "$after" ] && [ "$after" != "$before" ]; then
                printf '%s\n' "$after"
                return 0
            fi
        fi

        sleep 2
    done

    ssh_fail "assertion failed: boot_id did not change from ${before} within ${SSH_REBOOT_TIMEOUT}s"
}

# reboot_guest reboots the guest and returns when it is reachable again. The
# reboot is issued through guest_sudo_probe, which passes -n, because the one login
# identity is the administrator account and not root.
#
# The reboot command's status is NOT discarded. A successful reboot kills the
# connection under ssh, which reports that as 255; a connected session may
# instead reach the probe's bound and report 124. Those two statuses are the
# expected symptoms. Every other non-zero status means the command did not
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
    output="$(guest_sudo_probe systemctl reboot 2>&1)" || status=$?

    if [ "$status" -ne 0 ] && [ "$status" -ne 255 ] && [ "$status" -ne 124 ]; then
        ssh_fail "reboot command failed with status ${status} before the guest could restart: ${output}"
    fi

    local after
    after="$(wait_for_boot_id_change "$before")"

    ssh_log "guest is back after the reboot (boot_id ${before} -> ${after})"
}
