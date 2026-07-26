#!/usr/bin/env bash
#
# vm-boot-test.sh — the booted-VM harness's one entry point (Layer B, #67).
#
# Usage:
#   test/vm/vm-boot-test.sh --family debian|fedora --artifact-dir <dir>
#
# It is meant to be run through an explicit interpreter
# (`bash test/vm/vm-boot-test.sh`) and is also committed executable: scp does
# not preserve the executable bit without -p, so a harness that relied on the
# copied mode alone would be one layer down from the same defect. Both
# mechanisms, so neither is a single point of failure. Nothing in the tree calls
# this script yet — the CI job that does lands later.
#
# What it does, in order:
#
#   1. fetch and verify the family's pinned cloud image (images.sh);
#   2. generate the run-time credentials and build the NoCloud seed
#      (cloudinit.sh);
#   3. boot under QEMU/KVM, gate on serial-console boot output, wait for sshd
#      (vm.sh + ssh.sh);
#   4. probe `sudo -n true` in the guest, so a broken NOPASSWD grant fails once
#      here rather than obscurely inside a later guest script;
#   5. create the administrator-writable staging directory ~/vm-boot (0700)
#      and ~/vm-boot/guest/;
#   6. select the arch-qualified artifact — exactly one — from --artifact-dir;
#   7. stage the artifact, the guest scripts and creds.env into that directory;
#   8. install the credentials privileged into /root and remove the staged copy;
#   9. run the guest bootstrap as `sudo -n sh ~/vm-boot/guest/install-package.sh`;
#  10. run the activation checks as
#      `sudo -n sh ~/vm-boot/guest/check-activation.sh`, which enables and
#      starts both units, asserts the systemd-created directories and the
#      broker socket, and proves the broker is live;
#  11. run the PAM checks as `sudo -n sh ~/vm-boot/guest/check-pam.sh`, which
#      proves the installed unit's administrator group, authenticates a real
#      non-root administrator end to end and proves both negatives from the
#      journal;
#  12. run the journald read-back as
#      `sudo -n sh ~/vm-boot/guest/check-journal.sh`, which asks the broker's
#      own journal query for the daemon's unit over the authenticated socket
#      route and asserts a line the daemon itself emitted comes back in the
#      response.
#
# The harness has exactly ONE SSH identity in the guest: the administrator
# account cloud-init created. That account cannot write /root, cannot install
# packages and cannot read a 0600 root-owned file, so everything goes through an
# administrator-writable staging directory plus explicit escalation. Every
# guest-bound destination is inside ~/vm-boot, and every guest script runs as
# `sudo -n sh <staged path>`.
#
# At this commit the run ends once the daemon has read a line it emitted itself
# back through the broker's journal query: the reboot posture lands in a later
# commit.
#
# Every tilde path in this file is deliberately quoted: it is transmitted
# literally and expanded by the GUEST's shell, because ~/vm-boot is the
# administrator account's home in the guest and not any directory on the
# runner. That is why SC2088 is disabled at those call sites.
#
# shellcheck shell=bash

set -euo pipefail

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUEST_SCRIPT_DIR="$HARNESS_DIR/guest"

# shellcheck source=/dev/null
. "$HARNESS_DIR/lib/images.sh"
# shellcheck source=/dev/null
. "$HARNESS_DIR/lib/cloudinit.sh"
# shellcheck source=/dev/null
. "$HARNESS_DIR/lib/vm.sh"
# shellcheck source=/dev/null
. "$HARNESS_DIR/lib/ssh.sh"
# shellcheck source=/dev/null
. "$HARNESS_DIR/lib/diagnostics.sh"

# VM_RUN_DIR holds everything this run generates — the keypair, creds.env, the
# seed and the overlay disk. VM_IMAGE_CACHE_DIR is separate so a verified base
# image survives between families and runs.
VM_RUN_DIR="${VM_RUN_DIR:-${RUNNER_TEMP:-/tmp}/pilothouse-vm-boot}"
VM_IMAGE_CACHE_DIR="${VM_IMAGE_CACHE_DIR:-${RUNNER_TEMP:-/tmp}/pilothouse-vm-images}"

orchestrator_log() {
    printf 'vm-boot-test: %s\n' "$*"
}

orchestrator_fail() {
    printf 'vm-boot-test: %s\n' "$*" >&2
    exit 1
}

usage() {
    printf 'usage: %s --family debian|fedora --artifact-dir <dir>\n' "$0" >&2
}

# parse_arguments sets FAMILY and ARTIFACT_DIR. An unrecognised family is a
# named failure, not a fallback: this tier covers exactly the two distro
# families the container install matrix already covers, amd64 only.
parse_arguments() {
    FAMILY=""
    ARTIFACT_DIR=""

    while [ "$#" -gt 0 ]; do
        case "$1" in
            --family)
                [ "$#" -ge 2 ] || { usage; orchestrator_fail "--family needs a value"; }
                FAMILY="$2"
                shift 2
                ;;
            --artifact-dir)
                [ "$#" -ge 2 ] || { usage; orchestrator_fail "--artifact-dir needs a value"; }
                ARTIFACT_DIR="$2"
                shift 2
                ;;
            *)
                usage
                orchestrator_fail "unknown argument '$1'"
                ;;
        esac
    done

    case "$FAMILY" in
        debian | fedora) ;;
        "")
            usage
            orchestrator_fail "--family is required: expected debian or fedora"
            ;;
        *)
            usage
            orchestrator_fail "unknown family '$FAMILY': expected debian or fedora"
            ;;
    esac

    [ -n "$ARTIFACT_DIR" ] || { usage; orchestrator_fail "--artifact-dir is required"; }
    [ -d "$ARTIFACT_DIR" ] || orchestrator_fail "artifact directory '$ARTIFACT_DIR' does not exist or is not a directory"

    ARTIFACT_DIR="$(cd "$ARTIFACT_DIR" && pwd)"
}

# package_format_for_family maps a family to the package format its guest's own
# package manager consumes.
package_format_for_family() {
    case "$1" in
        debian) printf '%s\n' 'deb' ;;
        fedora) printf '%s\n' 'rpm' ;;
        *) orchestrator_fail "unknown family '$1': expected debian or fedora" ;;
    esac
}

# select_artifact prints the one artifact to install. The `packages` job
# uploads both an amd64 and an arm64 file per format, so the glob is
# arch-qualified — `*_amd64.deb` for Debian, `*.x86_64.rpm` for Fedora — and
# then exactly one match is required, failing with the count and the matched
# basenames otherwise. This is the same rule packaging/verify-install.sh
# applies for Layer A; an unqualified glob over the whole directory would match
# two files and silently install whichever sorted last.
select_artifact() {
    local family="$1" artifact_dir="$2"
    local format
    format="$(package_format_for_family "$family")"

    case "$format" in
        deb) set -- "$artifact_dir"/*_amd64.deb ;;
        rpm) set -- "$artifact_dir"/*.x86_64.rpm ;;
    esac

    local candidate selected="" names=""
    local count=0

    for candidate in "$@"; do
        [ -f "$candidate" ] || continue

        selected="$candidate"
        count=$((count + 1))
        names="$names $(basename "$candidate")"
    done

    if [ "$count" -ne 1 ]; then
        orchestrator_fail "expected exactly one amd64 $format artifact in $artifact_dir, found ${count}:${names:-" (none)"}"
    fi

    printf '%s\n' "$selected"
}

# require_passwordless_escalation is the top-of-run probe. Every later step —
# installing the package, starting the units, reading the credentials file,
# talking to the broker socket — escalates non-interactively, so a missing
# NOPASSWD grant must be reported here, once, by name.
require_passwordless_escalation() {
    orchestrator_log "probing non-interactive privilege escalation in the guest"

    if ! guest_run sudo -n true; then
        orchestrator_fail "assertion failed: 'sudo -n true' was rejected in the guest; the administrator account has no working NOPASSWD grant and every later step needs one"
    fi
}

# create_guest_staging creates the staging directory as the administrator
# account, before anything is copied. That account cannot write /root and
# cannot install packages, so this directory is where every guest-bound file
# lands; privileged placement happens afterwards, explicitly.
# shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
create_guest_staging() {
    guest_run mkdir -p '~/vm-boot/guest'
    guest_run chmod 0700 '~/vm-boot'
    guest_run chmod 0700 '~/vm-boot/guest'

    orchestrator_log "created the guest staging directory ~/vm-boot (mode 0700) and ~/vm-boot/guest/"
}

# stage_artifact copies the selected artifact under a fixed staged name, so the
# guest script has nothing to select: selection happened on the host, under the
# arch-qualified rule.
# shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
stage_artifact() {
    local artifact="$1" format="$2"

    guest_copy "$artifact" "~/vm-boot/pilothouse-artifact.$format"
    orchestrator_log "staged $(basename "$artifact") as ~/vm-boot/pilothouse-artifact.$format"
}

# stage_guest_scripts copies every guest script into the staging directory. The
# copied mode is deliberately irrelevant: scp does not preserve the executable
# bit without -p, and each script is invoked through an explicit interpreter.
# shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
stage_guest_scripts() {
    local script staged=0

    for script in "$GUEST_SCRIPT_DIR"/*.sh; do
        [ -f "$script" ] || continue

        guest_copy "$script" '~/vm-boot/guest/'
        staged=$((staged + 1))
    done

    [ "$staged" -gt 0 ] || orchestrator_fail "no guest scripts found in $GUEST_SCRIPT_DIR"
    orchestrator_log "staged $staged guest scripts into ~/vm-boot/guest/"
}

# install_guest_credentials places the generated credentials where only root
# can read them. The staging directory is administrator-writable by
# construction, so the staged copy is removed immediately afterwards: the
# generated root password must not linger in a file the unprivileged account
# can read. No credential is ever passed as a command-line argument.
# shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
install_guest_credentials() {
    guest_copy "$VM_CREDS_ENV" '~/vm-boot/creds.env'
    guest_run sudo -n install -o root -g root -m 0600 '~/vm-boot/creds.env' /root/.pilothouse-vm-creds
    guest_run rm -f '~/vm-boot/creds.env'

    orchestrator_log "installed the credentials as /root/.pilothouse-vm-creds (0600) and removed the staged copy"
}

main() {
    parse_arguments "$@"

    local format
    format="$(package_format_for_family "$FAMILY")"

    create_run_workspace "$VM_RUN_DIR"
    install_failure_diagnostics

    local image
    image="$(fetch_image "$FAMILY" "$VM_IMAGE_CACHE_DIR")"

    create_seed "$FAMILY" "$VM_WORKSPACE"
    boot_guest "$FAMILY" "$image" "$VM_SEED_ISO" "$VM_WORKSPACE"

    require_passwordless_escalation
    create_guest_staging

    local artifact
    artifact="$(select_artifact "$FAMILY" "$ARTIFACT_DIR")"
    orchestrator_log "selected artifact $artifact"

    stage_artifact "$artifact" "$format"
    stage_guest_scripts
    install_guest_credentials

    # shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
    guest_run sudo -n sh '~/vm-boot/guest/install-package.sh'

    # shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
    guest_run sudo -n sh '~/vm-boot/guest/check-activation.sh'

    # shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
    guest_run sudo -n sh '~/vm-boot/guest/check-pam.sh'

    # shellcheck disable=SC2088 # expanded by the guest's shell, not the runner's
    guest_run sudo -n sh '~/vm-boot/guest/check-journal.sh'

    orchestrator_log "$FAMILY guest booted, package installed, both units active, the broker answering on its socket, PAM authenticating a real non-root administrator and the daemon reading its own journal record back through the broker"
}

main "$@"
