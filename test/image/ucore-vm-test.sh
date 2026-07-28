#!/bin/bash
# Boot the two already-composed uCore fixtures from an official Fedora CoreOS
# QEMU bootstrap and prove the image-only
# contract: local image deployment, truthful host-image capabilities, enforcing
# SELinux without new AVC denials, and bootc update/rollback slot continuity.
#
# This is a consumer of fixture-ucore-images, not its lifecycle owner. It stops
# and waits for QEMU, empties the isolated store after exporting the fixtures,
# and leaves that empty store structure and fixture-ucore-vm directory in place.
# The outer issue-80 job owns the final exact-store reset and workspace removal.
set -euo pipefail
readonly PATH

SCRIPT_PATH="$(readlink -f -- "${BASH_SOURCE[0]}")"
readonly SCRIPT_PATH
SCRIPT_DIR="$(dirname "$SCRIPT_PATH")"
readonly SCRIPT_DIR
readonly GUEST_SCRIPT="${SCRIPT_DIR}/guest/validate-ucore.sh"
readonly DIGEST_PATTERN='^sha256:[0-9a-f]{64}$'
readonly REF_PATTERN='^localhost/pilothouse-image-test-[a-z0-9][a-z0-9-]{0,31}:(baseline|update)$'
readonly MAX_FCOS_UNCOMPRESSED_BYTES=4294967296
readonly MAX_FIXTURE_LAYOUT_BYTES=3221225472
readonly FIXTURE_DISK_BYTES=3758096384

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
    fail "the private composed-image store is root-owned; run this fixture consumer through sudo"

for tool in awk curl du grep jq mkfs.ext4 openssl podman qemu-img readlink \
    qemu-system-x86_64 scp sha256sum skopeo ssh ssh-keygen ss timeout truncate xz; do
    command -v "$tool" >/dev/null 2>&1 || fail "required tool is unavailable: $tool"
done
[[ -f "$GUEST_SCRIPT" && ! -L "$GUEST_SCRIPT" &&
   -s "$GUEST_SCRIPT" && -r "$GUEST_SCRIPT" ]] ||
    fail "guest validation script is missing, empty or not a regular file: $GUEST_SCRIPT"

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
    (.release.id | type) == "number" and
    (.release.asset_id | type) == "number" and
    (.release.tag | type) == "string" and
    (.release.artifact | type) == "string" and
    (
        (
            .release.pam_compatibility == "v0.6.0-debian-pam" and
            .release.id == 358276825 and
            .release.asset_id == 486354638 and
            .release.tag == "v0.6.0" and
            .release.artifact == "frostyard-pilothouse-0.6.0-1.x86_64.rpm"
        ) or
        (
            .release.pam_compatibility == "none" and
            (
                .release.id != 358276825 or
                .release.asset_id != 486354638 or
                .release.tag != "v0.6.0" or
                .release.artifact != "frostyard-pilothouse-0.6.0-1.x86_64.rpm"
            )
        )
    ) and
    .executables.source == "checked-out-head" and
    (.executables.pilothouse_sha256 | type) == "string" and
    (.executables.pilothouse_sha256 | test("^sha256:[0-9a-f]{64}$")) and
    (.executables.pilothoused_sha256 | type) == "string" and
    (.executables.pilothoused_sha256 | test("^sha256:[0-9a-f]{64}$")) and
    .source == "ghcr.io/ublue-os/ucore:latest" and
    .baseline.slot == "baseline" and
    .update.slot == "update"
' "$IMAGE_MANIFEST" >/dev/null ||
    fail "uCore fixture manifest has the wrong schema, release, source or slots"

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
readonly CONTAINERS_CONF CONTAINERS_STORAGE_CONF
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

skopeo_fixture() {
    TMPDIR="$image_tmpdir" timeout --signal=TERM --kill-after=30s "$1" \
        skopeo "${@:2}"
}

for ref_and_id in "$baseline_ref|$baseline_id" "$update_ref|$update_id"; do
    ref="${ref_and_id%%|*}"
    expected_id="${ref_and_id#*|}"
    actual_id="$(podman_fixture 2m image inspect --format '{{.Id}}' "$ref")"
    [[ "$actual_id" =~ ^[0-9a-f]{64}$ ]] && actual_id="sha256:${actual_id}"
    [[ "$actual_id" == "$expected_id" ]] ||
        fail "$ref no longer has its manifested image ID"
done

readonly FCOS_STREAM_URL="https://builds.coreos.fedoraproject.org/streams/stable.json"
readonly FCOS_ARCHIVE="${VM_DIR}/fcos.qcow2.xz"
readonly FCOS_BACKING="${VM_DIR}/fcos.qcow2"
readonly DISK_IMAGE="${VM_DIR}/disk.qcow2"
readonly IGNITION_CONFIG="${VM_DIR}/config.ign"
readonly FIXTURE_LAYOUT="${VM_DIR}/fixture-oci-layout"
readonly FIXTURE_DISK="${VM_DIR}/fixtures.ext4"
readonly FIXTURE_LABEL="PH_FIXTURE"
readonly GUEST_FIXTURE_DIR="/run/pilothouse-image-fixtures"
readonly GUEST_FIXTURE_DEVICE="/dev/disk/by-label/${FIXTURE_LABEL}"
readonly SSH_KEY="${VM_DIR}/id_ed25519"
readonly CREDENTIALS="${VM_DIR}/credentials.json"
readonly OVMF_CODE="${VM_DIR}/OVMF_CODE.fd"
readonly OVMF_VARS="${VM_DIR}/OVMF_VARS.fd"

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
    if kill -0 "$pid" 2>/dev/null; then
        return 1
    fi
    return 0
}

cleanup() {
    stop_qemu
}

cleanup_on_exit() {
    local status="$1"
    trap - EXIT
    cleanup || {
        echo "ucore-vm-test: cleanup did not fully stop QEMU" >&2
        [[ "$status" -ne 0 ]] || status=1
    }
    exit "$status"
}
trap 'cleanup_on_exit $?' EXIT

download_fcos_qemu() {
    local artifact url compressed_sha uncompressed_sha uncompressed_size
    artifact="$(
        curl -fsSL --retry 3 --retry-delay 5 \
            --proto '=https' --proto-redir '=https' \
            --connect-timeout 30 --max-time 120 --max-filesize 1048576 \
            "$FCOS_STREAM_URL" |
            jq -cer '.architectures.x86_64.artifacts.qemu.formats["qcow2.xz"].disk'
    )" || fail "could not resolve the Fedora CoreOS stable QEMU image"

    url="$(jq -er '.location | select(type == "string")' <<<"$artifact")" ||
        fail "Fedora CoreOS metadata has no QEMU image location"
    compressed_sha="$(jq -er '.sha256 | select(type == "string")' <<<"$artifact")" ||
        fail "Fedora CoreOS metadata has no compressed checksum"
    uncompressed_sha="$(jq -er '."uncompressed-sha256" | select(type == "string")' <<<"$artifact")" ||
        fail "Fedora CoreOS metadata has no uncompressed checksum"

    [[ "$url" =~ ^https://builds\.coreos\.fedoraproject\.org/.+\.qcow2\.xz$ ]] ||
        fail "Fedora CoreOS metadata returned an unexpected QEMU image URL"
    [[ "$compressed_sha" =~ ^[0-9a-f]{64}$ && "$uncompressed_sha" =~ ^[0-9a-f]{64}$ ]] ||
        fail "Fedora CoreOS metadata returned malformed checksums"

    curl -fL --retry 3 --retry-delay 5 \
        --proto '=https' --proto-redir '=https' \
        --connect-timeout 30 --max-time 1800 --max-filesize 2147483648 \
        --output "$FCOS_ARCHIVE" "$url" ||
        fail "could not download the Fedora CoreOS QEMU image"
    printf '%s  %s\n' "$compressed_sha" "$FCOS_ARCHIVE" |
        sha256sum --check --status ||
        fail "the compressed Fedora CoreOS QEMU image failed checksum verification"
    uncompressed_size="$(
        xz --robot --list "$FCOS_ARCHIVE" |
            awk -F '\t' '$1 == "file" {print $5}'
    )" || fail "could not read the Fedora CoreOS uncompressed size"
    [[ "$uncompressed_size" =~ ^[1-9][0-9]*$ ]] ||
        fail "Fedora CoreOS archive has an invalid uncompressed size"
    ((uncompressed_size <= MAX_FCOS_UNCOMPRESSED_BYTES)) ||
        fail "Fedora CoreOS archive exceeds the 4 GiB uncompressed limit"
    xz -dc -- "$FCOS_ARCHIVE" >"$FCOS_BACKING" ||
        fail "could not decompress the Fedora CoreOS QEMU image"
    printf '%s  %s\n' "$uncompressed_sha" "$FCOS_BACKING" |
        sha256sum --check --status ||
        fail "the Fedora CoreOS QEMU image failed its uncompressed checksum verification"
    rm -f -- "$FCOS_ARCHIVE" ||
        fail "could not remove the verified compressed Fedora CoreOS image"
}

create_ignition() {
    ssh-keygen -q -t ed25519 -N '' -C 'pilothouse-image-test' -f "$SSH_KEY"

    local public_key
    IFS= read -r public_key <"${SSH_KEY}.pub" ||
        fail "could not read the generated SSH public key"
    [[ "$public_key" == ssh-ed25519\ * ]] ||
        fail "the generated SSH public key has an unexpected format"

    jq -n --arg key "$public_key" '{
        ignition: {version: "3.4.0"},
        passwd: {users: [{
            name: "core",
            sshAuthorizedKeys: [$key]
        }]}
    }' >"$IGNITION_CONFIG" ||
        fail "could not create the Fedora CoreOS Ignition configuration"
    chmod 0600 "$IGNITION_CONFIG"
}

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

guest_sudo() {
    guest_run sudo -n "$@"
}

guest_sudo_long() {
    guest_run_long sudo -n "$@"
}

guest_run_timeout() {
    local duration="$1"
    shift
    timeout --signal=TERM --kill-after=10s "$duration" \
        ssh "${ssh_options[@]}" core@127.0.0.1 -- "$@"
}

guest_probe() {
    timeout --signal=TERM --kill-after=5s 15s \
        ssh "${ssh_options[@]}" core@127.0.0.1 -- "$@"
}

guest_copy() {
    local source="$1" destination="$2"
    timeout --signal=TERM --kill-after=10s 20m \
        scp "${ssh_common_options[@]}" -P "$ssh_port" -- "$source" "core@127.0.0.1:$destination"
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

wait_for_fixture_device() {
    local deadline=$((SECONDS + 60))
    while ((SECONDS < deadline)); do
        if [[ -n "${qemu_pid:-}" ]] && ! kill -0 "$qemu_pid" 2>/dev/null; then
            fail "QEMU exited before the fixture disk appeared"
        fi
        if guest_probe test -b "$GUEST_FIXTURE_DEVICE" >/dev/null 2>&1; then
            return 0
        fi
        sleep 2
    done
    fail "fixture disk did not appear within 60s after SSH"
}

wait_for_boot_id_change() {
    local before="$1" after deadline=$((SECONDS + 120))
    while ((SECONDS < deadline)); do
        if [[ -n "${qemu_pid:-}" ]] && ! kill -0 "$qemu_pid" 2>/dev/null; then
            fail "QEMU exited before the guest completed its reboot"
        fi
        if after="$(guest_probe cat /proc/sys/kernel/random/boot_id 2>/dev/null)" &&
            [[ -n "$after" && "$after" != "$before" ]]; then
            printf '%s\n' "$after"
            return 0
        fi
        sleep 2
    done
    fail "guest boot_id did not change from $before within 120s"
}

wait_for_broker() {
    local started=$SECONDS deadline=$((SECONDS + 120))
    while ((SECONDS < deadline)); do
        if guest_sudo test -S /run/pilothouse/broker.sock >/dev/null 2>&1; then
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
    output="$(guest_sudo systemctl reboot 2>&1)" || status=$?
    if [[ "$status" -ne 0 && "$status" -ne 255 && "$status" -ne 124 ]]; then
        fail "guest reboot command failed with status $status: $output"
    fi
    after="$(wait_for_boot_id_change "$before")"
    wait_for_broker
    log "guest completed reboot (boot_id $before -> $after)"
}

guest_status_digest() {
    local slot="$1"
    guest_sudo bootc status --format json |
        jq -er --arg slot "$slot" '.status[$slot].image.imageDigest // empty'
}

guest_status_name() {
    local slot="$1"
    guest_sudo bootc status --format json |
        jq -er --arg slot "$slot" '.status[$slot].image.image.image // empty'
}

guest_image_id() {
    local ref="$1" image_id
    image_id="$(guest_sudo podman image inspect --format '{{.Id}}' "$ref")"
    image_id="${image_id//[$'\r\n']/}"
    [[ "$image_id" =~ ^[0-9a-f]{64}$ ]] && image_id="sha256:$image_id"
    [[ "$image_id" =~ $DIGEST_PATTERN ]] ||
        fail "guest Podman returned a non-canonical image ID for $ref"
    printf '%s\n' "$image_id"
}

run_guest_validation() {
    local expected_slot="$1"
    guest_sudo sh /var/home/core/validate-ucore.sh validate "$expected_slot"
}

log "exporting the baseline and update fixtures to one shared local OCI layout"
mkdir -m 0700 -- "$FIXTURE_LAYOUT" ||
    fail "could not create the fixture OCI layout directory"
skopeo_fixture 20m copy \
    "containers-storage:${baseline_ref}" "oci:${FIXTURE_LAYOUT}:baseline"
skopeo_fixture 20m copy \
    "containers-storage:${update_ref}" "oci:${FIXTURE_LAYOUT}:update"
jq -e '
    [.manifests[].annotations["org.opencontainers.image.ref.name"]] |
    sort == ["baseline", "update"]
' "${FIXTURE_LAYOUT}/index.json" >/dev/null ||
    fail "the fixture OCI layout does not contain exactly the baseline and update refs"
for ref_and_id in "baseline|$baseline_id" "update|$update_id"; do
    ref="${ref_and_id%%|*}"
    expected_id="${ref_and_id#*|}"
    layout_config_id="$(
        skopeo_fixture 2m inspect --raw "oci:${FIXTURE_LAYOUT}:${ref}" |
            jq -er '.config.digest | select(type == "string")'
    )" || fail "could not inspect the $ref fixture in the OCI layout"
    [[ "$layout_config_id" == "$expected_id" ]] ||
        fail "the $ref OCI fixture config does not match its manifested image ID"
done
podman_fixture 10m image rm --all --force
remaining_images="$(podman_fixture 2m images --all --quiet)"
readonly remaining_images
[[ -z "$remaining_images" ]] ||
    fail "private fixture store still contains images after export"

fixture_layout_size="$(
    du --summarize --bytes -- "$FIXTURE_LAYOUT" |
        awk '{print $1}'
)"
[[ "$fixture_layout_size" =~ ^[1-9][0-9]*$ ]] ||
    fail "the fixture OCI layout has an invalid size"
[[ "$fixture_layout_size" -le "$MAX_FIXTURE_LAYOUT_BYTES" ]] ||
    fail "the fixture OCI layout exceeds the 3 GiB limit"
readonly fixture_layout_size
truncate --size="$FIXTURE_DISK_BYTES" -- "$FIXTURE_DISK" ||
    fail "could not allocate the sparse fixture disk"
mkfs.ext4 -F -q -m 0 -L "$FIXTURE_LABEL" -O ^has_journal \
    -d "$FIXTURE_LAYOUT" "$FIXTURE_DISK" ||
    fail "could not populate the read-only fixture disk"
rm -rf --one-file-system -- "$FIXTURE_LAYOUT" ||
    fail "could not remove the standalone fixture OCI layout"

log "downloading the checksum-verified Fedora CoreOS stable QEMU bootstrap"
download_fcos_qemu
create_ignition
qemu-img create -f qcow2 -F qcow2 -b "$FCOS_BACKING" "$DISK_IMAGE" >/dev/null
qemu-img resize "$DISK_IMAGE" 40G >/dev/null

firmware="$(find_ovmf)" || fail "OVMF CODE and VARS firmware files are unavailable"
cp -- "${firmware%%|*}" "$OVMF_CODE"
cp -- "${firmware#*|}" "$OVMF_VARS"

log "starting the official Fedora CoreOS bootstrap under QEMU/KVM"
qemu-system-x86_64 \
    -name pilothouse-ucore-image-test \
    -machine q35 \
    -accel kvm \
    -cpu host \
    -smp 2 \
    -m 4096 \
    -display none \
    -monitor none \
    -serial stdio \
    -drive "if=pflash,format=raw,unit=0,file=$OVMF_CODE,readonly=on" \
    -drive "if=pflash,format=raw,unit=1,file=$OVMF_VARS" \
    -drive "file=$DISK_IMAGE,format=qcow2,if=virtio" \
    -drive "file=$FIXTURE_DISK,format=raw,if=none,readonly=on,id=fixture" \
    -device virtio-blk-pci,drive=fixture,serial=pilothouse-fixture \
    -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:${ssh_port}-:22" \
    -device virtio-net-pci,netdev=net0 \
    -fw_cfg "name=opt/com.coreos/config,file=$IGNITION_CONFIG" \
    </dev/null &
readonly qemu_pid=$!
wait_for_ssh
wait_for_fixture_device

log "mounting the shared OCI fixture layout from its read-only attached disk"
guest_sudo mkdir -m 0700 -- "$GUEST_FIXTURE_DIR"
guest_sudo mount -t ext4 \
    -o ro,noload,nosuid,nodev,noexec,context=system_u:object_r:container_file_t:s0 \
    "$GUEST_FIXTURE_DEVICE" "$GUEST_FIXTURE_DIR"
guest_sudo_long skopeo copy \
    "oci:${GUEST_FIXTURE_DIR}:baseline" "containers-storage:${baseline_ref}"
guest_sudo_long skopeo copy \
    "oci:${GUEST_FIXTURE_DIR}:update" "containers-storage:${update_ref}"
guest_sudo umount "$GUEST_FIXTURE_DIR"
guest_sudo rmdir "$GUEST_FIXTURE_DIR"
loaded_baseline_id="$(guest_image_id "$baseline_ref")"
readonly loaded_baseline_id
[[ "$loaded_baseline_id" == "$baseline_id" ]] ||
    fail "the guest-loaded baseline image ID does not match the fixture manifest"
loaded_update_id="$(guest_image_id "$update_ref")"
readonly loaded_update_id
[[ "$loaded_update_id" == "$update_id" ]] ||
    fail "the guest-loaded update image ID does not match the fixture manifest"
guest_sudo_long bootc switch --quiet --transport containers-storage "$baseline_ref"
baseline_staged_name="$(guest_status_name staged)"
baseline_staged_digest="$(guest_status_digest staged)"
readonly baseline_staged_name baseline_staged_digest
[[ "$baseline_staged_name" == "$baseline_ref" && "$baseline_staged_digest" =~ $DIGEST_PATTERN ]] ||
    fail "bootc did not stage the exact baseline fixture"
reboot_guest
[[ "$(guest_status_digest booted)" == "$baseline_staged_digest" ]] ||
    fail "the staged baseline did not become the booted deployment"

password="$(openssl rand -hex 24)"
PILOTHOUSE_IMAGE_TEST_PASSWORD="$password" jq -n \
    '{username: "pilothouse-image-test", password: env.PILOTHOUSE_IMAGE_TEST_PASSWORD}' \
    >"$CREDENTIALS"
chmod 0600 "$CREDENTIALS"
unset password PILOTHOUSE_IMAGE_TEST_PASSWORD

guest_copy "$GUEST_SCRIPT" /var/home/core/validate-ucore.sh
guest_copy "$CREDENTIALS" /var/home/core/pilothouse-image-credentials.json
guest_run chmod 0700 /var/home/core/validate-ucore.sh
guest_run chmod 0600 /var/home/core/pilothouse-image-credentials.json
guest_sudo sh /var/home/core/validate-ucore.sh prepare

baseline_booted="$(guest_status_digest booted)"
readonly baseline_booted
run_guest_validation baseline

log "staging the already-verified private update through containers-storage"
guest_sudo_long bootc switch --quiet --transport containers-storage "$update_ref"
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
guest_sudo_long bootc rollback
reboot_guest
[[ "$(guest_status_digest booted)" == "$pre_rollback_target" ]] ||
    fail "bootc rollback did not boot the prior deployment"
[[ "$(guest_status_digest rollback)" == "$pre_rollback_booted" ]] ||
    fail "bootc rollback did not retain the rolled-back-from deployment"
run_guest_validation baseline

cleanup || fail "cleanup did not fully stop QEMU"
trap - EXIT
log "PASS: uCore baseline, update and rollback satisfied the image-host contract"
