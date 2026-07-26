#!/bin/sh
set -eu
# shellcheck source=test/vm/guest/lib.sh disable=SC1090,SC1091
. "$(dirname "$0")/lib.sh"
require_root

# check-journal.sh — prove the daemon built with the `sdjournal` tag reads the
# journal BACK on a booted host, through the journal-backed read surface the
# broker actually exposes.
#
# Invoked by the orchestrator, and only by the orchestrator, as:
#
#     sudo -n sh ~/vm-boot/guest/check-journal.sh
#
# The evidence is the BROKER's response body, and nothing else. Finding the
# daemon's startup line in `journalctl` output would prove that systemd logged
# it, not that the daemon can read it back through its own journald reader, so
# this script never reads the journal itself: it authenticates over the direct
# socket route (guest/lib.sh's broker_login + broker_query, the same route
# check-pam.sh uses), runs the journal query for the broker's own unit, and
# asserts the record comes back in the query's `entries`.
#
# Every identifier below is grounded in the daemon's own source rather than
# assumed:
#
#   * JOURNAL_QUERY_ID is `QueryServicesJournal` in internal/broker/api.go;
#   * JOURNAL_UNIT_PARAMETER is the key its handler reads in
#     cmd/pilothoused/main.go (`manager.Journal(ctx, parameters["unit"])`);
#   * the response shape — `{unit, description, entries:[{timestamp, priority,
#     severity, message, unit}]}` — is services.Journal / services.JournalEntry
#     in internal/modules/services/manager.go;
#   * DAEMON_STARTUP_MESSAGE is the line cmd/pilothoused/main.go logs once the
#     privileged broker is listening. The daemon has been running since
#     check-activation.sh started it, comfortably inside the reader's one-hour
#     window (`journalWindow`), and the reader walks that window forwards from
#     its start, so the startup record is among the first of the 200 entries
#     the query is allowed to return.
#
# The query is registered behind the Systemd AND Journald capabilities, so a
# daemon whose journald reader is the header-free stub does not answer it at
# all — which is exactly why this check is a read-back and not a string match.

BROKER_UNIT="pilothoused.service"

# JOURNAL_QUERY_ID and JOURNAL_UNIT_PARAMETER are the wire contract, spelled
# exactly as internal/broker/api.go declares it and as the handler in
# cmd/pilothoused/main.go reads it.
JOURNAL_QUERY_ID="org.frostyard.pilothouse.services.journal"
JOURNAL_UNIT_PARAMETER="unit"

# DAEMON_STARTUP_MESSAGE is a line the DAEMON ITSELF emitted, not one this
# harness planted: pilothoused logs it on the successful listen. A record the
# harness wrote would prove only that something reached the journal.
DAEMON_STARTUP_MESSAGE="privileged broker listening"

WORK_DIR="$(mktemp -d)"
chmod 0700 "$WORK_DIR"
trap 'rm -rf "$WORK_DIR"' EXIT

load_credentials

# The authenticated direct socket route. The journal query is not registered
# for unauthenticated callers, and check-activation.sh already proved that an
# unauthenticated query answers 401, so a session is required here.
# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_USERNAME="$PH_ADMIN_USER"
# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_PASSWORD="$PH_ADMIN_PASSWORD"
broker_login

guest_log "querying $JOURNAL_QUERY_ID for $BROKER_UNIT over the broker socket"

# The parameter object is built by jq from the environment, so the parameter
# NAME is the one constant above and the value is never spliced into a JSON
# string by hand.
journal_parameters="$(
    JOURNAL_UNIT="$BROKER_UNIT" jq -nc --arg name "$JOURNAL_UNIT_PARAMETER" '{($name): env.JOURNAL_UNIT}'
)" || fail "could not build the $JOURNAL_QUERY_ID request parameters"

# broker_query POSTs /v1/queries/{id} with the session token and fails by name
# unless the broker answered EXACTLY 200; it is called with a redirection and
# never through a command substitution, so its session variables survive.
journal_response="$WORK_DIR/journal.json"
broker_query "$JOURNAL_QUERY_ID" "$journal_parameters" >"$journal_response"

guest_log "$JOURNAL_QUERY_ID answered 200 over the broker socket"

answered_unit="$(jq -er '.unit' <"$journal_response")" ||
    fail "the $JOURNAL_QUERY_ID response carries no unit field"

[ "$answered_unit" = "$BROKER_UNIT" ] ||
    fail "the $JOURNAL_QUERY_ID response is about $answered_unit, expected $BROKER_UNIT"

jq -e '.entries | type == "array"' <"$journal_response" >/dev/null ||
    fail "the $JOURNAL_QUERY_ID response carries no entries array"

entry_count="$(jq -e '.entries | length' <"$journal_response")" ||
    fail "could not count the entries in the $JOURNAL_QUERY_ID response"

[ "$entry_count" -gt 0 ] ||
    fail "the $JOURNAL_QUERY_ID response carried 0 entries for $BROKER_UNIT: the query answered, but the daemon's journald reader read nothing back"

# THE assertion of this check: an entry the BROKER returned carries the line
# the daemon itself emitted. The match is on the parsed `message` field of a
# returned entry — not on a line of any log this script read for itself.
startup_hits="$(
    jq --arg message "$DAEMON_STARTUP_MESSAGE" \
        '[.entries[] | select((.message // "") | contains($message))] | length' \
        <"$journal_response"
)" || fail "could not search the $JOURNAL_QUERY_ID response's entries for the daemon's own startup record"

[ "$startup_hits" != "0" ] ||
    fail "none of the $entry_count entries the $JOURNAL_QUERY_ID query returned for $BROKER_UNIT has a message containing '$DAEMON_STARTUP_MESSAGE': the daemon cannot read its own line back, which is the whole claim of this check"

# The unit each matching entry declares comes from the record's own
# _SYSTEMD_UNIT field, so this is the returned record's provenance rather than
# a restatement of the query's parameter.
startup_units="$(
    jq -r --arg message "$DAEMON_STARTUP_MESSAGE" \
        '[.entries[] | select((.message // "") | contains($message)) | .unit] | unique | join(",")' \
        <"$journal_response"
)" || fail "could not read the unit of the matching entries in the $JOURNAL_QUERY_ID response"

[ "$startup_units" = "$BROKER_UNIT" ] ||
    fail "the matching entries name unit(s) '$startup_units', expected only $BROKER_UNIT"

guest_log "the broker returned $startup_hits entr(y/ies) carrying '$DAEMON_STARTUP_MESSAGE' out of $entry_count: the daemon read its own line back through its journald reader"
