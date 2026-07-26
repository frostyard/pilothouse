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
# At this commit the script performs all eight checks:
#
#   1. Install the artifact through the container's own package manager, so
#      the hand-written per-format `dependencies` lists in .goreleaser.yaml are
#      resolved against the distro's real repositories.
#   2. check_account  - the `pilothouse` user and group exist afterward and
#      match the INSTALLED sysusers declaration.
#   3. check_owner_mode - on-disk owner, group and mode of the configuration
#      directories and env files, read from the installed filesystem rather
#      than from package metadata.
#   4. check_pam - every stack and every module the INSTALLED PAM policy names
#      exists on this distro. Both lists are parsed out of the installed policy
#      at run time; nothing about the policy is hardcoded here, so the same
#      code covers whichever per-format policy the package shipped.
#   5. expect_unit - the distro's own `systemd-analyze verify` accepts both
#      INSTALLED unit files. That validator runs offline, against files, so it
#      needs no service manager and stays inside this layer's container-only
#      remit.
#   6. expect_linked - the cgo-linked binary's dynamic dependencies all
#      resolve, which is what proves the declared libpam and libsystemd
#      dependencies really satisfy it.
#   7. Reinstalling the SAME artifact over the existing install succeeds, and
#      checks 2 and 3 still hold afterward. The two check functions above are
#      re-invoked rather than copied, so the reinstall is held to exactly the
#      assertions the first install was held to.
#   8. Removing the package succeeds and the post-removal state is asserted,
#      per format. dpkg and rpm do not treat `type: config` files alike, so the
#      expectations are per verb, not shared:
#
#        - Both formats: expect_removed paths (the binaries, both units and the
#          sysusers file) are gone. Neither manager keeps non-config payload.
#        - Debian `dpkg -r`: expect_conffile paths SURVIVE, because they are
#          conffiles and a remove is not a purge.
#        - Debian `dpkg -P`: the same conffiles are gone.
#        - Fedora `rpm -e`: the same conffiles are gone, and no `.rpmsave` was
#          left beside any of them. A `.rpmsave` would mean the postinstall
#          modified a config file the package itself shipped.
#        - Both formats: the `pilothouse` user and group still exist.
#          systemd-sysusers created them and neither manager owns them, so a
#          future change that starts deleting them is noticed here.
#
#      Whether /etc/pilothouse itself is pruned is deliberately NOT asserted:
#      whether an empty directory that held surviving conffiles is removed
#      varies between managers and versions, and it is not worth pinning. The
#      two directories systemd's RuntimeDirectory=/StateDirectory= own are not
#      mentioned anywhere in this script either - they are deliberately
#      unpackaged, and a container has no running systemd to create them.
#
# Every expectation is written as one `expect_owner_mode <path> <owner>
# <group> <mode>`, `expect_unit <path>`, `expect_linked <path>`,
# `expect_conffile <path>` or `expect_removed <path>` call per line with
# literal arguments, so the Go guard tests in
# packaging/verify_install_test.go can parse them deterministically.
#
# The first failed assertion aborts the whole run: every check calls fail(),
# which exits non-zero immediately.

# sysusers_conf is the installed sysusers declaration. It is the live source of
# truth for the account this script checks; nothing about the account is
# hardcoded here.
sysusers_conf=/usr/lib/sysusers.d/pilothouse.conf

# pam_policy is the INSTALLED PAM policy. Like the sysusers file it is the live
# source of truth: check 4 derives both of its expectation lists from this file
# at run time rather than from a per-distro table, so the check follows
# whichever policy the format's override actually shipped.
pam_policy=/etc/pam.d/pilothouse

# package_name is the installed package's name, as .goreleaser.yaml's
# `package_name` declares it. Check 8 hands it to the removal verbs; a Go guard
# test keeps it equal to the live configuration.
package_name=frostyard-pilothouse

# rpmsave_search_dirs are the directories check 8 sweeps for a stray
# `*.rpmsave` after an `rpm -e`: the two directories the packaged config files
# live in.
rpmsave_search_dirs='/etc/pilothouse /etc/pam.d'

# pam_module_dir_candidates are the directories a distro may keep its PAM
# modules in. The set is searched, not assumed: Debian-family hosts use a
# multiarch subdirectory, Fedora-family hosts use the lib64 one, and neither
# path is hardcoded as "the" module directory.
pam_module_dir_candidates='/lib/security /lib64/security /usr/lib/security /usr/lib64/security /usr/lib/*-linux-gnu/security'

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
# sysusers declaration. It is a function so the whole check is one named unit
# that can be invoked more than once.
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

# check_owner_mode is check 3. Like check_account it is a function so the whole
# check is one named unit that can be invoked more than once.
check_owner_mode() {
    expect_owner_mode /etc/pilothouse root pilothouse 0750
    expect_owner_mode /etc/pilothouse/storage/credentials root root 0700
    expect_owner_mode /etc/pilothouse/pilothouse.env root pilothouse 0640
    expect_owner_mode /etc/pilothouse/pilothoused.env root pilothouse 0640

    printf 'verify-install: on-disk ownership and modes match the packaging contract\n'
}

# pam_stacks prints every stack the installed policy pulls in: the operand of
# an `@include` line, and the file named by an `include` or `substack` control
# value. Comments are stripped first so a commented-out directive contributes
# nothing.
pam_stacks() {
    awk '
        { sub(/#.*/, "") }
        $1 == "@include" { if (NF >= 2) { print $2 } ; next }
        NF >= 3 && ($2 == "include" || $2 == "substack") { print $3 }
    ' "${pam_policy}" | sort -u
}

# pam_modules prints every PAM module the installed policy names, as a bare
# file name. A module may be written with a directory prefix, so the leading
# path is stripped before the name is matched. The name may also contain
# hyphens or dots, so those are accepted too: every module the policy names
# must be checked, not only the ones spelled with word characters.
pam_modules() {
    awk '
        { sub(/#.*/, "") }
        {
            for (i = 1; i <= NF; i++) {
                token = $i
                sub(/^.*\//, "", token)
                if (token ~ /^pam_[A-Za-z0-9_.-]+\.so$/) { print token }
            }
        }
    ' "${pam_policy}" | sort -u
}

# pam_module_dirs prints the candidate module directories that exist here. The
# candidates are searched rather than selected by distro, so the check needs no
# knowledge of which family it is running on.
pam_module_dirs() {
    for dir in ${pam_module_dir_candidates}; do
        if [ -d "${dir}" ]; then
            printf '%s\n' "${dir}"
        fi
    done
}

# check_pam is check 4. Every list it asserts against is derived from the
# installed policy, and a policy that yields no stacks or no modules is itself
# a failure: without that guard a mis-parse would pass vacuously.
check_pam() {
    [ -f "${pam_policy}" ] ||
        fail "${pam_policy} is missing after install"

    module_dirs=$(pam_module_dirs)
    [ -n "${module_dirs}" ] ||
        fail "no PAM module directory exists among the candidates: ${pam_module_dir_candidates}"

    stacks=$(pam_stacks)
    [ -n "${stacks}" ] ||
        fail "${pam_policy} names no stacks; the policy parse yielded nothing"

    modules=$(pam_modules)
    [ -n "${modules}" ] ||
        fail "${pam_policy} names no modules; the policy parse yielded nothing"

    for stack in ${stacks}; do
        [ -f "/etc/pam.d/${stack}" ] ||
            fail "${pam_policy} pulls in stack ${stack}, but /etc/pam.d/${stack} does not exist"
    done

    for module in ${modules}; do
        found=
        for dir in ${module_dirs}; do
            if [ -f "${dir}/${module}" ]; then
                found="${dir}/${module}"
                break
            fi
        done

        [ -n "${found}" ] ||
            fail "${pam_policy} loads ${module}, which exists in none of: ${module_dirs}"
    done

    printf 'verify-install: every stack and module named by %s exists\n' "${pam_policy}"
}

# expect_unit asserts one INSTALLED unit file is accepted by the distro's own
# systemd-analyze verify. That validator parses files offline, so it works in a
# container with no service manager running.
expect_unit() {
    [ -f "$1" ] ||
        fail "$1 is missing after install"

    systemd-analyze verify "$1" ||
        fail "systemd-analyze verify $1 failed"
}

# expect_linked asserts one INSTALLED binary's dynamic dependencies all
# resolve. It is applied only to the cgo-linked binary: the other binary is
# built with cgo disabled, so it is static and ldd exits non-zero for it for
# reasons that have nothing to do with the declared dependency lists.
expect_linked() {
    [ -f "$1" ] ||
        fail "$1 is missing after install"

    linkage=$(ldd "$1") ||
        fail "ldd $1 failed; $1 must be dynamically linked for this check to mean anything"

    unresolved=$(printf '%s\n' "${linkage}" | grep 'not found' || true)

    [ -z "${unresolved}" ] ||
        fail "$1 has unresolved dynamic dependencies:${unresolved}"
}

# expect_conffile and expect_removed each record one path for check 8. They
# print rather than assert, because the same set is asserted differently at
# different points of the removal sequence: a conffile is expected to survive a
# `dpkg -r` and to be gone after a `dpkg -P` or an `rpm -e`. Keeping the sets as
# one literal call per line lets the Go drift guards compare them against the
# live .goreleaser.yaml in both directions.
expect_conffile() {
    printf '%s\n' "$1"
}

expect_removed() {
    printf '%s\n' "$1"
}

# conffile_paths are the packaged `type: config` destinations - the files dpkg
# treats as conffiles and rpm may save as `.rpmsave`.
conffile_paths() {
    expect_conffile /etc/pam.d/pilothouse
    expect_conffile /etc/pilothouse/pilothouse.env
    expect_conffile /etc/pilothouse/pilothoused.env
}

# removed_paths are every packaged destination that is neither config nor a
# directory, plus the two build outputs. Neither manager keeps any of them, so
# all of them must be gone after any removal verb.
removed_paths() {
    expect_removed /usr/bin/pilothouse
    expect_removed /usr/bin/pilothoused
    expect_removed /usr/lib/systemd/system/pilothouse.service
    expect_removed /usr/lib/systemd/system/pilothoused.service
    expect_removed /usr/lib/sysusers.d/pilothouse.conf
}

# check_removed_paths_gone asserts the non-config payload is gone after the
# removal verb named by $1.
check_removed_paths_gone() {
    for path in $(removed_paths); do
        [ ! -e "${path}" ] ||
            fail "$1 left ${path} behind; it is not a config file and neither package manager keeps it"
    done

    printf 'verify-install: %s removed every non-config packaged path\n' "$1"
}

# check_conffiles_present asserts the conffiles SURVIVE the removal verb named
# by $1. Only Debian's `dpkg -r` is expected to behave this way.
check_conffiles_present() {
    for path in $(conffile_paths); do
        [ -e "${path}" ] ||
            fail "$1 deleted the conffile ${path}; a remove that is not a purge must preserve it"
    done

    printf 'verify-install: %s preserved every conffile\n' "$1"
}

# check_conffiles_gone asserts the conffiles are gone after the removal verb
# named by $1 - Debian's `dpkg -P` and Fedora's `rpm -e`.
check_conffiles_gone() {
    for path in $(conffile_paths); do
        [ ! -e "${path}" ] ||
            fail "$1 left the config file ${path} behind"
    done

    printf 'verify-install: %s removed every config file\n' "$1"
}

# check_no_rpmsave asserts no `.rpmsave` survives the removal verb named by $1.
# One would mean the postinstall modified a config file the package shipped,
# which is a defect worth catching here.
check_no_rpmsave() {
    # shellcheck disable=SC2086 # the candidate directories are a deliberate word-split list
    saved=$(find ${rpmsave_search_dirs} -name '*.rpmsave' 2>/dev/null || true)

    [ -z "${saved}" ] ||
        fail "$1 left a .rpmsave behind, so the install modified a config file the package shipped:${saved}"

    printf 'verify-install: %s left no .rpmsave file behind\n' "$1"
}

# check_account_survives_removal asserts the user and group outlive the removal
# verb named by $1. systemd-sysusers created them and neither manager owns them,
# so a future change that starts deleting them fails here.
check_account_survives_removal() {
    getent passwd "${removal_account}" >/dev/null ||
        fail "$1 deleted the ${removal_account} user; neither package manager owns the account"
    getent group "${removal_account}" >/dev/null ||
        fail "$1 deleted the ${removal_account} group; neither package manager owns the account"

    printf 'verify-install: the %s user and group survived %s\n' "${removal_account}" "$1"
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
check_pam

# Check 5: both installed unit files are accepted by the distro's own
# validator. One literal path per line, so the drift guard can parse them.
expect_unit /usr/lib/systemd/system/pilothouse.service
expect_unit /usr/lib/systemd/system/pilothoused.service

printf 'verify-install: both installed unit files pass systemd-analyze verify\n'

# Check 6: the cgo-linked binary resolves every shared library it needs, which
# is the check that proves the declared libpam and libsystemd dependencies are
# real. The other binary is deliberately absent here: it is built with cgo
# disabled and is therefore static.
expect_linked /usr/bin/pilothoused

printf 'verify-install: the cgo-linked binary resolves every dynamic dependency\n'

# Check 7: reinstall the SAME artifact over the existing install, then re-invoke
# checks 2 and 3. The functions are called again rather than their assertions
# copied, so the reinstalled state is held to exactly the same contract.
case "${format}" in
    deb)
        apt-get install -y --reinstall "${artifact}" ||
            fail "apt-get install -y --reinstall of ${artifact} failed"
        ;;
    rpm)
        dnf reinstall -y "${artifact}" ||
            fail "dnf reinstall of ${artifact} failed"
        ;;
esac

printf 'verify-install: reinstalled %s\n' "${artifact}"

check_account
check_owner_mode

printf 'verify-install: the account and on-disk metadata survived the reinstall\n'

# Check 8: removal, asserted per format. The account name is captured here
# because the sysusers file it is read from goes away with the package.
removal_account=$(sysusers_field name)

case "${format}" in
    deb)
        # Debian, verb one: a remove is not a purge, so the conffiles stay.
        dpkg -r "${package_name}" ||
            fail "dpkg -r ${package_name} failed"
        check_removed_paths_gone "dpkg -r"
        check_conffiles_present "dpkg -r"
        check_account_survives_removal "dpkg -r"

        # Debian, verb two: purge operates on the removed-but-unpurged
        # package, so no reinstall is needed between the two verbs.
        dpkg -P "${package_name}" ||
            fail "dpkg -P ${package_name} failed"
        check_conffiles_gone "dpkg -P"
        check_account_survives_removal "dpkg -P"
        ;;
    rpm)
        # Fedora: erase removes unmodified config files outright.
        rpm -e "${package_name}" ||
            fail "rpm -e ${package_name} failed"
        check_removed_paths_gone "rpm -e"
        check_conffiles_gone "rpm -e"
        check_no_rpmsave "rpm -e"
        check_account_survives_removal "rpm -e"
        ;;
esac

printf 'verify-install: post-removal state matches the %s expectations\n' "${format}"

printf 'verify-install: all checks passed\n'
