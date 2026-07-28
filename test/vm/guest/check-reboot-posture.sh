#!/bin/sh
set -eu
# shellcheck source=test/vm/guest/lib.sh disable=SC1090,SC1091
. "$(dirname "$0")/lib.sh"
require_root

# check-reboot-posture.sh — the post-reboot half. It asserts what a real
# systemd on a real host must have done unaided while the machine restarted.
#
# Invoked by the orchestrator, and only by the orchestrator, as:
#
#     sudo -n sh ~/vm-boot/guest/check-reboot-posture.sh
#
# NOTHING here enables or starts a unit. Both units were enabled once, before
# the reboot, by check-activation.sh; the claim of this check is that they came
# back BY THEMSELVES, so any `systemctl` enable-or-start verb anywhere in this
# file would be the check manufacturing the state it is asserting — a guard in
# packaging/vm_harness_test.go scans this file, comments included, for exactly
# that. The bounded wait below is a wait for systemd to finish, not an
# intervention.
#
# The four assertions, in order:
#
#   1. both units are active again, unaided;
#   2. the capability set, obtained through the SAME direct authenticated
#      broker route the pre-reboot half used, is identical to the recorded one
#      after the same stable normalisation (sorted ids). A query that fails or
#      answers an EMPTY set fails by name, so an empty answer can never be
#      compared equal against another empty answer;
#   3. /run/pilothouse was DESTROYED AND RECREATED. One assertion group, two
#      halves, neither accepted alone: the pre-reboot SENTINEL must be ABSENT
#      (a surviving directory carries its contents) AND the directory must
#      exist again with owner root, group pilothouse, mode 0750 (absence alone
#      would also hold for a directory that never came back). The directory's
#      INODE is printed as diagnostic context and asserted NOWHERE: /run is a
#      fresh tmpfs each boot whose inode counter restarts, so requiring the
#      number to DIFFER could fail on correct behaviour, and requiring it to
#      match would be wrong outright;
#   4. /var/lib/pilothouse PERSISTED: same owner, group and mode, and
#      audit.db's INODE UNCHANGED. This is the only inode assertion in the
#      harness, and it is an equality on persistent on-disk storage, where an
#      inode number is a durable identity. No pre-reboot audit RECORD is
#      required to be readable.

# CAPABILITY_QUERY_ID is the broker's capability query, spelled exactly as
# internal/broker/api.go declares it (QueryCapabilities).
CAPABILITY_QUERY_ID="org.frostyard.pilothouse.capabilities.list"

RUNTIME_DIRECTORY="/run/pilothouse"
STATE_DIRECTORY="/var/lib/pilothouse"
AUDIT_DATABASE="/var/lib/pilothouse/audit.db"

BROKER_UNIT="pilothoused.service"
WEB_UNIT="pilothouse.service"

# PRE_REBOOT_STATE_BASENAME is the file capture-pre-reboot.sh wrote into the
# staging directory before the reboot. The staging directory is on the guest's
# persistent disk, so the file is still there.
PRE_REBOOT_STATE_BASENAME="pre-reboot-state.env"

STAGING_DIRECTORY="$(dirname "$0")/.."
PRE_REBOOT_STATE_FILE="$STAGING_DIRECTORY/$PRE_REBOOT_STATE_BASENAME"

# UNIT_ACTIVATION_TIMEOUT_SECONDS bounds the wait for each unit to report
# active after the boot. sshd answering does not mean the whole boot transaction
# has settled, so a unit that is still starting must not be read as a failure —
# and one that never starts must fail by name rather than hang.
UNIT_ACTIVATION_TIMEOUT_SECONDS=120

dump_unit_diagnostics() {
    dump_unit="$1"

    guest_log "--- systemctl status $dump_unit ---"
    systemctl status "$dump_unit" --no-pager --full ||
        guest_log "systemctl status $dump_unit exited non-zero"

    guest_log "--- journalctl -u $dump_unit ---"
    journalctl --unit "$dump_unit" --no-pager --lines=200 ||
        guest_log "journalctl -u $dump_unit exited non-zero"
}

# wait_for_unit_active waits for ONE unit to report active. `is-active --quiet`
# is a status test, not output to be parsed. No unit is enabled or started
# here: this waits for what systemd is already doing.
wait_for_unit_active() {
    unit="$1"

    waited=0
    while [ "$waited" -lt "$UNIT_ACTIVATION_TIMEOUT_SECONDS" ]; do
        if systemctl is-active --quiet "$unit"; then
            guest_log "$unit is active again ${waited}s after the reboot, with no manual intervention"
            return 0
        fi

        sleep 1
        waited=$((waited + 1))
    done

    dump_unit_diagnostics "$unit"
    fail "$unit did not return to active within ${UNIT_ACTIVATION_TIMEOUT_SECONDS}s of the reboot; it was enabled before the reboot and nothing enabled or started it afterwards, which is the whole claim of this check"
}

WORK_DIR="$(mktemp -d)"
chmod 0700 "$WORK_DIR"
trap 'rm -rf "$WORK_DIR"' EXIT

[ -f "$PRE_REBOOT_STATE_FILE" ] ||
    fail "$PRE_REBOOT_STATE_FILE is missing after the reboot; capture-pre-reboot.sh writes it into the staging directory before the reboot and there is nothing to compare against without it"

# shellcheck disable=SC1090,SC1091 # written by capture-pre-reboot.sh at run time
. "$PRE_REBOOT_STATE_FILE"

# Each recorded value is required by name. A state file missing one of them is
# an incomplete capture, and comparing against an empty string would be the
# empty-versus-empty pass this check exists to rule out.
[ -n "${PRE_REBOOT_BOOT_ID-}" ] ||
    fail "$PRE_REBOOT_STATE_FILE declares no PRE_REBOOT_BOOT_ID"
[ -n "${PRE_REBOOT_CAPABILITY_IDS-}" ] ||
    fail "$PRE_REBOOT_STATE_FILE declares no PRE_REBOOT_CAPABILITY_IDS; there is no recorded capability set to compare against"
[ -n "${PRE_REBOOT_SENTINEL_PATH-}" ] ||
    fail "$PRE_REBOOT_STATE_FILE declares no PRE_REBOOT_SENTINEL_PATH; without the sentinel's path nothing distinguishes a recreated $RUNTIME_DIRECTORY from a surviving one"
[ -n "${PRE_REBOOT_AUDIT_DATABASE_INODE-}" ] ||
    fail "$PRE_REBOOT_STATE_FILE declares no PRE_REBOOT_AUDIT_DATABASE_INODE; there is no recorded identity for $AUDIT_DATABASE to compare against"

# PRE_REBOOT_RUNTIME_DIRECTORY_INODE is deliberately absent from the list
# above. It is diagnostic context, and a value that decides nothing must not be
# able to fail the run either — not even by being missing.

# The reboot really happened. The orchestrator's reboot_guest already proved
# this host-side by waiting until SSH could read a changed boot id; asserting
# it again from inside the guest means this script cannot be green against the
# machine that captured the state.
boot_id="$(cat /proc/sys/kernel/random/boot_id)" ||
    fail "could not read the guest's boot id after the reboot"

[ "$boot_id" != "$PRE_REBOOT_BOOT_ID" ] ||
    fail "the boot id is unchanged ($boot_id): this check is running on the same boot that captured the pre-reboot state, so nothing below would be about a reboot at all"

# Assertion 1: both units are active again, with nothing enabled or started
# here. The broker first, because the web unit Requires= it.
wait_for_unit_active "$BROKER_UNIT"
wait_for_unit_active "$WEB_UNIT"

# Assertion 2: the capability set, through the same direct authenticated broker
# route, is identical to the one recorded before the reboot.
load_credentials

# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_USERNAME="$PH_ADMIN_USER"
# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_PASSWORD="$PH_ADMIN_PASSWORD"
broker_login

guest_log "querying $CAPABILITY_QUERY_ID over the broker socket after the reboot"

capability_response="$WORK_DIR/capabilities.json"
broker_query "$CAPABILITY_QUERY_ID" >"$capability_response"

# The array's TYPE is checked before its length, for the same reason as in
# capture-pre-reboot.sh: `.capabilities | length` on a missing field answers 0
# rather than failing, which would report a broken response as an empty set.
capability_count="$(jq -e 'if (.capabilities | type) == "array" then (.capabilities | length) else null end' <"$capability_response")" ||
    fail "the post-reboot $CAPABILITY_QUERY_ID response carries no capabilities array; a capability set that cannot be read after the reboot is a failure of this check and is never treated as an empty set"

[ "$capability_count" -gt 0 ] ||
    fail "the post-reboot $CAPABILITY_QUERY_ID query returned an EMPTY capability set; an empty answer is a failure and must never be compared equal against another empty answer"

capability_ids="$(jq -er '.capabilities | sort | join(" ")' <"$capability_response")" ||
    fail "could not normalise the post-reboot capability ids from the $CAPABILITY_QUERY_ID response"

[ -n "$capability_ids" ] ||
    fail "the normalised post-reboot capability id list is empty; an empty list is a failure, not a match"

[ "$capability_ids" = "$PRE_REBOOT_CAPABILITY_IDS" ] ||
    fail "the capability set changed across the reboot: before it was '$PRE_REBOOT_CAPABILITY_IDS' and now it is '$capability_ids'"

guest_log "the capability set is identical across the reboot ($capability_count): $capability_ids"

# Assertion 3: /run/pilothouse was DESTROYED AND RECREATED. Both halves, as one
# group. The inode is printed first, as context for whichever half fails, and
# is compared with nothing.
post_reboot_runtime_directory_inode="unavailable"
if [ -d "$RUNTIME_DIRECTORY" ]; then
    post_reboot_runtime_directory_inode="$(stat -c '%i' "$RUNTIME_DIRECTORY")" ||
        post_reboot_runtime_directory_inode="unreadable"
fi

guest_log "diagnostic context only: $RUNTIME_DIRECTORY inode was ${PRE_REBOOT_RUNTIME_DIRECTORY_INODE-unrecorded} before the reboot and is $post_reboot_runtime_directory_inode now; /run is a per-boot tmpfs whose inode numbers may legitimately repeat, so this number is asserted nowhere"

[ ! -e "$PRE_REBOOT_SENTINEL_PATH" ] ||
    fail "the pre-reboot sentinel $PRE_REBOOT_SENTINEL_PATH still exists after the reboot: $RUNTIME_DIRECTORY survived instead of being destroyed and recreated from the unit's RuntimeDirectory="

[ -d "$RUNTIME_DIRECTORY" ] ||
    fail "$RUNTIME_DIRECTORY does not exist after the reboot: the sentinel is gone, but systemd never recreated the runtime directory from the unit's RuntimeDirectory="

expect_owner_mode "$RUNTIME_DIRECTORY" root pilothouse 750

guest_log "$RUNTIME_DIRECTORY was destroyed and recreated: the sentinel is gone and the directory is back as root:pilothouse 0750"

# Assertion 4: /var/lib/pilothouse PERSISTED, proven by the sound direction of
# the inode instrument on the guest's persistent root filesystem.
expect_owner_mode "$STATE_DIRECTORY" root pilothouse 750

[ -f "$AUDIT_DATABASE" ] ||
    fail "$AUDIT_DATABASE does not exist after the reboot: the unit's StateDirectory= must persist across a reboot, and the audit database with it"

post_reboot_audit_database_inode="$(stat -c '%i' "$AUDIT_DATABASE")" ||
    fail "could not read the inode of $AUDIT_DATABASE after the reboot"

[ "$post_reboot_audit_database_inode" = "$PRE_REBOOT_AUDIT_DATABASE_INODE" ] ||
    fail "$AUDIT_DATABASE's inode changed across the reboot (was $PRE_REBOOT_AUDIT_DATABASE_INODE, now $post_reboot_audit_database_inode): the file was recreated rather than persisted, so the daemon did not keep its audit trail"

guest_log "$STATE_DIRECTORY persisted: root:pilothouse 0750 and $AUDIT_DATABASE is still inode $post_reboot_audit_database_inode"
guest_log "reboot posture proven: both units active unaided, the capability set unchanged, $RUNTIME_DIRECTORY destroyed and recreated, $STATE_DIRECTORY persisted"
