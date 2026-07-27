# lib.sh — the shared guest-side library for the booted-VM harness (#67).
#
# This is a SOURCED library, not a program: it is committed non-executable and
# is never invoked as a command. Every guest script sources it as its first
# effective statement after `set -eu`:
#
#     #!/bin/sh
#     set -eu
#     . "$(dirname "$0")/lib.sh"
#     require_root
#
# POSIX sh, because Debian's /bin/sh is dash and Fedora's is bash and the same
# script must run on both. `set -eu` in every caller plus a single fail() that
# names the failing assertion is what makes a guest script abort on its FIRST
# failed assertion rather than reporting the last one.
#
# Nothing here escalates privilege. The orchestrator invokes every guest script
# as `sudo -n sh ~/vm-boot/guest/<name>.sh`, so the whole script already runs as
# root, and require_root proves that at run time instead of trusting the call
# site. One escalation boundary, at the call site, is easier to audit than one
# per command — and if a call site ever loses its escalation, require_root fails
# immediately and legibly instead of the failure surfacing as a confusing
# permission error several assertions later.
#
# shellcheck shell=sh

set -eu

# GUEST_CREDENTIALS_FILE is where the orchestrator installs the run-time
# credentials it generated on the host, with `install -o root -g root -m 0600`.
# Nothing in this repository contains its contents, and no credential is ever
# passed to a guest command as an argument.
GUEST_CREDENTIALS_FILE="${GUEST_CREDENTIALS_FILE:-/root/.pilothouse-vm-creds}"

# BROKER_SOCKET is the privileged broker's Unix socket, and WEB_BASE_URL is the
# unprivileged web console's default listen address.
BROKER_SOCKET="${BROKER_SOCKET:-/run/pilothouse/broker.sock}"
WEB_BASE_URL="${WEB_BASE_URL:-http://127.0.0.1:8888}"

# BROKER_REMOTE is the `remote` the harness sends with a DIRECT broker login,
# and it is deliberately not 127.0.0.1. The broker keys its login lockout on
# lower(username) + "\0" + remote, and the web console sends the request's own
# peer host, which on loopback is 127.0.0.1. Reusing that value here would put
# the harness's direct logins in the same lockout bucket as the web flows, so a
# wrong-password check on one surface could answer 429 on the other before
# Authenticate was ever called. A distinct token keeps the two buckets apart.
BROKER_REMOTE="${BROKER_REMOTE:-vm-boot-harness}"

# BROKER_REQUEST_TIMEOUT_SECONDS bounds every direct broker request. A socket
# that accepts a connection and then never answers must fail by name rather
# than hang the run until the job's own limit.
BROKER_REQUEST_TIMEOUT_SECONDS="${BROKER_REQUEST_TIMEOUT_SECONDS:-30}"

# fail is the ONLY failure path in the guest. It names the assertion that
# failed and exits non-zero, which under `set -e` aborts the calling script at
# the first failure.
fail() {
    printf 'assertion failed: %s\n' "$*" >&2
    exit 1
}

guest_log() {
    printf 'guest: %s\n' "$*"
}

# require_root is the converse guard to the orchestrator's explicit call
# form, `sudo -n sh <staged path>`.
# Package installation, `systemctl enable --now`, reading the 0600 credentials
# file, opening the 0660 root:pilothouse broker socket, `journalctl -u` and
# clearing root's password all need privilege; a call site that lost its
# escalation must fail here, at the top, not three assertions deep.
require_root() {
    require_root_uid="$(id -u)"
    [ "$require_root_uid" = "0" ] ||
        fail "this script must run as root (id -u is $require_root_uid); the orchestrator invokes every guest script as 'sudo -n sh ~/vm-boot/guest/<name>.sh'"
}

# expect_owner_mode asserts a path's owner, group and mode as they are on disk.
expect_owner_mode() {
    expect_path="$1"
    expect_owner="$2"
    expect_group="$3"
    expect_mode="$4"

    [ -e "$expect_path" ] || fail "$expect_path does not exist"

    expect_actual="$(stat -c '%U %G %a' "$expect_path")" ||
        fail "could not stat $expect_path"

    [ "$expect_actual" = "$expect_owner $expect_group $expect_mode" ] ||
        fail "$expect_path is '$expect_actual', expected '$expect_owner $expect_group $expect_mode'"
}

# load_credentials sources the installed credentials file into the current
# shell. It is the only reader: no credential is echoed, and none is ever
# passed on a command line.
load_credentials() {
    [ -f "$GUEST_CREDENTIALS_FILE" ] ||
        fail "$GUEST_CREDENTIALS_FILE is missing; the orchestrator installs it with 'install -o root -g root -m 0600'"

    # shellcheck disable=SC1090,SC1091 # generated on the host at run time
    . "$GUEST_CREDENTIALS_FILE"

    [ -n "${PH_ADMIN_USER-}" ] ||
        fail "$GUEST_CREDENTIALS_FILE declares no PH_ADMIN_USER"
    [ -n "${PH_ADMIN_PASSWORD-}" ] ||
        fail "$GUEST_CREDENTIALS_FILE declares no administrator credential"
    [ -n "${PH_ROOT_PASSWORD-}" ] ||
        fail "$GUEST_CREDENTIALS_FILE declares no root credential"
}

# broker_curl <path> [curl-arg...] makes an HTTP request against the broker's
# Unix socket. The socket is mode 0660 owned root:pilothouse, so this needs
# privilege — which the script already has, because it was invoked through the
# one escalation boundary. There is deliberately no inner escalation here.
broker_curl() {
    broker_path="$1"
    shift

    curl --silent --show-error --unix-socket "$BROKER_SOCKET" "$@" \
        "http://localhost${broker_path}"
}

# web_curl <path> [curl-arg...] makes an HTTP request against the unprivileged
# web console over loopback. It carries no inner escalation either, for the
# same reason: this is the request an ordinary client would make, and wrapping
# it would only blur where privilege is obtained.
web_curl() {
    web_path="$1"
    shift

    curl --silent --show-error "$@" "${WEB_BASE_URL}${web_path}"
}

# broker_login authenticates over the broker's Unix socket and is the first
# half of the harness's reusable AUTHENTICATED direct route: POST /v1/login
# with a JSON body of username, password and remote, then POST /v1/queries/{id}
# with the returned token (broker_query, below). The socket is mode 0660 owned
# root:pilothouse, so the caller must be privileged — it is, because the whole
# script runs as root under `sudo -n sh`, which require_root proves.
#
# The credentials are read from BROKER_LOGIN_USERNAME and
# BROKER_LOGIN_PASSWORD, which the caller sets from the loaded credentials.
# They are never command-line arguments: the request body is built by jq from
# the environment of that one jq process and handed to curl as a file.
#
# On success it sets BROKER_SESSION_TOKEN, BROKER_SESSION_USERNAME and
# BROKER_SESSION_ADMIN for the caller, so it must be called directly and never
# through a command substitution, which would discard all three.
broker_login() {
    [ -n "${BROKER_LOGIN_USERNAME-}" ] ||
        fail "broker_login needs BROKER_LOGIN_USERNAME set to the account to authenticate as"
    [ -n "${BROKER_LOGIN_PASSWORD-}" ] ||
        fail "broker_login needs BROKER_LOGIN_PASSWORD set to that account's credential"

    broker_login_request="$(mktemp)"
    broker_login_body="$(mktemp)"
    chmod 0600 "$broker_login_request" "$broker_login_body"

    BROKER_LOGIN_USERNAME="$BROKER_LOGIN_USERNAME" \
        BROKER_LOGIN_PASSWORD="$BROKER_LOGIN_PASSWORD" \
        BROKER_REMOTE="$BROKER_REMOTE" \
        jq -nc '{username: env.BROKER_LOGIN_USERNAME, password: env.BROKER_LOGIN_PASSWORD, remote: env.BROKER_REMOTE}' \
        >"$broker_login_request" ||
        fail "could not build the direct broker login request for $BROKER_LOGIN_USERNAME"

    broker_login_status="$(
        broker_curl /v1/login \
            --request POST \
            --header 'Content-Type: application/json' \
            --data-binary @"$broker_login_request" \
            --max-time "$BROKER_REQUEST_TIMEOUT_SECONDS" \
            --output "$broker_login_body" \
            --write-out '%{http_code}'
    )" || fail "POST /v1/login over $BROKER_SOCKET did not complete within ${BROKER_REQUEST_TIMEOUT_SECONDS}s"

    rm -f "$broker_login_request"

    [ "$broker_login_status" = "200" ] ||
        fail "POST /v1/login for $BROKER_LOGIN_USERNAME returned HTTP $broker_login_status, expected exactly 200; a 429 is the per-username+remote lockout answering before Authenticate was called, which is a failure of this check and never a pass"

    BROKER_SESSION_TOKEN="$(jq -er '.token' <"$broker_login_body")" ||
        fail "the broker login response for $BROKER_LOGIN_USERNAME carries no token"
    # shellcheck disable=SC2034 # read by the calling guest script, not here
    BROKER_SESSION_USERNAME="$(jq -er '.session.identity.username' <"$broker_login_body")" ||
        fail "the broker login response for $BROKER_LOGIN_USERNAME carries no session.identity.username"
    # shellcheck disable=SC2034 # read by the calling guest script, not here
    BROKER_SESSION_ADMIN="$(jq -er '.session.identity.admin | tostring' <"$broker_login_body")" ||
        fail "the broker login response for $BROKER_LOGIN_USERNAME carries no session.identity.admin"

    rm -f "$broker_login_body"
}

# broker_query <query-id> [parameters-json] is the second half of the direct
# route: POST /v1/queries/{id} carrying the token broker_login returned, with a
# JSON parameters object. It prints the query's `result` on standard output and
# fails by name on any status other than 200.
#
# The bearer token goes to curl in a header FILE rather than as an argument, so
# no session credential lands in the guest's process table.
broker_query() {
    [ "$#" -ge 1 ] || fail "usage: broker_query <query-id> [parameters-json]"
    [ -n "${BROKER_SESSION_TOKEN-}" ] ||
        fail "broker_query needs a session: call broker_login first"

    broker_query_id="$1"
    broker_query_parameters="${2:-}"
    [ -n "$broker_query_parameters" ] || broker_query_parameters='{}'

    broker_query_request="$(mktemp)"
    broker_query_header="$(mktemp)"
    broker_query_body="$(mktemp)"
    chmod 0600 "$broker_query_request" "$broker_query_header" "$broker_query_body"

    BROKER_QUERY_PARAMETERS="$broker_query_parameters" \
        jq -nc '{parameters: (env.BROKER_QUERY_PARAMETERS | fromjson)}' \
        >"$broker_query_request" ||
        fail "could not build the $broker_query_id request from parameters $broker_query_parameters"

    BROKER_SESSION_TOKEN="$BROKER_SESSION_TOKEN" \
        jq -nj '"Authorization: Bearer " + env.BROKER_SESSION_TOKEN' \
        >"$broker_query_header" ||
        fail "could not build the authorization header for $broker_query_id"

    broker_query_status="$(
        broker_curl "/v1/queries/$broker_query_id" \
            --request POST \
            --header 'Content-Type: application/json' \
            --header @"$broker_query_header" \
            --data-binary @"$broker_query_request" \
            --max-time "$BROKER_REQUEST_TIMEOUT_SECONDS" \
            --output "$broker_query_body" \
            --write-out '%{http_code}'
    )" || fail "POST /v1/queries/$broker_query_id over $BROKER_SOCKET did not complete within ${BROKER_REQUEST_TIMEOUT_SECONDS}s"

    rm -f "$broker_query_request" "$broker_query_header"

    [ "$broker_query_status" = "200" ] ||
        fail "POST /v1/queries/$broker_query_id returned HTTP $broker_query_status, expected exactly 200 for an authenticated caller"

    jq -e '.result' <"$broker_query_body" ||
        fail "the $broker_query_id response carries no result field"

    rm -f "$broker_query_body"
}
