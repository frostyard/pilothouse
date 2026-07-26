#!/bin/sh
set -eu
# shellcheck source=test/vm/guest/lib.sh disable=SC1090,SC1091
. "$(dirname "$0")/lib.sh"
require_root

# check-activation.sh — enable and start both units on the booted guest, then
# assert the state only a real systemd on a real host can produce.
#
# Invoked by the orchestrator, and only by the orchestrator, as:
#
#     sudo -n sh ~/vm-boot/guest/check-activation.sh
#
# Explicit interpreter and explicit escalation: `systemctl enable --now` and
# opening the 0660 root:pilothouse broker socket both need root, and
# require_root above proves the escalation actually happened.
#
# The units are ENABLED AND STARTED here rather than asserted to be running:
# packaging/postinstall.sh contains no `systemctl` call, so installing the
# package deliberately starts nothing, and asserting that the units are already
# active immediately after install would assert a behaviour the packaging does
# not have.
#
# Nothing here touches the guest's SELinux mode. The Fedora guest boots
# enforcing and stays that way, so activation happens under enforcement exactly
# as it would on an administrator's host; the audit-log posture is #80's.

# UNIT_ACTIVATION_TIMEOUT_SECONDS is the one bounded wait this script uses: for
# each unit to report `active`, and for the broker to publish its socket. It is
# named rather than inlined so the timeout is stated once and reported by that
# same number when it expires.
UNIT_ACTIVATION_TIMEOUT_SECONDS=90

# BROKER_PROBE_TIMEOUT_SECONDS bounds the liveness request itself. A socket that
# accepts a connection and then never answers is exactly the stale-broker case
# this probe exists to catch, so the request must time out and fail rather than
# hang the run until the job's own limit.
BROKER_PROBE_TIMEOUT_SECONDS=15

# CAPABILITY_QUERY_ID is the broker's capability query, spelled exactly as
# internal/broker/api.go declares it (QueryCapabilities).
CAPABILITY_QUERY_ID="org.frostyard.pilothouse.capabilities.list"

BROKER_UNIT="pilothoused.service"
WEB_UNIT="pilothouse.service"

# dump_unit_diagnostics prints ONE unit's own status and journal. Both units
# log JSON to stdout and neither overrides StandardOutput=, so each process's
# records land in its own unit's journal: a dump that named the other unit
# would be vacuous. Neither command's failure may abort the dump before it has
# printed anything — `systemctl status` exits non-zero for an inactive unit,
# which is precisely the case being diagnosed — so each is reported by name
# instead of aborting or being silently discarded.
dump_unit_diagnostics() {
    dump_unit="$1"

    guest_log "--- systemctl status $dump_unit ---"
    systemctl status "$dump_unit" --no-pager --full ||
        guest_log "systemctl status $dump_unit exited non-zero"

    guest_log "--- journalctl -u $dump_unit ---"
    journalctl --unit "$dump_unit" --no-pager --lines=200 ||
        guest_log "journalctl -u $dump_unit exited non-zero"
}

# enable_and_wait_for_unit enables and starts one unit and waits for it to
# reach `active`. `systemctl is-active --quiet` is a status test, not output to
# be parsed: it exits zero only when the unit is active, so a not-yet-active
# unit is distinguished from a missing one by the failure path below, which
# prints that unit's own status and journal before failing by name.
enable_and_wait_for_unit() {
    unit="$1"

    guest_log "enabling and starting $unit"
    if ! systemctl enable --now "$unit"; then
        dump_unit_diagnostics "$unit"
        fail "systemctl enable --now $unit failed; the package installs the unit but deliberately does not start it, so this is the first start"
    fi

    waited=0
    while [ "$waited" -lt "$UNIT_ACTIVATION_TIMEOUT_SECONDS" ]; do
        if systemctl is-active --quiet "$unit"; then
            guest_log "$unit is active after ${waited}s"
            return 0
        fi

        sleep 1
        waited=$((waited + 1))
    done

    dump_unit_diagnostics "$unit"
    fail "$unit did not reach active within ${UNIT_ACTIVATION_TIMEOUT_SECONDS}s"
}

# Requirement 2: both units enable, start and reach active within the bounded
# wait. The broker first, because the web unit Requires= it.
enable_and_wait_for_unit "$BROKER_UNIT"
enable_and_wait_for_unit "$WEB_UNIT"

# Requirement 3: the two directories systemd creates from the broker unit's
# RuntimeDirectory=/StateDirectory= with RuntimeDirectoryMode=/StateDirectoryMode=
# 0750, under User=root Group=pilothouse. This is the check no container can
# make: there is no systemd there to create them, which is why #77 asserts
# nothing about them and why they are asserted here instead.
#
# expect_owner_mode compares `stat -c '%U %G %a'`, and %a prints the mode
# without a leading zero, so mode 0750 is written 750 and 0660 is written 660.
guest_log "asserting the systemd-created directories"
expect_owner_mode /run/pilothouse root pilothouse 750
expect_owner_mode /var/lib/pilothouse root pilothouse 750

# Requirement 4, first half: the socket exists with the mode that lets the
# unprivileged web process reach the broker and nothing else. Type=simple marks
# the unit active as soon as the process is executed, so the listener may not
# have been published at that instant; the wait uses the same named timeout and
# reports the broker's own diagnostics when it expires.
waited=0
while [ ! -S "$BROKER_SOCKET" ] && [ "$waited" -lt "$UNIT_ACTIVATION_TIMEOUT_SECONDS" ]; do
    sleep 1
    waited=$((waited + 1))
done

if [ ! -S "$BROKER_SOCKET" ]; then
    dump_unit_diagnostics "$BROKER_UNIT"
    fail "$BROKER_SOCKET was not created within ${UNIT_ACTIVATION_TIMEOUT_SECONDS}s of $BROKER_UNIT becoming active"
fi

guest_log "asserting the broker socket"
expect_owner_mode "$BROKER_SOCKET" root pilothouse 660

# Requirement 4, second half: prove a live HTTP server is answering on that
# socket, not that a socket file exists. The request is deliberately
# UNAUTHENTICATED: every broker query runs authorize() first
# (internal/broker/server.go), so the only correct answer is 401 with a JSON
# error body. That is a stronger liveness proof than a capability list would be
# here — it can only come from a server that accepted the connection and parsed
# the request, while a connection refusal, a stale socket file or a hang all
# fail. The authenticated capability list needs a session token and belongs
# with the authenticated flows, in a later check.
probe_body="$(mktemp)"
trap 'rm -f "$probe_body"' EXIT

guest_log "probing the broker with an unauthenticated $CAPABILITY_QUERY_ID query"

probe_status="$(
    broker_curl "/v1/queries/$CAPABILITY_QUERY_ID" \
        --request POST \
        --header 'Content-Type: application/json' \
        --data '{}' \
        --max-time "$BROKER_PROBE_TIMEOUT_SECONDS" \
        --output "$probe_body" \
        --write-out '%{http_code}'
)" ||
    fail "the unauthenticated $CAPABILITY_QUERY_ID request to $BROKER_SOCKET did not complete within ${BROKER_PROBE_TIMEOUT_SECONDS}s: a refused connection or a hang means no live broker is answering on the socket"

[ "$probe_status" = "401" ] ||
    fail "the unauthenticated $CAPABILITY_QUERY_ID query returned HTTP $probe_status, expected exactly 401: every broker query authorizes first, so an unauthenticated caller must be rejected and must never receive a capability list"

probe_error="$(jq -er '.error' <"$probe_body")" ||
    fail "the 401 response to the unauthenticated $CAPABILITY_QUERY_ID query carries no JSON error field; the broker answers rejections with a JSON body"

[ -n "$probe_error" ] ||
    fail "the 401 response to the unauthenticated $CAPABILITY_QUERY_ID query carries an empty JSON error field"

guest_log "the broker rejected the unauthenticated query with 401: $probe_error"
guest_log "both units are active, the systemd-created directories and the broker socket are correct, and the broker is live"
