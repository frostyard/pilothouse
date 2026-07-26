#!/bin/sh
set -eu
# shellcheck source=test/vm/guest/lib.sh disable=SC1090,SC1091
. "$(dirname "$0")/lib.sh"
require_root

# capture-pre-reboot.sh — record, on the running guest, everything the
# post-reboot half needs to compare against, and plant the discriminator that
# makes `/run/pilothouse` was DESTROYED provable.
#
# Invoked by the orchestrator, and only by the orchestrator, as:
#
#     sudo -n sh ~/vm-boot/guest/capture-pre-reboot.sh
#
# It records four things, and asserts what it can already assert:
#
#   1. the CAPABILITY SET, obtained through the direct authenticated broker
#      route (broker_login over the Unix socket, then broker_query) — never
#      inferred from the web console's rendered HTML, which is a view of the
#      set rather than the set. A query that fails, or that answers an EMPTY
#      set, fails here: an empty set recorded now would later be compared
#      against an empty set and pass while proving nothing;
#   2. a SENTINEL file created inside /run/pilothouse. This is the sole
#      mandatory discriminator for the runtime directory's destruction: a
#      directory that survived a reboot still carries its contents, and one
#      systemd recreated from RuntimeDirectory= cannot. Its path is recorded
#      so the post-reboot half asserts the absence of the exact file this
#      script created;
#   3. /run/pilothouse's own INODE, which is DIAGNOSTIC CONTEXT ONLY. /run is a
#      fresh tmpfs on every boot and its inode counter restarts, so correct
#      behaviour can legitimately reuse the same number; requiring it to DIFFER
#      would be an assertion that fails on correct behaviour, and it is
#      therefore forbidden. It is recorded so a failure can be diagnosed, and
#      it takes part in no pass/fail decision on either side of the reboot;
#   4. /var/lib/pilothouse/audit.db's INODE, which IS asserted after the
#      reboot — for equality. That is the sound direction of the same
#      instrument: the state directory lives on the guest's ordinary
#      persistent root filesystem, where an inode number is a durable
#      identity, and StateDirectory= means it must survive. No pre-reboot
#      audit RECORD is required to be readable; only the database file's
#      identity is captured.
#
# The state file is written into the administrator-writable staging directory
# and chowned to the administrator account, so the orchestrator can retrieve it
# as its one login identity with no escalation. It carries NO credential: a
# capability id list, a sentinel path, two inode numbers, three ownership
# triples and the boot id.

# CAPABILITY_QUERY_ID is the broker's capability query, spelled exactly as
# internal/broker/api.go declares it (QueryCapabilities). Its result is
# capability.Set's own wire shape, {"capabilities": [...]}, already sorted and
# present-only.
CAPABILITY_QUERY_ID="org.frostyard.pilothouse.capabilities.list"

# RUNTIME_DIRECTORY is the broker unit's RuntimeDirectory= — destroyed when the
# unit stops and recreated when it starts. STATE_DIRECTORY is its
# StateDirectory=, which persists, and AUDIT_DATABASE is the durable action
# audit database the daemon opens inside it (cmd/pilothoused/main.go's
# -audit-db default).
RUNTIME_DIRECTORY="/run/pilothouse"
STATE_DIRECTORY="/var/lib/pilothouse"
AUDIT_DATABASE="/var/lib/pilothouse/audit.db"

# SENTINEL_PATH is the file whose ABSENCE after the reboot proves the runtime
# directory was destroyed rather than merely left looking correct. The leading
# dot keeps it out of the way of anything the daemon lists.
SENTINEL_PATH="/run/pilothouse/.vm-boot-sentinel"

# PRE_REBOOT_STATE_BASENAME is the file this script writes inside the staging
# directory. check-reboot-posture.sh reads it back from the same place — the
# staging directory is on the guest's persistent disk, so it survives the
# reboot — and the orchestrator retrieves a copy for the job log.
PRE_REBOOT_STATE_BASENAME="pre-reboot-state.env"

STAGING_DIRECTORY="$(dirname "$0")/.."
PRE_REBOOT_STATE_FILE="$STAGING_DIRECTORY/$PRE_REBOOT_STATE_BASENAME"

WORK_DIR="$(mktemp -d)"
chmod 0700 "$WORK_DIR"
trap 'rm -rf "$WORK_DIR"' EXIT

load_credentials

# The direct authenticated broker route, the same one check-pam.sh and
# check-journal.sh use: login over the Unix socket, then the query with the
# returned bearer token. The capability set is taken from the BROKER, not from
# anything the web console rendered.
# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_USERNAME="$PH_ADMIN_USER"
# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_PASSWORD="$PH_ADMIN_PASSWORD"
broker_login

guest_log "querying $CAPABILITY_QUERY_ID over the broker socket before the reboot"

capability_response="$WORK_DIR/capabilities.json"
broker_query "$CAPABILITY_QUERY_ID" >"$capability_response"

# The array's TYPE is checked before its length: `.capabilities | length` on a
# missing field answers 0 rather than failing, which would turn "the response
# had no capabilities array at all" into "the set was empty" and report the
# wrong assertion. A non-array answers null here, which jq -e reports as a
# failure.
capability_count="$(jq -e 'if (.capabilities | type) == "array" then (.capabilities | length) else null end' <"$capability_response")" ||
    fail "the pre-reboot $CAPABILITY_QUERY_ID response carries no capabilities array; a capability set that cannot be read before the reboot is a failure of this check and is never recorded as empty"

[ "$capability_count" -gt 0 ] ||
    fail "the pre-reboot $CAPABILITY_QUERY_ID query returned an EMPTY capability set; recording it would let an equally empty post-reboot answer compare equal and prove nothing"

# The ids are sorted here so the comparison after the reboot is on a stable
# normalisation and can never fail on ordering alone.
capability_ids="$(jq -er '.capabilities | sort | join(" ")' <"$capability_response")" ||
    fail "could not normalise the pre-reboot capability ids from the $CAPABILITY_QUERY_ID response"

[ -n "$capability_ids" ] ||
    fail "the normalised pre-reboot capability id list is empty; an empty list is a failure, not a baseline"

guest_log "pre-reboot capability set ($capability_count): $capability_ids"

# The two directories are asserted NOW as well, so a posture that was already
# wrong before the reboot fails here rather than being blamed on the reboot.
guest_log "asserting the systemd-created directories before the reboot"
expect_owner_mode "$RUNTIME_DIRECTORY" root pilothouse 750
expect_owner_mode "$STATE_DIRECTORY" root pilothouse 750

[ -f "$AUDIT_DATABASE" ] ||
    fail "$AUDIT_DATABASE does not exist before the reboot; the daemon opens it inside its StateDirectory= and its identity is what proves that directory persisted"

# The sentinel. Created as root inside the 0750 root:pilothouse runtime
# directory, and readable by nobody else.
: >"$SENTINEL_PATH" ||
    fail "could not create the sentinel $SENTINEL_PATH inside $RUNTIME_DIRECTORY"
chmod 0600 "$SENTINEL_PATH" ||
    fail "could not set the mode of the sentinel $SENTINEL_PATH"

[ -f "$SENTINEL_PATH" ] ||
    fail "the sentinel $SENTINEL_PATH was not created; without it the post-reboot half cannot tell a recreated $RUNTIME_DIRECTORY from a surviving one"

guest_log "created the sentinel $SENTINEL_PATH inside $RUNTIME_DIRECTORY"

# DIAGNOSTIC CONTEXT ONLY. Recorded, printed, compared with nothing. The read
# itself is deliberately not an assertion either: a number that decides nothing
# must not be able to fail the run, so an unreadable inode is recorded as such
# and the posture assertions above and after it stand on their own.
runtime_directory_inode="unavailable"
if [ -d "$RUNTIME_DIRECTORY" ]; then
    runtime_directory_inode="$(stat -c '%i' "$RUNTIME_DIRECTORY")" ||
        runtime_directory_inode="unreadable"
fi

audit_database_inode="$(stat -c '%i' "$AUDIT_DATABASE")" ||
    fail "could not read the inode of $AUDIT_DATABASE"

runtime_directory_ownership="$(stat -c '%U %G %a' "$RUNTIME_DIRECTORY")" ||
    fail "could not read the ownership of $RUNTIME_DIRECTORY"
state_directory_ownership="$(stat -c '%U %G %a' "$STATE_DIRECTORY")" ||
    fail "could not read the ownership of $STATE_DIRECTORY"

boot_id="$(cat /proc/sys/kernel/random/boot_id)" ||
    fail "could not read the guest's boot id before the reboot"

# The state file is a plain set of shell assignments so the post-reboot half
# can source it. Every value below is an identifier, a path, a number or an
# ownership triple; none of them is a credential, and the file is deliberately
# readable by the administrator account that retrieves it.
cat >"$PRE_REBOOT_STATE_FILE" <<STATE
# Written by capture-pre-reboot.sh before the guest was rebooted. It carries no
# credential. PRE_REBOOT_RUNTIME_DIRECTORY_INODE is DIAGNOSTIC CONTEXT ONLY:
# /run is a per-boot tmpfs whose inode numbers may legitimately repeat, so it
# takes part in no pass/fail decision.
PRE_REBOOT_BOOT_ID='$boot_id'
PRE_REBOOT_CAPABILITY_COUNT='$capability_count'
PRE_REBOOT_CAPABILITY_IDS='$capability_ids'
PRE_REBOOT_SENTINEL_PATH='$SENTINEL_PATH'
PRE_REBOOT_RUNTIME_DIRECTORY_INODE='$runtime_directory_inode'
PRE_REBOOT_RUNTIME_DIRECTORY_OWNERSHIP='$runtime_directory_ownership'
PRE_REBOOT_AUDIT_DATABASE_INODE='$audit_database_inode'
PRE_REBOOT_STATE_DIRECTORY_OWNERSHIP='$state_directory_ownership'
STATE

chmod 0644 "$PRE_REBOOT_STATE_FILE" ||
    fail "could not set the mode of $PRE_REBOOT_STATE_FILE"

# Chowned to the administrator account, which is the harness's one login
# identity: the orchestrator retrieves this file as that account, with no
# escalation, exactly as it stages every other file in this directory.
chown "$PH_ADMIN_USER" "$PRE_REBOOT_STATE_FILE" ||
    fail "could not give $PRE_REBOOT_STATE_FILE to $PH_ADMIN_USER; the orchestrator retrieves it as that account and cannot escalate to read it"

guest_log "wrote the pre-reboot state to $PRE_REBOOT_STATE_FILE (owner $PH_ADMIN_USER, no credential)"
guest_log "runtime directory inode $runtime_directory_inode recorded as diagnostic context only; audit database inode $audit_database_inode recorded for the persistence assertion"
