#!/bin/sh
set -eu

# packaging/verify-install.sh - Layer A of package validation.
#
# This script runs INSIDE a target distro container image, as root, against a
# directory of freshly built artifacts. It is deliberately container-only: it
# never requires systemd as PID 1, never starts or queries a service manager,
# never restarts the machine, and never depends on an enforcing SELinux policy.
# Anything that needs a booted host belongs to Layer B, not here.
#
# Usage:
#   packaging/verify-install.sh <artifact-dir>
#
# At this commit the script performs three checks:
#
#   1. Install the artifact through the container's own package manager, so
#      the hand-written per-format `dependencies` lists in .goreleaser.yaml are
#      resolved against the distro's real repositories.
#   2. check_account  - the `pilothouse` user and group exist afterward and
#      match the INSTALLED sysusers declaration.
#   3. check_owner_mode - on-disk owner, group and mode of the configuration
#      directories and env files, read from the installed filesystem rather
#      than from package metadata.
#
# Every expectation is written as one `expect_owner_mode <path> <owner>
# <group> <mode>` call per line with literal arguments, so the Go guard tests
# in packaging/verify_install_test.go can parse them deterministically.
#
# The first failed assertion aborts the whole run: every check calls fail(),
# which exits non-zero immediately.

# sysusers_conf is the installed sysusers declaration. It is the live source of
# truth for the account this script checks; nothing about the account is
# hardcoded here.
sysusers_conf=/usr/lib/sysusers.d/pilothouse.conf

fail() {
    printf 'verify-install: %s\n' "$1" >&2
    exit 1
}

usage() {
    printf 'usage: packaging/verify-install.sh <artifact-dir>\n' >&2
}

# sysusers_field prints one field of the installed sysusers user line. The
# GECOS field is quoted and may contain spaces, so the line is split on the
# quote character rather than on whitespace.
sysusers_field() {
    awk -v want="$1" '
        $1 != "u" { next }
        {
            split($0, quoted, "\"")
            split(quoted[3], tail, " ")

            if (want == "name") { print $2 }
            else if (want == "gecos") { print quoted[2] }
            else if (want == "home") { print tail[1] }
            else if (want == "shell") { print tail[2] }
        }
    ' "${sysusers_conf}"
}

# passwd_field prints one colon-separated field of a getent record.
passwd_field() {
    printf '%s\n' "$1" | cut -d: -f"$2"
}

# expect_equal compares one observed account property against the value the
# installed sysusers file declares. A declared "-" means "distro default", so
# it is not asserted.
expect_equal() {
    if [ "$2" = "-" ] || [ -z "$2" ]; then
        return 0
    fi

    if [ "$2" != "$3" ]; then
        fail "$1: expected '$2' (from ${sysusers_conf}), got '$3'"
    fi
}

# check_account is check 2: the account exists and reproduces the installed
# sysusers declaration. It is a function because a later reinstall check
# re-invokes it.
check_account() {
    [ -f "${sysusers_conf}" ] ||
        fail "${sysusers_conf} is missing after install"

    account=$(sysusers_field name)
    [ -n "${account}" ] ||
        fail "${sysusers_conf} declares no user line"

    passwd_entry=$(getent passwd "${account}") ||
        fail "user ${account} does not exist after install"
    group_entry=$(getent group "${account}") ||
        fail "group ${account} does not exist after install"

    expect_equal "${account} home directory" "$(sysusers_field home)" "$(passwd_field "${passwd_entry}" 6)"
    expect_equal "${account} shell" "$(sysusers_field shell)" "$(passwd_field "${passwd_entry}" 7)"
    expect_equal "${account} GECOS" "$(sysusers_field gecos)" "$(passwd_field "${passwd_entry}" 5)"

    account_uid=$(passwd_field "${passwd_entry}" 3)
    account_gid=$(passwd_field "${passwd_entry}" 4)
    group_gid=$(passwd_field "${group_entry}" 3)

    [ "${account_gid}" = "${group_gid}" ] ||
        fail "user ${account} has primary group gid ${account_gid}, expected the ${account} group's gid ${group_gid}"

    [ "${account_uid}" -lt 1000 ] ||
        fail "user ${account} has uid ${account_uid}, which is outside the system range (< 1000)"

    printf 'verify-install: account %s (uid %s, gid %s) matches %s\n' \
        "${account}" "${account_uid}" "${account_gid}" "${sysusers_conf}"
}

# expect_owner_mode asserts one installed path's on-disk owner, group and mode,
# read with stat from the installed filesystem rather than from the package
# database.
expect_owner_mode() {
    [ -e "$1" ] ||
        fail "$1 is missing after install"

    observed=$(stat -c '%U %G %04a' "$1") ||
        fail "cannot stat $1"

    if [ "${observed}" != "$2 $3 $4" ]; then
        fail "$1: expected owner/group/mode '$2 $3 $4', got '${observed}'"
    fi
}

# check_owner_mode is check 3. Like check_account it is a function because a
# later reinstall check re-invokes it.
check_owner_mode() {
    expect_owner_mode /etc/pilothouse root pilothouse 0750
    expect_owner_mode /etc/pilothouse/storage/credentials root root 0700
    expect_owner_mode /etc/pilothouse/pilothouse.env root pilothouse 0640
    expect_owner_mode /etc/pilothouse/pilothoused.env root pilothouse 0640

    printf 'verify-install: on-disk ownership and modes match the packaging contract\n'
}

if [ "$#" -ne 1 ]; then
    usage
    fail "exactly one operand is required: the directory holding the built artifacts"
fi

if [ ! -d "$1" ]; then
    usage
    fail "artifact directory '$1' does not exist or is not a directory"
fi

artifact_dir=$(cd "$1" && pwd)

# The package format is detected from the container's own package manager,
# never from an artifact's file name.
if command -v apt-get >/dev/null 2>&1; then
    format=deb
elif command -v dnf >/dev/null 2>&1; then
    format=rpm
else
    fail "no supported package manager found: need apt-get (deb) or dnf (rpm)"
fi

# Only amd64 artifacts are installed; arm64 install validation is out of scope.
case "${format}" in
    deb) set -- "${artifact_dir}"/*_amd64.deb ;;
    rpm) set -- "${artifact_dir}"/*.x86_64.rpm ;;
esac

artifact=
artifact_count=0
artifact_names=

for candidate in "$@"; do
    [ -f "${candidate}" ] || continue

    artifact="${candidate}"
    artifact_count=$((artifact_count + 1))
    artifact_names="${artifact_names} $(basename "${candidate}")"
done

if [ "${artifact_count}" -ne 1 ]; then
    fail "expected exactly one amd64 ${format} artifact in ${artifact_dir}, found ${artifact_count}:${artifact_names:-" (none)"}"
fi

printf 'verify-install: format %s, artifact %s\n' "${format}" "${artifact}"

# Check 1: install through the distro package manager so the per-format
# dependency lists are resolved against real repositories.
case "${format}" in
    deb)
        apt-get update ||
            fail "apt-get update failed"
        apt-get install -y "${artifact}" ||
            fail "apt-get install of ${artifact} failed"
        ;;
    rpm)
        dnf install -y "${artifact}" ||
            fail "dnf install of ${artifact} failed"
        ;;
esac

printf 'verify-install: installed %s\n' "${artifact}"

check_account
check_owner_mode

printf 'verify-install: all checks passed\n'
