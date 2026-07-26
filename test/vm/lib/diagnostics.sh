# diagnostics.sh — the failure-time diagnostics discriminator for the booted-VM
# harness (#67).
#
# This is a SOURCED library, not a program: it is committed non-executable and
# is never invoked as a command. Source it from a bash host-side script, after
# vm.sh and ssh.sh, and arm it once before the guest is created:
#
#     . test/vm/lib/diagnostics.sh
#     install_failure_diagnostics
#
# The discriminator is deliberately "does the guest answer SSH *at the moment
# of failure*", not "did it ever answer". A run that installed the package and
# then failed a later assertion has a live guest whose own unit state and
# journal are the evidence; a run that failed because the image did not verify,
# QEMU never started, cloud-init never finished, sshd never came up, or the
# guest never returned from a reboot has no guest to ask, and the only evidence
# is host-side: QEMU's stderr and the serial console log. Probing at failure
# time is what makes the reboot case work — a guest that dies mid-reboot
# answered SSH minutes earlier and cannot answer now.
#
# Neither branch is silent, and nothing here uploads anything: diagnostics are
# printed to this process's standard output, which is the job log, and the
# workspace holding the disks, the seed and the credentials is never retained.
#
# shellcheck shell=bash

set -euo pipefail

# DIAGNOSTIC_UNITS are the two units this tier installs. They are separate
# processes with separate journals, so both are always dumped: naming only one
# would hide whichever half actually failed.
DIAGNOSTIC_UNITS=(pilothoused.service pilothouse.service)

# DIAGNOSTIC_JOURNAL_LINES bounds each journal dump so a failing run leaves
# evidence rather than an unreadable wall of records.
DIAGNOSTIC_JOURNAL_LINES="${DIAGNOSTIC_JOURNAL_LINES:-200}"

# DIAGNOSTICS_DUMPED guards against a double dump: the ERR trap fires first and
# the EXIT trap follows it on the way out.
DIAGNOSTICS_DUMPED=0

diagnostics_log() {
    printf 'diagnostics: %s\n' "$*"
}

# guest_is_reachable_now answers the discriminator's question. The probe runs
# in a subshell because the SSH library reports a missing key or a malformed
# credentials file by exiting, and an exit taken inside a trap handler would
# abandon the rest of the dump; a subshell confines it to a false answer, which
# is exactly the answer that case deserves.
guest_is_reachable_now() {
    ( guest_answers_ssh ) >/dev/null 2>&1
}

# dump_guest_unit_diagnostics prints each unit's own status and journal from
# the live guest. Both need privilege, so both go through the escalation
# wrapper. A collection command that does not complete is reported by name
# rather than swallowed — a diagnostics dump that silently produces nothing is
# the failure mode this whole file exists to prevent.
dump_guest_unit_diagnostics() {
    local unit
    for unit in "${DIAGNOSTIC_UNITS[@]}"; do
        printf '===== systemctl status %s (guest) =====\n' "$unit"
        if ! guest_sudo systemctl status --no-pager --full "$unit" 2>&1; then
            printf 'diagnostics: systemctl status %s did not complete in the guest\n' "$unit"
        fi

        printf '===== journalctl -u %s (guest, last %s lines) =====\n' "$unit" "$DIAGNOSTIC_JOURNAL_LINES"
        if ! guest_sudo journalctl --no-pager --lines "$DIAGNOSTIC_JOURNAL_LINES" -u "$unit" 2>&1; then
            printf 'diagnostics: journalctl -u %s did not complete in the guest\n' "$unit"
        fi
    done
}

# dump_pre_reboot_diagnostics prints both units' state BEFORE the guest is
# rebooted, unconditionally and on a run that is still healthy. It is not a
# failure path: a guest that never comes back from the reboot cannot be asked
# anything afterwards, and the host-side console log alone would leave the
# pre-reboot unit state unrecorded. Dumping it while the guest is still there
# is what makes that case diagnosable.
dump_pre_reboot_diagnostics() {
    diagnostics_log "dumping both units' state before the reboot is issued, so a guest that never returns still leaves evidence of what it looked like"
    dump_guest_unit_diagnostics
}

# dump_failure_diagnostics is the whole discriminator. It is a no-op on a
# successful exit and runs at most once.
dump_failure_diagnostics() {
    local status="${1:-1}"

    [ "$status" -ne 0 ] || return 0
    [ "$DIAGNOSTICS_DUMPED" -eq 0 ] || return 0
    DIAGNOSTICS_DUMPED=1

    diagnostics_log "the run failed with status $status; collecting diagnostics"

    if guest_is_reachable_now; then
        diagnostics_log "the guest answers ssh now: collecting its unit state and journals"
        dump_guest_unit_diagnostics
    else
        diagnostics_log "the guest does not answer ssh now: collecting the host-side qemu stderr and serial console logs"
        dump_boot_diagnostics
    fi
}

# diagnostics_on_exit collects first and stops the guest afterwards. The order
# is load-bearing: a dump taken from a guest that has already been killed is
# not a dump.
diagnostics_on_exit() {
    dump_failure_diagnostics "$1"
    stop_vm
}

# install_failure_diagnostics arms both traps. errtrace is enabled so the ERR
# trap fires from inside functions too; the EXIT trap is what catches the
# harness's own `exit 1` failure paths, and DIAGNOSTICS_DUMPED keeps the two
# from dumping twice.
install_failure_diagnostics() {
    set -E
    trap 'dump_failure_diagnostics $?' ERR
    trap 'diagnostics_on_exit $?' EXIT

    diagnostics_log "failure diagnostics armed: unit state from a reachable guest, host-side logs otherwise"
}
