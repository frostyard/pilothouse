#!/bin/sh
# Runs inside the ephemeral uCore guest as root. This does not repeat the #67
# package-install suite; it checks only image-host facts and uses one broker
# login as the prerequisite for reading Pilothouse's advertised capabilities.
set -eu

CREDENTIALS=/root/pilothouse-image-credentials.json
BROKER_SOCKET=/run/pilothouse/broker.sock
CAPABILITY_QUERY=org.frostyard.pilothouse.capabilities.list
HOST_IMAGE_QUERY=org.frostyard.pilothouse.maintenance.host_image_status

fail() {
    printf 'ucore guest: %s\n' "$*" >&2
    exit 1
}

log() {
    printf 'ucore guest: %s\n' "$*"
}

[ "$(id -u)" = 0 ] || fail "validation must run as root"
for tool in bootc chpasswd cmp curl diff getenforce getent grep journalctl jq \
    podman sed systemctl useradd usermod; do
    command -v "$tool" >/dev/null 2>&1 ||
        fail "required image-host tool is unavailable: $tool"
done

case "${1-}" in
    prepare)
        [ -f "$CREDENTIALS" ] || fail "credentials file is missing"
        username="$(jq -er '.username' "$CREDENTIALS")" ||
            fail "credentials carry no username"
        password="$(jq -er '.password' "$CREDENTIALS")" ||
            fail "credentials carry no password"
        getent group wheel >/dev/null 2>&1 || fail "uCore has no wheel administrator group"
        if ! id "$username" >/dev/null 2>&1; then
            useradd --create-home --groups wheel "$username"
        else
            usermod --append --groups wheel "$username"
        fi
        printf '%s:%s\n' "$username" "$password" | chpasswd
        unset password
        systemctl restart pilothoused.service pilothouse.service
        systemctl is-active --quiet pilothoused.service pilothouse.service ||
            fail "Pilothouse units did not become active after preparing the image-test identity"
        log "prepared the ephemeral administrator used to read broker capabilities"
        exit 0
        ;;
    validate)
        expected_slot="${2-}"
        case "$expected_slot" in baseline | update) ;; *) fail "validate needs baseline or update" ;; esac
        ;;
    *)
        fail "usage: validate-ucore.sh prepare | validate baseline|update"
        ;;
esac

[ "$(cat /usr/lib/pilothouse-image-test/slot)" = "$expected_slot" ] ||
    fail "booted /usr marker does not identify the expected $expected_slot deployment"
[ "$(getenforce)" = Enforcing ] ||
    fail "SELinux is not enforcing"
[ -S "$BROKER_SOCKET" ] ||
    fail "the image-booted broker socket is unavailable"
bootc status --json >/dev/null ||
    fail "bootc status is not functional on the booted image"

work_dir="$(mktemp -d)"
chmod 0700 "$work_dir"
cleanup() {
    rm -f "$work_dir/login.json" "$work_dir/login-body.json" \
        "$work_dir/query.json" "$work_dir/query-body.json" \
        "$work_dir/auth.header" "$work_dir/actual" "$work_dir/expected" \
        "$work_dir/host-image.json" "$work_dir/new-avcs"
    rmdir "$work_dir"
}
trap cleanup EXIT

username="$(jq -er '.username' "$CREDENTIALS")" ||
    fail "credentials carry no username"
password="$(jq -er '.password' "$CREDENTIALS")" ||
    fail "credentials carry no password"

PILOTHOUSE_IMAGE_TEST_USERNAME="$username" \
    PILOTHOUSE_IMAGE_TEST_PASSWORD="$password" \
    jq -n '{
        username: env.PILOTHOUSE_IMAGE_TEST_USERNAME,
        password: env.PILOTHOUSE_IMAGE_TEST_PASSWORD,
        remote: "ucore-image-test"
    }' >"$work_dir/login.json"
unset password PILOTHOUSE_IMAGE_TEST_PASSWORD

login_status="$(
    curl --silent --show-error --max-time 30 \
        --unix-socket "$BROKER_SOCKET" \
        --request POST \
        --header 'Content-Type: application/json' \
        --data-binary @"$work_dir/login.json" \
        --output "$work_dir/login-body.json" \
        --write-out '%{http_code}' \
        http://localhost/v1/login
)" || fail "broker login did not complete"
[ "$login_status" = 200 ] ||
    fail "broker login returned HTTP $login_status, expected 200"

token="$(jq -er '.token' "$work_dir/login-body.json")" ||
    fail "broker login returned no token"
PILOTHOUSE_IMAGE_TEST_TOKEN="$token" \
    jq -nj '"Authorization: Bearer " + env.PILOTHOUSE_IMAGE_TEST_TOKEN' \
    >"$work_dir/auth.header"
unset token PILOTHOUSE_IMAGE_TEST_TOKEN
printf '%s\n' '{"parameters":{}}' >"$work_dir/query.json"

journal_cursor="$(
    journalctl --no-pager --lines 0 --show-cursor |
        sed -n 's/^-- cursor: //p'
)" || fail "could not establish a journal cursor before the image-host queries"
[ -n "$journal_cursor" ] ||
    fail "journalctl returned no cursor before the image-host queries"

broker_query() {
    query_id="$1"
    output="$2"
    status="$(
        curl --silent --show-error --max-time 30 \
            --unix-socket "$BROKER_SOCKET" \
            --request POST \
            --header 'Content-Type: application/json' \
            --header @"$work_dir/auth.header" \
            --data-binary @"$work_dir/query.json" \
            --output "$output" \
            --write-out '%{http_code}' \
            "http://localhost/v1/queries/$query_id"
    )" || fail "$query_id did not complete"
    [ "$status" = 200 ] ||
        fail "$query_id returned HTTP $status, expected 200"
}

broker_query "$CAPABILITY_QUERY" "$work_dir/query-body.json"
jq -er '.result.capabilities[]' "$work_dir/query-body.json" |
    LC_ALL=C sort -u >"$work_dir/actual"

# Build the expectation from independent host observations. The four opt-in
# dependencies are deliberately absent: the packaged uCore unit configures
# none of them, even though Podman itself is present in the base image.
: >"$work_dir/expected"
[ -d /run/systemd/system ] &&
    systemctl show-environment >/dev/null 2>&1 &&
    printf '%s\n' systemd >>"$work_dir/expected"
journalctl --no-pager --lines 0 >/dev/null 2>&1 &&
    printf '%s\n' journald >>"$work_dir/expected"
systemd-sysext list >/dev/null 2>&1 &&
    printf '%s\n' sysext >>"$work_dir/expected"
bootc status --json >/dev/null 2>&1 &&
    printf '%s\n' bootc >>"$work_dir/expected"
rpm-ostree status --json >/dev/null 2>&1 &&
    printf '%s\n' rpm-ostree >>"$work_dir/expected"

if systemctl list-unit-files bootc-fetch-apply-updates.timer --no-legend |
    grep -q '^bootc-fetch-apply-updates\.timer[[:space:]]' &&
   systemctl list-unit-files bootc-fetch-apply-updates.service --no-legend |
    grep -q '^bootc-fetch-apply-updates\.service[[:space:]]'; then
    printf '%s\n' autoupdate-bootc >>"$work_dir/expected"
fi
if systemctl list-unit-files rpm-ostreed-automatic.timer --no-legend |
    grep -q '^rpm-ostreed-automatic\.timer[[:space:]]' &&
   systemctl list-unit-files rpm-ostreed-automatic.service --no-legend |
    grep -q '^rpm-ostreed-automatic\.service[[:space:]]'; then
    printf '%s\n' autoupdate-rpm-ostree >>"$work_dir/expected"
fi
LC_ALL=C sort -u -o "$work_dir/expected" "$work_dir/expected"

grep -qx bootc "$work_dir/actual" ||
    fail "Pilothouse did not advertise bootc on the bootc host"
if ! cmp -s "$work_dir/expected" "$work_dir/actual"; then
    diff -u "$work_dir/expected" "$work_dir/actual" >&2 || true
    fail "advertised capabilities do not exactly match independently observed image capabilities"
fi

broker_query "$HOST_IMAGE_QUERY" "$work_dir/host-image.json"
jq -e '
    .result.bootc_available == true and
    (.result.booted.image | type == "string" and length > 0) and
    (.result.booted.digest | type == "string" and
        test("^sha256:[0-9a-f]{64}$"))
' "$work_dir/host-image.json" >/dev/null ||
    fail "Pilothouse's host-image query does not report the booted bootc deployment"

# Fail on every AVC created during the controlled broker-query window, and on
# any current-boot AVC that names Pilothouse. This is an enforcing smoke test,
# not a claim that the RPM provides a dedicated Pilothouse SELinux domain.
journalctl --no-pager --after-cursor="$journal_cursor" -o cat >"$work_dir/new-avcs"
if grep -Eiq 'avc:[[:space:]]+denied' "$work_dir/new-avcs"; then
    cat "$work_dir/new-avcs" >&2
    fail "an unexpected SELinux AVC denial occurred during image-host validation"
fi
if journalctl --no-pager --boot -o cat |
    grep -Ei 'avc:[[:space:]]+denied' |
    grep -Eiq 'pilothouse|pilothoused|/run/pilothouse|/var/lib/pilothouse'; then
    journalctl --no-pager --boot -o cat |
        grep -Ei 'avc:[[:space:]]+denied' |
        grep -Ei 'pilothouse|pilothoused|/run/pilothouse|/var/lib/pilothouse' >&2 || true
    fail "the current boot contains a Pilothouse-related SELinux AVC denial"
fi

log "$expected_slot deployment is enforcing, capability-truthful and AVC-clean"
