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
