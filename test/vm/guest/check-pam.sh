#!/bin/sh
set -eu
# shellcheck source=test/vm/guest/lib.sh disable=SC1090,SC1091
. "$(dirname "$0")/lib.sh"
require_root

# check-pam.sh — prove PAM authenticates a real non-root administrator through
# the running stack, on a booted host. This is the reason Layer B exists: no
# container can run pam_authenticate against a live daemon.
#
# Invoked by the orchestrator, and only by the orchestrator, as:
#
#     sudo -n sh ~/vm-boot/guest/check-pam.sh
#
# It CONSUMES the credentials cloud-init delivered and generates NOTHING: the
# administrator account, its password and root's password were all created on
# the host, in the job workspace, and installed here as
# /root/.pilothouse-vm-creds (0600). No credential is printed, and none is ever
# passed to a command as an argument.
#
# What it proves, in order:
#
#   1. The administrator group is the one the INSTALLED unit declares — `sudo`
#      on Debian, `wheel` on Fedora, the single token by which the two package
#      unit files differ — and the cloud-init-created account is in it.
#   2. Three logins against the web console on 127.0.0.1:8888, each asserting
#      the EXACT status: the administrator succeeds (303), the administrator
#      with a wrong password fails (401), and root with its VALID password
#      fails (401). The order matters and a 429 is a failure of this check,
#      never a pass: one failed attempt arms a per-username+remote lockout that
#      answers 429 BEFORE Authenticate is called, so a "any non-success status"
#      assertion would pass without PAM ever being reached.
#   3. Journal evidence for the two claims a status code cannot carry, each
#      bounded by a cursor captured immediately before the request it is about
#      and matched on the record's parsed JSON `msg` field rather than on a
#      substring of the line.
#   4. The administrator's session identity is `admin` over the authenticated
#      direct broker route, so the family's administrator group is proven
#      FUNCTIONALLY and not merely by comparing two strings.
#
# The generated root password is removed at the end.

BROKER_UNIT="pilothoused.service"
WEB_UNIT="pilothouse.service"

# WEB_REQUEST_TIMEOUT_SECONDS bounds every request to the web console, so a
# console that accepts the connection and never answers fails by name instead
# of hanging the run.
WEB_REQUEST_TIMEOUT_SECONDS=30

# JOURNAL_SETTLE_SECONDS is how long a NEGATIVE journal assertion waits before
# reading. journald is asynchronous, so "no such record past this cursor" is
# only evidence once the records that were going to arrive have arrived.
JOURNAL_SETTLE_SECONDS=5

# JOURNAL_RECORD_TIMEOUT_SECONDS bounds a POSITIVE journal assertion: the
# record must appear past its cursor within this many seconds.
JOURNAL_RECORD_TIMEOUT_SECONDS=30

# CAPABILITY_QUERY_ID is the broker's capability query, spelled exactly as
# internal/broker/api.go declares it (QueryCapabilities). It is used here only
# to exercise the second half of the direct route with a real query.
CAPABILITY_QUERY_ID="org.frostyard.pilothouse.capabilities.list"

# REFRESH_CAPABILITIES_MESSAGE is the warning the WEB process logs when the
# capability query it runs on the administrator's behalf immediately after a
# successful login fails. refreshCapabilities swallows that error and the login
# still redirects, so the 303 alone proves nothing about the query: the proof
# is that this record is absent for the login just performed.
REFRESH_CAPABILITIES_MESSAGE="refresh capabilities"

# ROOT_REJECTION_MESSAGE and ROOT_REJECTION_ERROR are the BROKER's record for
# the UID-zero refusal. Both PAM stacks run an account phase after the auth
# phase, and a rejection anywhere in it produces exactly the same 401, so the
# status cannot tell the two apart. This message is emitted only on the Resolve
# path — which runs only after Authenticate returned nil — and this error text
# is unique to the UID-zero branch, so together they prove PAM accepted root's
# password and the application layer refused the login anyway.
ROOT_REJECTION_MESSAGE="authenticated account rejected"
ROOT_REJECTION_ERROR="direct root login is disabled"

WORK_DIR="$(mktemp -d)"
chmod 0700 "$WORK_DIR"
trap 'rm -rf "$WORK_DIR"' EXIT

load_credentials

# write_form_field <path> writes the value in FORM_VALUE to a file, which is
# how every form value reaches curl: `--data-urlencode name@file` keeps the
# value out of the process table and URL-encodes it, so nothing depends on the
# generated alphabet happening to be form-safe. jq is the writer because it
# emits the value verbatim without a trailing newline and without any shell
# echo of it.
write_form_field() {
    FORM_VALUE="$FORM_VALUE" jq -nj 'env.FORM_VALUE' >"$1" ||
        fail "could not stage the login form field $1"

    chmod 0600 "$1"
}

# web_login <csrf-file> <username-file> <password-file> POSTs the login form to
# the web console and prints the HTTP status it answered with. The console is
# the production surface: POST /login requires the csrf field, and there is no
# pre-login cookie to carry, because the session cookie is set only after
# authentication succeeds and the login token is one process-lifetime value.
web_login() {
    web_curl /login \
        --request POST \
        --data-urlencode "csrf@$1" \
        --data-urlencode "username@$2" \
        --data-urlencode "password@$3" \
        --max-time "$WEB_REQUEST_TIMEOUT_SECONDS" \
        --output "$WORK_DIR/login-response.html" \
        --write-out '%{http_code}'
}

# journal_cursor <unit> prints a cursor for that unit's current end. Capturing
# and consuming the cursor with the same unit filter avoids journal
# implementation differences when a cursor from an unrelated stream is later
# combined with `--unit`.
journal_cursor() {
    journalctl --unit "$1" --no-pager --lines=1 --output json >"$WORK_DIR/cursor.json" ||
        fail "could not read $1's journal to capture a cursor"

    journal_cursor_value="$(jq -r '.__CURSOR // empty' <"$WORK_DIR/cursor.json")" ||
        fail "could not read a cursor out of the journal's last record"

    [ -n "$journal_cursor_value" ] ||
        fail "the journal returned no cursor; an unbounded journal search is not evidence about one login"

    printf '%s\n' "$journal_cursor_value"
}

# journal_records_since <unit> <cursor> <out> collects that unit's records past
# that cursor, parsing each line's MESSAGE as the JSON the process actually
# emitted. Every assertion below matches the parsed `msg` field: a substring
# search over the raw line would match the phrase anywhere, including inside an
# unrelated record's error text.
# Neither step is a pipeline: a POSIX sh pipeline reports only its LAST
# command's status, so `journalctl | jq >out || fail` would turn a failed
# journal read into an empty record set and let a negative assertion pass
# vacuously. Each command is run and checked on its own.
journal_records_since() {
    journal_records_raw="$WORK_DIR/journal-raw.json"

    journalctl --unit "$1" --after-cursor "$2" --no-pager --output json >"$journal_records_raw" ||
        fail "could not read $1's journal past the cursor captured for this check"

    jq -c 'select(.MESSAGE | type == "string") | .MESSAGE | (fromjson? // empty) | select(type == "object")' \
        <"$journal_records_raw" >"$3" ||
        fail "could not parse $1's journal records as the JSON the process emitted"
}

# guest_os_id prints the guest's /etc/os-release ID, in a subshell so the
# release file's variables do not leak into this script.
guest_os_id() {
    (
        # shellcheck disable=SC1091 # the guest's own release file
        . /etc/os-release
        printf '%s\n' "${ID:-}"
    )
}

# expected_admin_group prints the administrator group the family's package is
# supposed to declare. It is a per-family branch and never a default: the
# single token by which packaging/deb/pilothoused.service and
# packaging/rpm/pilothoused.service differ is exactly what this check exists to
# prove on a booted host.
expected_admin_group() {
    case "$1" in
        debian) printf '%s\n' 'sudo' ;;
        fedora) printf '%s\n' 'wheel' ;;
        *) fail "unknown guest os ID '$1': this tier covers exactly debian and fedora" ;;
    esac
}

# installed_admin_group prints the --admin-group token from the INSTALLED unit,
# read back through systemctl so it is the file the package put on this guest
# and not a copy from the repository.
installed_admin_group() {
    systemctl cat "$BROKER_UNIT" >"$WORK_DIR/broker-unit.txt" ||
        fail "could not read the installed $BROKER_UNIT through systemctl cat"

    installed_admin_group_value="$(
        sed -n 's/^ExecStart=.*--admin-group \([^ ]*\).*$/\1/p' <"$WORK_DIR/broker-unit.txt" |
            head -n 1
    )" || fail "could not read $BROKER_UNIT's ExecStart from the installed unit"

    [ -n "$installed_admin_group_value" ] ||
        fail "the installed $BROKER_UNIT declares no --admin-group token"

    printf '%s\n' "$installed_admin_group_value"
}

# 1. The administrator group, from the installed unit, and the account's
# membership of it.
os_id="$(guest_os_id)"
[ -n "$os_id" ] || fail "/etc/os-release declares no ID, so the family's expected administrator group cannot be established"

expected_group="$(expected_admin_group "$os_id")"
declared_group="$(installed_admin_group)"

[ "$declared_group" = "$expected_group" ] ||
    fail "the installed $BROKER_UNIT declares --admin-group $declared_group, expected $expected_group on $os_id"

guest_log "the installed $BROKER_UNIT declares --admin-group $declared_group on $os_id"

admin_groups="$(id -nG "$PH_ADMIN_USER")" ||
    fail "could not read the administrator account's group membership"

printf '%s\n' "$admin_groups" | tr ' ' '\n' | grep -qx "$declared_group" ||
    fail "the administrator account is in '$admin_groups', which does not include the $declared_group group the installed $BROKER_UNIT declares"

guest_log "the administrator account is a member of $declared_group"

# 2a. The successful login. GET /login first for the hidden csrf input: POST
# /login rejects a missing or wrong csrf field with 403, so a bare POST would
# fail for the wrong reason.
login_page="$WORK_DIR/login.html"

login_page_status="$(
    web_curl /login \
        --max-time "$WEB_REQUEST_TIMEOUT_SECONDS" \
        --output "$login_page" \
        --write-out '%{http_code}'
)" || fail "GET /login on $WEB_BASE_URL did not complete within ${WEB_REQUEST_TIMEOUT_SECONDS}s"

[ "$login_page_status" = "200" ] ||
    fail "GET /login returned HTTP $login_page_status, expected exactly 200"

csrf_value="$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$login_page" | head -n 1)"
[ -n "$csrf_value" ] ||
    fail "GET /login carried no hidden csrf input value, so the login form cannot be submitted"

FORM_VALUE="$csrf_value"
write_form_field "$WORK_DIR/csrf"
FORM_VALUE="$PH_ADMIN_USER"
write_form_field "$WORK_DIR/username"
FORM_VALUE="$PH_ADMIN_PASSWORD"
write_form_field "$WORK_DIR/admin-secret"

guest_log "signing in as the administrator through POST /login"

login_cursor="$(journal_cursor "$WEB_UNIT")"

login_status="$(web_login "$WORK_DIR/csrf" "$WORK_DIR/username" "$WORK_DIR/admin-secret")" ||
    fail "POST /login for the administrator did not complete within ${WEB_REQUEST_TIMEOUT_SECONDS}s"

[ "$login_status" = "303" ] ||
    fail "the administrator's login returned HTTP $login_status, expected exactly 303 to /; a 401 means PAM rejected a valid account and a 429 means the per-username+remote lockout answered before Authenticate was called, and neither is a pass"

guest_log "the administrator's login was accepted with 303"

# 2b. The capability query the web process runs on that administrator's behalf
# immediately after the login. refreshCapabilities logs this warning and
# returns, leaving the login successful, so the 303 above is not evidence about
# the query; the absence of the record past the cursor captured immediately
# before the POST is. The record comes from the WEB process, so it is the web
# unit's journal that is read — the broker's would find nothing whatever
# happened.
sleep "$JOURNAL_SETTLE_SECONDS"
journal_records_since "$WEB_UNIT" "$login_cursor" "$WORK_DIR/web-records.json"

# The search counts matching records rather than testing jq's exit status: a
# jq that failed outright would otherwise be indistinguishable from "no such
# record", which is exactly the vacuous pass this assertion must not have.
refresh_hits="$(jq --slurp "[.[] | select(.msg == \"$REFRESH_CAPABILITIES_MESSAGE\")] | length" <"$WORK_DIR/web-records.json")" ||
    fail "could not search $WEB_UNIT's records past the login's cursor"

[ "$refresh_hits" = "0" ] ||
    fail "$WEB_UNIT logged '$REFRESH_CAPABILITIES_MESSAGE' $refresh_hits time(s) for the login just performed: the capability query the web process runs against the broker failed, and the 303 hid it because refreshCapabilities swallows its error"

guest_log "no '$REFRESH_CAPABILITIES_MESSAGE' record past the login's cursor: the broker answered the capability query"

# 2c. The same administrator with a wrong password. The wrong value is derived
# from the real one so it cannot accidentally be the account's password.
wrong_secret="$PH_ADMIN_PASSWORD-definitely-not-the-password"
FORM_VALUE="$wrong_secret"
write_form_field "$WORK_DIR/wrong-secret"

guest_log "attempting the administrator's login with a wrong password"

wrong_status="$(web_login "$WORK_DIR/csrf" "$WORK_DIR/username" "$WORK_DIR/wrong-secret")" ||
    fail "POST /login with a wrong password did not complete within ${WEB_REQUEST_TIMEOUT_SECONDS}s"

[ "$wrong_status" = "401" ] ||
    fail "the wrong-password login returned HTTP $wrong_status, expected exactly 401; a 429 means the lockout answered before Authenticate was called, so PAM never rejected anything and this check would have passed vacuously"

guest_log "the wrong-password login was rejected with 401"

# 2d. root, with the VALID generated password. A locked or password-less root
# would be rejected by PAM before Pilothouse ever saw it, so the account has a
# real password here and the refusal must come from the application layer.
FORM_VALUE="root"
write_form_field "$WORK_DIR/root-username"
FORM_VALUE="$PH_ROOT_PASSWORD"
write_form_field "$WORK_DIR/root-secret"

guest_log "attempting a direct root login with root's valid password"

root_cursor="$(journal_cursor "$BROKER_UNIT")"

root_status="$(web_login "$WORK_DIR/csrf" "$WORK_DIR/root-username" "$WORK_DIR/root-secret")" ||
    fail "POST /login for root did not complete within ${WEB_REQUEST_TIMEOUT_SECONDS}s"

[ "$root_status" = "401" ] ||
    fail "the root login returned HTTP $root_status, expected exactly 401: direct root login is refused"

# The status alone cannot distinguish the UID-zero refusal from a PAM
# account-phase rejection, so the broker's own record past the cursor captured
# immediately before the attempt is the evidence. This is the BROKER's journal
# — the opposite unit from the refresh-capabilities check above.
root_records="$WORK_DIR/broker-records.json"
root_record_found=""
waited=0

while [ "$waited" -lt "$JOURNAL_RECORD_TIMEOUT_SECONDS" ]; do
    journal_records_since "$BROKER_UNIT" "$root_cursor" "$root_records"

    root_hits="$(jq --slurp "[.[] | select(.msg == \"$ROOT_REJECTION_MESSAGE\" and .user == \"root\" and ((.error // \"\") | contains(\"$ROOT_REJECTION_ERROR\")))] | length" <"$root_records")" ||
        fail "could not search $BROKER_UNIT's records past the root attempt's cursor"

    if [ "$root_hits" != "0" ]; then
        root_record_found="yes"
        break
    fi

    sleep 1
    waited=$((waited + 1))
done

[ -n "$root_record_found" ] ||
    fail "$BROKER_UNIT logged no '$ROOT_REJECTION_MESSAGE' record with user=root and an error containing '$ROOT_REJECTION_ERROR' within ${JOURNAL_RECORD_TIMEOUT_SECONDS}s of the attempt: the 401 could equally have come from the PAM account phase, which would prove nothing about the UID-zero refusal"

guest_log "the broker refused root at the application layer after PAM authenticated it"

# 3. The administrator's identity over the AUTHENTICATED direct broker route.
# This is the functional proof of the group check at the top: `admin` is true
# only because the account is in the group the installed unit declares. The
# route sends its own remote, so it cannot be throttled by the wrong-password
# attempt above, which is keyed on the web process's 127.0.0.1.
guest_log "authenticating the administrator directly against the broker socket"

# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_USERNAME="$PH_ADMIN_USER"
# shellcheck disable=SC2034 # read by broker_login in lib.sh
BROKER_LOGIN_PASSWORD="$PH_ADMIN_PASSWORD"
broker_login

[ "$BROKER_SESSION_USERNAME" = "$PH_ADMIN_USER" ] ||
    fail "the broker session identity names $BROKER_SESSION_USERNAME, expected the administrator account"

[ "$BROKER_SESSION_ADMIN" = "true" ] ||
    fail "the administrator's broker session identity reports admin=$BROKER_SESSION_ADMIN, expected true: membership of $declared_group must make the account an administrator, and a matching group name that does not is a failure"

guest_log "the administrator's broker session identity is admin"

broker_query "$CAPABILITY_QUERY_ID" >"$WORK_DIR/capabilities.json"

jq -e 'type == "object"' <"$WORK_DIR/capabilities.json" >/dev/null ||
    fail "the authenticated $CAPABILITY_QUERY_ID query returned no capability object"

guest_log "the authenticated $CAPABILITY_QUERY_ID query answered over the broker socket"

# 4. Remove the generated root password. It existed only so the root-login
# refusal could be proved against a real password rather than against a locked
# account, and both commands succeed because this script runs as root — no step
# here assumes an SSH login as root, which the guest does not have.
guest_log "removing the generated root password"

passwd -d root ||
    fail "passwd -d root failed, so the generated root password is still set"

usermod -L root ||
    fail "usermod -L root failed, so root's account is still unlocked"

guest_log "PAM authenticated a real non-root administrator end to end, both negatives are proved by the journal, and root's generated password is removed"
