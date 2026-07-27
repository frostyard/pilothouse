#!/bin/sh
set -eu
# shellcheck source=test/vm/guest/lib.sh disable=SC1090,SC1091
. "$(dirname "$0")/lib.sh"
require_root

# install-package.sh — install the staged package artifact in the guest, and
# the two test fixtures the later guest checks need.
#
# Invoked by the orchestrator, and only by the orchestrator, as:
#
#     sudo -n sh ~/vm-boot/guest/install-package.sh
#
# Explicit interpreter and explicit escalation: package installation needs
# root, and require_root above proves the escalation actually happened before
# anything else runs.
#
# What this script is NOT is a second copy of Layer A (#77). Dependency
# resolution, the postinstall account and permission repair, PAM policy
# resolution, unit-file validity, dynamic linkage, reinstall and removal are
# all asserted there, in containers, against these same artifacts. Here the
# install is a PREREQUISITE: everything this tier exists to prove happens after
# it, on a booted host. Nor is SELinux worked around — the Fedora guest boots
# enforcing and stays that way, so the install runs under enforcement like any
# other install on that distro.
#
# curl and jq are test fixtures, not a package source: curl makes the
# `--unix-socket` requests the broker checks need, and jq is what lets those
# checks match a JSON field rather than a substring.

# The staging directory is derived from this script's own path rather than from
# a tilde: the tilde was expanded by the administrator's shell before sudo -n
# ran, so $0 is already the absolute staged path, and root's home is somewhere
# else entirely.
staging_dir="$(cd "$(dirname "$0")/.." && pwd)" ||
    fail "could not resolve the staging directory from $0"

# The package format follows the guest's own package manager, never an
# artifact's file name. The orchestrator has already selected exactly one
# arch-qualified artifact on the host and staged it under a fixed name.
if command -v apt-get >/dev/null 2>&1; then
    format=deb
elif command -v dnf >/dev/null 2>&1; then
    format=rpm
else
    fail "no supported package manager in the guest: need apt-get (deb) or dnf (rpm)"
fi

artifact="$staging_dir/pilothouse-artifact.$format"
[ -f "$artifact" ] ||
    fail "the staged $format artifact $artifact is missing; the orchestrator stages it before invoking this script"

guest_log "installing $artifact and the curl/jq test fixtures ($format)"

case "$format" in
    deb)
        DEBIAN_FRONTEND=noninteractive apt-get update ||
            fail "apt-get update failed in the guest"
        DEBIAN_FRONTEND=noninteractive apt-get install -y curl jq ||
            fail "installing the curl and jq test fixtures failed in the guest"
        DEBIAN_FRONTEND=noninteractive apt-get install -y "$artifact" ||
            fail "installing $artifact failed in the guest"
        ;;
    rpm)
        dnf -y install curl jq ||
            fail "installing the curl and jq test fixtures failed in the guest"
        dnf -y install "$artifact" ||
            fail "installing $artifact failed in the guest"
        ;;
esac

# The fixtures are asserted because every later guest check depends on them; the
# package's own installed state is Layer A's subject, not this script's.
command -v curl >/dev/null 2>&1 || fail "curl is not on PATH after installation"
command -v jq >/dev/null 2>&1 || fail "jq is not on PATH after installation"

guest_log "installed $artifact"
