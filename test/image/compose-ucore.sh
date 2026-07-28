#!/usr/bin/env bash
# Build two job-local uCore derivatives containing the verified released RPM.
# The caller owns the private workspace and removes it after the image test.
set -euo pipefail

readonly UCORE_REPOSITORY="ghcr.io/ublue-os/ucore"
readonly UCORE_DISCOVERY_REF="${UCORE_REPOSITORY}:latest"
readonly DIGEST_PATTERN='^sha256:[0-9a-f]{64}$'
readonly RAW_INDEX_LIMIT=$((4 * 1024 * 1024))
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly SCRIPT_DIR
readonly UCORE_DIR="${SCRIPT_DIR}/ucore"

usage() {
    echo "usage: compose-ucore.sh --workspace ABSOLUTE_PATH --run-id LOWERCASE_ID" >&2
    exit 2
}

workspace=""
run_id=""
while (($#)); do
    case "$1" in
        --workspace)
            (($# >= 2)) || usage
            workspace="$2"
            shift 2
            ;;
        --run-id)
            (($# >= 2)) || usage
            run_id="$2"
            shift 2
            ;;
        *)
            usage
            ;;
    esac
done

[[ -n "$workspace" && -n "$run_id" ]] || usage
[[ "$workspace" == /* && "$workspace" != */../* && "$workspace" != */./* ]] ||
    { echo "workspace must be an absolute clean path" >&2; exit 1; }
[[ -d "$workspace" && ! -L "$workspace" ]] ||
    { echo "workspace must be a real directory" >&2; exit 1; }
canonical_workspace="$(realpath -e -- "$workspace")"
[[ "$canonical_workspace" == "$workspace" ]] ||
    { echo "workspace must already be canonical" >&2; exit 1; }
[[ "$run_id" =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]] ||
    { echo "run id must match [a-z0-9][a-z0-9-]{0,31}" >&2; exit 1; }

for tool in awk cosign head jq podman sha256sum skopeo timeout; do
    command -v "$tool" >/dev/null ||
        { echo "required tool is unavailable: $tool" >&2; exit 1; }
done

readonly RPM_MANIFEST="${workspace}/fixture-release-rpm/fixture.json"
[[ -f "$RPM_MANIFEST" && ! -L "$RPM_MANIFEST" ]] ||
    { echo "released-RPM fixture manifest is missing or not a regular file" >&2; exit 1; }

rpm_metadata="$(
    jq -er '
        if .schema == 1 and .kind == "pilothouse-release-rpm-fixture" and
           (.artifact | type) == "string" and
           (.digest | type) == "string" and
           (.size | type) == "number"
        then [.artifact, .digest, (.size | tostring)] | @tsv
        else error("invalid released-RPM fixture manifest")
        end
    ' "$RPM_MANIFEST"
)"
IFS=$'\t' read -r artifact rpm_digest rpm_size <<< "$rpm_metadata"
[[ "$artifact" =~ ^frostyard-pilothouse-(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?-1\.x86_64\.rpm$ ]] ||
    { echo "released-RPM artifact name is invalid" >&2; exit 1; }
[[ "$rpm_digest" =~ $DIGEST_PATTERN && "$rpm_size" =~ ^[1-9][0-9]*$ ]] ||
    { echo "released-RPM digest or size is invalid" >&2; exit 1; }
((rpm_size <= 256 * 1024 * 1024)) ||
    { echo "released-RPM artifact exceeds 256 MiB" >&2; exit 1; }
readonly RPM_PATH="${workspace}/fixture-release-rpm/${artifact}"
[[ -f "$RPM_PATH" && ! -L "$RPM_PATH" ]] ||
    { echo "released-RPM artifact is missing or not a regular file" >&2; exit 1; }
[[ "$(stat -c %s -- "$RPM_PATH")" == "$rpm_size" ]] ||
    { echo "released-RPM artifact size no longer matches its manifest" >&2; exit 1; }
[[ "sha256:$(sha256sum -- "$RPM_PATH" | awk '{print $1}')" == "$rpm_digest" ]] ||
    { echo "released-RPM artifact digest no longer matches its manifest" >&2; exit 1; }

readonly OUTPUT_DIR="${workspace}/fixture-ucore-images"
mkdir -m 0700 -- "$OUTPUT_DIR" ||
    { echo "create fresh uCore fixture directory: $OUTPUT_DIR" >&2; exit 1; }

readonly RAW_INDEX="${OUTPUT_DIR}/index.json"
index_digest="$(
    timeout --signal=TERM --kill-after=10s 2m \
        skopeo inspect --format '{{.Digest}}' "docker://${UCORE_DISCOVERY_REF}"
)"
[[ "$index_digest" =~ $DIGEST_PATTERN ]] ||
    { echo "uCore latest did not resolve to a lowercase SHA-256 digest" >&2; exit 1; }

timeout --signal=TERM --kill-after=10s 2m \
    cosign verify --key "${UCORE_DIR}/cosign.pub" \
    "${UCORE_REPOSITORY}@${index_digest}" >/dev/null

set +e
timeout --signal=TERM --kill-after=10s 2m \
    skopeo inspect --raw "docker://${UCORE_REPOSITORY}@${index_digest}" |
    head -c "$((RAW_INDEX_LIMIT + 1))" > "$RAW_INDEX"
raw_index_status=("${PIPESTATUS[@]}")
set -e
raw_index_size="$(stat -c %s -- "$RAW_INDEX")"
((raw_index_size <= RAW_INDEX_LIMIT)) ||
    { echo "uCore index exceeds ${RAW_INDEX_LIMIT} bytes" >&2; exit 1; }
((raw_index_status[0] == 0 && raw_index_status[1] == 0)) ||
    { echo "failed to read the bounded uCore index" >&2; exit 1; }
((raw_index_size > 0)) ||
    { echo "uCore index is empty" >&2; exit 1; }

member_digest="$(
    jq -er '
        if .mediaType != "application/vnd.oci.image.index.v1+json" then
            error("uCore source is not an OCI index")
        else
            [
                .manifests[] |
                select(
                    (
                        .mediaType == "application/vnd.oci.image.manifest.v1+json" or
                        .mediaType == "application/vnd.docker.distribution.manifest.v2+json"
                    ) and
                    .platform.os == "linux" and
                    .platform.architecture == "amd64" and
                    ((.platform.variant // "") == "")
                ) |
                .digest
            ] |
            if length == 1 then .[0]
            else error("want exactly one linux/amd64 uCore member")
            end
        end
    ' "$RAW_INDEX"
)"
[[ "$member_digest" =~ $DIGEST_PATTERN ]] ||
    { echo "uCore linux/amd64 member has an invalid digest" >&2; exit 1; }

timeout --signal=TERM --kill-after=10s 2m \
    cosign verify --key "${UCORE_DIR}/cosign.pub" \
    "${UCORE_REPOSITORY}@${member_digest}" >/dev/null

readonly STORAGE_ROOT="${OUTPUT_DIR}/storage"
readonly IMAGE_STORE="${OUTPUT_DIR}/imagestore"
readonly RUN_ROOT="${OUTPUT_DIR}/runroot"
readonly PODMAN_TMPDIR="${OUTPUT_DIR}/libpod-tmp"
readonly IMAGE_TMPDIR="${OUTPUT_DIR}/image-tmp"
readonly STORAGE_CONF="${OUTPUT_DIR}/storage.conf"
readonly BASE_REF="${UCORE_REPOSITORY}@${member_digest}"
readonly IMAGE_PREFIX="localhost/pilothouse-image-test-${run_id}"
readonly BASELINE_REF="${IMAGE_PREFIX}:baseline"
readonly UPDATE_REF="${IMAGE_PREFIX}:update"
mkdir -m 0700 -- "$PODMAN_TMPDIR" "$IMAGE_TMPDIR"
unset \
    CONTAINER_CONNECTION \
    CONTAINER_HOST \
    CONTAINER_SSHKEY \
    CONTAINERS_CONF_OVERRIDE \
    STORAGE_DRIVER \
    STORAGE_OPTS
export CONTAINERS_CONF=/dev/null
jq -nr \
    --arg graphroot "$STORAGE_ROOT" \
    --arg imagestore "$IMAGE_STORE" \
    --arg runroot "$RUN_ROOT" \
    '"[storage]\n" +
     "driver = \"overlay\"\n" +
     "graphroot = \($graphroot | @json)\n" +
     "imagestore = \($imagestore | @json)\n" +
     "runroot = \($runroot | @json)\n" +
     "transient_store = false"' > "$STORAGE_CONF"
chmod 0600 "$STORAGE_CONF"
export CONTAINERS_STORAGE_CONF="$STORAGE_CONF"
podman_args=(
    --remote=false
    --root "$STORAGE_ROOT"
    --imagestore "$IMAGE_STORE"
    --runroot "$RUN_ROOT"
    --tmpdir "$PODMAN_TMPDIR"
    --events-backend none
    --storage-driver overlay
)

TMPDIR="$IMAGE_TMPDIR" timeout --signal=TERM --kill-after=30s 10m \
    podman "${podman_args[@]}" pull "$BASE_REF"

for slot in baseline update; do
    image_ref="${IMAGE_PREFIX}:${slot}"
    TMPDIR="$IMAGE_TMPDIR" timeout --signal=TERM --kill-after=30s 30m \
        podman "${podman_args[@]}" build \
        --pull=never \
        --network=none \
        --file "${UCORE_DIR}/Containerfile" \
        --tag "$image_ref" \
        --build-arg "UCORE_IMAGE=${BASE_REF}" \
        --build-arg "PILOTHOUSE_RPM=${artifact}" \
        --build-arg "IMAGE_TEST_SLOT=${slot}" \
        "${workspace}/fixture-release-rpm"
done

baseline_id="$(
    TMPDIR="$IMAGE_TMPDIR" timeout --signal=TERM --kill-after=10s 2m \
        podman "${podman_args[@]}" image inspect --format '{{.Id}}' "$BASELINE_REF"
)"
update_id="$(
    TMPDIR="$IMAGE_TMPDIR" timeout --signal=TERM --kill-after=10s 2m \
        podman "${podman_args[@]}" image inspect --format '{{.Id}}' "$UPDATE_REF"
)"
[[ "$baseline_id" =~ ^[0-9a-f]{64}$ ]] && baseline_id="sha256:${baseline_id}"
[[ "$update_id" =~ ^[0-9a-f]{64}$ ]] && update_id="sha256:${update_id}"
[[ "$baseline_id" =~ $DIGEST_PATTERN && "$update_id" =~ $DIGEST_PATTERN ]] ||
    { echo "derived image IDs are invalid" >&2; exit 1; }
[[ "$baseline_id" != "$update_id" ]] ||
    { echo "baseline and update image fixtures must be distinct" >&2; exit 1; }

readonly OUTPUT_MANIFEST="${OUTPUT_DIR}/fixture.json"
jq -n \
    --argjson producer_uid "$EUID" \
    --arg index "$index_digest" \
    --arg member "$member_digest" \
    --arg baseline_ref "$BASELINE_REF" \
    --arg baseline_id "$baseline_id" \
    --arg update_ref "$UPDATE_REF" \
    --arg update_id "$update_id" \
    --arg storage_root "$STORAGE_ROOT" \
    --arg image_store "$IMAGE_STORE" \
    --arg run_root "$RUN_ROOT" \
    --arg podman_tmpdir "$PODMAN_TMPDIR" \
    --arg image_tmpdir "$IMAGE_TMPDIR" \
    --arg storage_config "$STORAGE_CONF" \
    '{
        schema: 1,
        kind: "pilothouse-ucore-image-fixture",
        producer_uid: $producer_uid,
        source: "ghcr.io/ublue-os/ucore:latest",
        source_index_digest: $index,
        source_linux_amd64_digest: $member,
        baseline: {ref: $baseline_ref, id: $baseline_id, slot: "baseline"},
        update: {ref: $update_ref, id: $update_id, slot: "update"},
        storage: {
            root: $storage_root,
            imagestore: $image_store,
            runroot: $run_root,
            podman_tmpdir: $podman_tmpdir,
            image_tmpdir: $image_tmpdir,
            config: $storage_config
        }
    }' > "$OUTPUT_MANIFEST"
chmod 0600 "$OUTPUT_MANIFEST"
rm -f -- "$RAW_INDEX"

printf '%s\n' "$OUTPUT_MANIFEST"
