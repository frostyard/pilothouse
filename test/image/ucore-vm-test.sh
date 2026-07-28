#!/bin/bash
# Boot the two already-composed uCore fixtures and prove the image-only
# contract: composefs installation, truthful host-image capabilities, enforcing
# SELinux without new AVC denials, and bootc update/rollback slot continuity.
#
# This is a consumer of fixture-ucore-images, not its owner. It stops and waits
# for every process it starts, detaches every loop backed by its disk, and leaves
# the private Podman store and fixture-ucore-vm directory in place. The outer
# issue-80 job owns the final exact-store reset and workspace removal.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly GUEST_SCRIPT="${SCRIPT_DIR}/guest/validate-ucore.sh"
readonly DIGEST_PATTERN='^sha256:[0-9a-f]{64}$'
readonly REF_PATTERN='^localhost/pilothouse-image-test-[a-z0-9][a-z0-9-]{0,31}:(baseline|update)$'

usage() {
    echo "usage: ucore-vm-test.sh --workspace ABSOLUTE_PATH [--ssh-port 2222]" >&2
}

fail() {
    echo "ucore-vm-test: $*" >&2
    exit 1
}

log() {
    echo "ucore-vm-test: $*"
}

workspace=""
ssh_port="2222"
while (($#)); do
    case "$1" in
        --workspace)
            (($# >= 2)) || { usage; exit 2; }
            workspace="$2"
            shift 2
            ;;
        --ssh-port)
            (($# >= 2)) || { usage; exit 2; }
            ssh_port="$2"
            shift 2
            ;;
        *)
            usage
            exit 2
            ;;
    esac
done

[[ -n "$workspace" && "$workspace" == /* && -d "$workspace" ]] ||
    fail "--workspace must name an existing absolute directory"
canonical_workspace="$(cd "$workspace" && pwd -P)"
[[ "$workspace" == "$canonical_workspace" ]] ||
    fail "--workspace must be its canonical absolute path"
if [[ ! "$ssh_port" =~ ^[0-9]+$ ]] ||
   ((ssh_port < 1024 || ssh_port > 65535)); then
    fail "--ssh-port must be an integer from 1024 through 65535"
fi
readonly workspace canonical_workspace ssh_port
[[ $EUID -eq 0 ]] ||
    fail "bootc install and loop-device setup require root; run this fixture consumer through sudo"

for tool in awk grep jq losetup openssl podman \
    qemu-system-x86_64 scp sed ssh ssh-keygen ss timeout truncate; do
    command -v "$tool" >/dev/null 2>&1 || fail "required tool is unavailable: $tool"
done
[[ -r "$GUEST_SCRIPT" ]] || fail "guest validation script is missing: $GUEST_SCRIPT"

if ss -H -ltn "sport = :${ssh_port}" | grep -q .; then
    fail "TCP port $ssh_port is already in use"
fi

readonly IMAGE_DIR="${workspace}/fixture-ucore-images"
readonly IMAGE_MANIFEST="${IMAGE_DIR}/fixture.json"
readonly VM_DIR="${workspace}/fixture-ucore-vm"
[[ -f "$IMAGE_MANIFEST" && ! -L "$IMAGE_MANIFEST" ]] ||
    fail "uCore fixture manifest is missing or is not a regular file: $IMAGE_MANIFEST"
mkdir -m 0700 -- "$VM_DIR" ||
    fail "create fresh VM fixture directory: $VM_DIR"
jq -e '
    .schema == 1 and
    .kind == "pilothouse-ucore-image-fixture" and
    .producer_uid == 0 and
    .source == "ghcr.io/ublue-os/ucore:latest" and
    .baseline.slot == "baseline" and
    .update.slot == "update"
' "$IMAGE_MANIFEST" >/dev/null ||
    fail "uCore fixture manifest has the wrong schema, kind, source or slots"

manifest_value() {
    local expression="$1"
    jq -er "$expression | select(type == \"string\" and length > 0)" "$IMAGE_MANIFEST"
}

baseline_ref="$(manifest_value '.baseline.ref')"
baseline_id="$(manifest_value '.baseline.id')"
update_ref="$(manifest_value '.update.ref')"
update_id="$(manifest_value '.update.id')"
storage_root="$(manifest_value '.storage.root')"
image_store="$(manifest_value '.storage.imagestore')"
run_root="$(manifest_value '.storage.runroot')"
podman_tmpdir="$(manifest_value '.storage.podman_tmpdir')"
image_tmpdir="$(manifest_value '.storage.image_tmpdir')"
storage_config="$(manifest_value '.storage.config')"
readonly baseline_ref baseline_id update_ref update_id
readonly storage_root image_store run_root podman_tmpdir image_tmpdir storage_config

[[ "$baseline_ref" =~ $REF_PATTERN && "$baseline_ref" == *:baseline ]] ||
    fail "fixture baseline ref is invalid"
[[ "$update_ref" =~ $REF_PATTERN && "$update_ref" == *:update ]] ||
    fail "fixture update ref is invalid"
[[ "${baseline_ref%:*}" == "${update_ref%:*}" ]] ||
    fail "fixture refs do not share one private image-test prefix"
[[ "$baseline_id" =~ $DIGEST_PATTERN && "$update_id" =~ $DIGEST_PATTERN ]] ||
    fail "fixture image IDs are invalid"
[[ "$baseline_id" != "$update_id" ]] ||
    fail "baseline and update fixture IDs must be distinct"

assert_storage_path() {
    local actual="$1" expected="$2"
    [[ "$actual" == "$expected" ]] ||
        fail "fixture storage path escaped its fixed workspace location: $actual"
}
assert_storage_path "$storage_root" "${IMAGE_DIR}/storage"
assert_storage_path "$image_store" "${IMAGE_DIR}/imagestore"
assert_storage_path "$run_root" "${IMAGE_DIR}/runroot"
assert_storage_path "$podman_tmpdir" "${IMAGE_DIR}/libpod-tmp"
assert_storage_path "$image_tmpdir" "${IMAGE_DIR}/image-tmp"
assert_storage_path "$storage_config" "${IMAGE_DIR}/storage.conf"
[[ -f "$storage_config" && ! -L "$storage_config" ]] ||
    fail "fixture storage configuration is missing or is not a regular file"

unset \
    CONTAINER_CONNECTION \
    CONTAINER_HOST \
    CONTAINER_SSHKEY \
    CONTAINERS_CONF_OVERRIDE \
    STORAGE_DRIVER \
    STORAGE_OPTS
export CONTAINERS_CONF=/dev/null
export CONTAINERS_STORAGE_CONF="$storage_config"
podman_args=(
    --remote=false
    --root "$storage_root"
    --imagestore "$image_store"
    --runroot "$run_root"
    --tmpdir "$podman_tmpdir"
    --events-backend none
    --storage-driver overlay
)
readonly -a podman_args

podman_fixture() {
    TMPDIR="$image_tmpdir" timeout --signal=TERM --kill-after=30s "$1" \
        podman "${podman_args[@]}" "${@:2}"
}

for ref_and_id in "$baseline_ref|$baseline_id" "$update_ref|$update_id"; do
    ref="${ref_and_id%%|*}"
    expected_id="${ref_and_id#*|}"
    actual_id="$(podman_fixture 2m image inspect --format '{{.Id}}' "$ref")"
    [[ "$actual_id" =~ ^[0-9a-f]{64}$ ]] && actual_id="sha256:${actual_id}"
    [[ "$actual_id" == "$expected_id" ]] ||
        fail "$ref no longer has its manifested image ID"
done

readonly DISK_IMAGE="${VM_DIR}/disk.raw"
readonly UPDATE_ARCHIVE="${VM_DIR}/update.oci"
readonly SSH_KEY="${VM_DIR}/id_ed25519"
readonly CREDENTIALS="${VM_DIR}/credentials.json"
readonly OVMF_CODE="${VM_DIR}/OVMF_CODE.fd"
readonly OVMF_VARS="${VM_DIR}/OVMF_VARS.fd"
readonly INSTALL_CONTAINER="pilothouse-image-install-${ssh_port}"

stop_qemu() {
    local pid="${qemu_pid:-}"
    [[ -n "$pid" ]] || return 0

    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        for _ in {1..20}; do
            kill -0 "$pid" 2>/dev/null || break
            sleep 0.5
        done
        if kill -0 "$pid" 2>/dev/null; then
            kill -KILL "$pid" 2>/dev/null || true
        fi
    fi
    wait "$pid" 2>/dev/null || true
    kill -0 "$pid" 2>/dev/null && return 1
}

remove_install_container() {
    podman_fixture 2m rm --force --ignore "$INSTALL_CONTAINER" >/dev/null
}

detach_disk_loops() {
    local failed=0 loop listing remaining
    listing="$(losetup -j "$DISK_IMAGE" 2>/dev/null)" || return 1
    while IFS= read -r loop; do
        [[ -n "$loop" ]] || continue
        timeout --signal=TERM --kill-after=10s 30s \
            losetup --detach "$loop" || failed=1
    done < <(awk -F: '{print $1}' <<<"$listing")

    remaining="$(losetup -j "$DISK_IMAGE" 2>/dev/null)" || failed=1
    [[ -z "$remaining" ]] || failed=1
    return "$failed"
}

cleanup() {
    local failed=0
    stop_qemu || failed=1
    remove_install_container || failed=1
    detach_disk_loops || failed=1
    return "$failed"
}

cleanup_on_exit() {
    local status="$1"
    trap - EXIT
    cleanup || {
        echo "ucore-vm-test: cleanup did not fully stop processes and detach the VM disk" >&2
        [[ "$status" -ne 0 ]] || status=1
    }
    exit "$status"
}
trap 'cleanup_on_exit $?' EXIT

find_ovmf() {
    local pair code vars
    for pair in \
        "/usr/share/OVMF/OVMF_CODE_4M.fd|/usr/share/OVMF/OVMF_VARS_4M.fd" \
        "/usr/share/OVMF/OVMF_CODE.fd|/usr/share/OVMF/OVMF_VARS.fd" \
        "/usr/share/edk2/ovmf/OVMF_CODE.fd|/usr/share/edk2/ovmf/OVMF_VARS.fd" \
        "/usr/share/qemu/OVMF_CODE.fd|/usr/share/qemu/OVMF_VARS.fd"; do
        code="${pair%%|*}"
        vars="${pair#*|}"
        if [[ -r "$code" && -r "$vars" ]]; then
            printf '%s|%s\n' "$code" "$vars"
            return 0
        fi
    done
    return 1
}

ssh_common_options=(
    -o BatchMode=yes
    -o StrictHostKeyChecking=no
    -o UserKnownHostsFile=/dev/null
    -o GlobalKnownHostsFile=/dev/null
    -o LogLevel=ERROR
    -o ConnectTimeout=10
    -i "$SSH_KEY"
)
ssh_options=(
    "${ssh_common_options[@]}"
    -p "$ssh_port"
)

guest_run() {
    guest_run_timeout 2m "$@"
}

guest_run_long() {
    guest_run_timeout 20m "$@"
}

guest_run_timeout() {
    local duration="$1"
    shift
    timeout --signal=TERM --kill-after=10s "$duration" \
        ssh "${ssh_options[@]}" root@127.0.0.1 -- "$@"
}

guest_probe() {
    timeout --signal=TERM --kill-after=5s 15s \
        ssh "${ssh_options[@]}" root@127.0.0.1 -- "$@"
}

guest_copy() {
    local source="$1" destination="$2"
    timeout --signal=TERM --kill-after=10s 20m \
        scp "${ssh_common_options[@]}" -P "$ssh_port" -- "$source" "root@127.0.0.1:$destination"
}

wait_for_ssh() {
    local started=$SECONDS deadline=$((SECONDS + 300))
    while ((SECONDS < deadline)); do
        if [[ -n "${qemu_pid:-}" ]] && ! kill -0 "$qemu_pid" 2>/dev/null; then
            fail "QEMU exited before the guest answered SSH"
        fi
        if guest_probe true >/dev/null 2>&1; then
            log "guest answered SSH after $((SECONDS - started))s"
            return 0
        fi
        sleep 5
    done
    fail "guest did not answer SSH within 300s"
}

wait_for_ssh_gone() {
    local misses=0 deadline=$((SECONDS + 120))
    while ((SECONDS < deadline)); do
        if guest_probe true >/dev/null 2>&1; then
            misses=0
        else
            misses=$((misses + 1))
            ((misses >= 3)) && return 0
        fi
        sleep 2
    done
    fail "pre-reboot sshd was still answering after 120s"
}

wait_for_broker() {
    local started=$SECONDS deadline=$((SECONDS + 120))
    while ((SECONDS < deadline)); do
        if guest_probe test -S /run/pilothouse/broker.sock >/dev/null 2>&1; then
            log "broker socket became ready after $((SECONDS - started))s"
            return 0
        fi
        sleep 2
    done
    fail "broker socket did not become ready within 120s after SSH"
}

reboot_guest() {
    local before after output status=0
    before="$(guest_run cat /proc/sys/kernel/random/boot_id)"
    output="$(guest_run systemctl reboot 2>&1)" || status=$?
    if [[ "$status" -ne 0 && "$status" -ne 255 && "$status" -ne 124 ]]; then
        fail "guest reboot command failed with status $status: $output"
    fi
    wait_for_ssh_gone
    wait_for_ssh
    wait_for_broker
    after="$(guest_run cat /proc/sys/kernel/random/boot_id)"
    [[ -n "$before" && -n "$after" && "$before" != "$after" ]] ||
        fail "guest answered after reboot without changing boot_id"
}

guest_status_digest() {
    local slot="$1"
    guest_run bootc status --format json |
        jq -er --arg slot "$slot" '.status[$slot].image.imageDigest // empty'
}

guest_status_name() {
    local slot="$1"
    guest_run bootc status --format json |
        jq -er --arg slot "$slot" '.status[$slot].image.image.image // empty'
}

run_guest_validation() {
    local expected_slot="$1"
    guest_run sh /root/validate-ucore.sh validate "$expected_slot"
}

log "creating sparse VM disk and installing the baseline with composefs"
truncate -s 20G "$DISK_IMAGE"
ssh-keygen -q -t ed25519 -N '' -C 'pilothouse-image-test' -f "$SSH_KEY"
podman_fixture 45m run \
    --rm \
    --name "$INSTALL_CONTAINER" \
    --privileged \
    --pid=host \
    --volume /dev:/dev \
    --volume "$workspace:$workspace" \
    --volume "${SSH_KEY}.pub:/run/pilothouse-image-test-key.pub:ro" \
    --security-opt label=type:unconfined_t \
    --env CONTAINERS_CONF=/dev/null \
    --env "CONTAINERS_STORAGE_CONF=$storage_config" \
    --env "TMPDIR=$image_tmpdir" \
    "$baseline_ref" \
    bootc install to-disk \
    --generic-image \
    --via-loopback \
    --skip-fetch-check \
    --composefs-backend \
    --filesystem btrfs \
    --karg console=ttyS0 \
    --root-ssh-authorized-keys /run/pilothouse-image-test-key.pub \
    "$DISK_IMAGE"

detach_disk_loops ||
    fail "bootc install returned with a loop device still attached to the private disk"

log "exporting the update fixture to a job-local OCI archive"
podman_fixture 20m save --format oci-archive --output "$UPDATE_ARCHIVE" "$update_ref"
[[ -s "$UPDATE_ARCHIVE" ]] || fail "Podman produced an empty update archive"

firmware="$(find_ovmf)" || fail "OVMF CODE and VARS firmware files are unavailable"
cp -- "${firmware%%|*}" "$OVMF_CODE"
cp -- "${firmware#*|}" "$OVMF_VARS"

log "starting the baseline uCore guest under QEMU/KVM"
qemu-system-x86_64 \
    -name pilothouse-ucore-image-test \
    -machine q35 \
    -accel kvm \
    -cpu host \
    -smp 2 \
    -m 3072 \
    -display none \
    -monitor none \
    -serial stdio \
    -drive "if=pflash,format=raw,unit=0,file=$OVMF_CODE,readonly=on" \
    -drive "if=pflash,format=raw,unit=1,file=$OVMF_VARS" \
    -drive "file=$DISK_IMAGE,format=raw,if=virtio" \
    -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:${ssh_port}-:22" \
    -device virtio-net-pci,netdev=net0 \
    </dev/null &
readonly qemu_pid=$!
wait_for_ssh
wait_for_broker

password="$(openssl rand -hex 24)"
PILOTHOUSE_IMAGE_TEST_PASSWORD="$password" jq -n \
    '{username: "pilothouse-image-test", password: env.PILOTHOUSE_IMAGE_TEST_PASSWORD}' \
    >"$CREDENTIALS"
chmod 0600 "$CREDENTIALS"
unset password PILOTHOUSE_IMAGE_TEST_PASSWORD

guest_copy "$GUEST_SCRIPT" /root/validate-ucore.sh
guest_copy "$CREDENTIALS" /root/pilothouse-image-credentials.json
guest_run chmod 0700 /root/validate-ucore.sh
guest_run chmod 0600 /root/pilothouse-image-credentials.json
guest_run sh /root/validate-ucore.sh prepare

baseline_booted="$(guest_status_digest booted)"
readonly baseline_booted
run_guest_validation baseline

log "loading the private update archive and staging it through containers-storage"
guest_copy "$UPDATE_ARCHIVE" /var/tmp/pilothouse-image-update.oci
guest_run_long podman load --input /var/tmp/pilothouse-image-update.oci
guest_run rm -f /var/tmp/pilothouse-image-update.oci
guest_run_long bootc switch --quiet --transport containers-storage "$update_ref"
staged_name="$(guest_status_name staged)"
staged_digest="$(guest_status_digest staged)"
readonly staged_name staged_digest
[[ "$staged_name" == "$update_ref" && "$staged_digest" =~ $DIGEST_PATTERN ]] ||
    fail "bootc did not stage the expected update fixture"

reboot_guest
[[ "$(guest_status_digest booted)" == "$staged_digest" ]] ||
    fail "the staged update did not become the booted deployment"
[[ "$(guest_status_digest rollback)" == "$baseline_booted" ]] ||
    fail "the prior baseline did not become the rollback deployment"
run_guest_validation update

log "rolling back and proving the deployment slots reverse"
pre_rollback_booted="$(guest_status_digest booted)"
pre_rollback_target="$(guest_status_digest rollback)"
readonly pre_rollback_booted pre_rollback_target
guest_run_long bootc rollback
reboot_guest
[[ "$(guest_status_digest booted)" == "$pre_rollback_target" ]] ||
    fail "bootc rollback did not boot the prior deployment"
[[ "$(guest_status_digest rollback)" == "$pre_rollback_booted" ]] ||
    fail "bootc rollback did not retain the rolled-back-from deployment"
run_guest_validation baseline

cleanup || fail "cleanup did not fully stop processes and detach the VM disk"
trap - EXIT
log "PASS: uCore baseline, update and rollback satisfied the image-host contract"
