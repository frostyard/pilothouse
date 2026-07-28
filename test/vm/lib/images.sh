# images.sh — pinned cloud-image acquisition for the booted-VM harness (#67).
#
# This is a SOURCED library, not a program: it is committed non-executable and
# is never invoked as a command. Source it from a bash host-side script:
#
#     . test/vm/lib/images.sh
#     image="$(fetch_image debian "$WORKSPACE/images")"
#
# fetch_image downloads the family's pinned image over HTTPS into <cache-dir>,
# verifies it against the digest recorded in test/vm/images.env with that
# family's algorithm, and fails loudly — naming both the expected and the
# actual digest — on mismatch. A cached copy is re-verified before it is
# reused, so a truncated or tampered file in the cache cannot be booted.
#
# shellcheck shell=bash

set -euo pipefail

# image_log writes progress to standard error; fetch_image's standard output
# carries the image path and nothing else.
image_log() {
    printf 'images: %s\n' "$*" >&2
}

image_fail() {
    printf 'images: %s\n' "$*" >&2
    exit 1
}

# image_pin_table resolves the pinning table. VM_IMAGES_ENV overrides it; the
# default is images.env beside this library's parent directory.
image_pin_table() {
    if [ -n "${VM_IMAGES_ENV:-}" ]; then
        printf '%s\n' "$VM_IMAGES_ENV"
        return 0
    fi

    local lib_dir
    lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    printf '%s\n' "${lib_dir%/lib}/images.env"
}

# load_image_pin sets IMAGE_URL, IMAGE_ALGORITHM and IMAGE_DIGEST for a family.
# The table is data, never shell: VM_IMAGES_ENV may redirect the path, but every
# non-comment line must match the one assignment grammar below and every field
# may occur only once. Nothing from the table is evaluated or sourced.
load_image_pin() {
    local family="$1"
    [[ "$family" =~ ^[a-z0-9]+$ ]] ||
        image_fail "invalid image family '$family'"

    local table
    table="$(image_pin_table)"
    [ -f "$table" ] || image_fail "pinned image table not found: $table"

    IMAGE_URL=""
    IMAGE_ALGORITHM=""
    IMAGE_DIGEST=""

    local wanted="${family^^}"
    local line line_number=0 row_family field value key
    declare -A seen=()
    while IFS= read -r line || [ -n "$line" ]; do
        ((line_number += 1))
        [[ "$line" =~ ^[[:space:]]*$ ]] && continue
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        if [[ ! "$line" =~ ^VM_IMAGE_([A-Z0-9]+)_(URL|ALGORITHM|DIGEST)=\"([^\"]*)\"$ ]]; then
            image_fail "$table:$line_number is not a plain VM_IMAGE_<FAMILY>_<FIELD>=\"value\" assignment"
        fi

        row_family="${BASH_REMATCH[1]}"
        field="${BASH_REMATCH[2]}"
        value="${BASH_REMATCH[3]}"
        key="${row_family}_${field}"
        [ -z "${seen[$key]+x}" ] ||
            image_fail "$table:$line_number duplicates $key"
        seen["$key"]=1

        [ "$row_family" = "$wanted" ] || continue
        case "$field" in
            URL) IMAGE_URL="$value" ;;
            ALGORITHM) IMAGE_ALGORITHM="$value" ;;
            DIGEST) IMAGE_DIGEST="$value" ;;
        esac
    done <"$table"

    if [ -z "$IMAGE_URL" ] || [ -z "$IMAGE_ALGORITHM" ] || [ -z "$IMAGE_DIGEST" ]; then
        image_fail "no pinned image for family '$family' in $table"
    fi
}

# image_digest_of prints the digest of a file under the named algorithm.
image_digest_of() {
    local algorithm="$1" path="$2"

    case "$algorithm" in
        sha256) sha256sum "$path" | cut -d ' ' -f 1 ;;
        sha512) sha512sum "$path" | cut -d ' ' -f 1 ;;
        *) image_fail "unsupported checksum algorithm '$algorithm'" ;;
    esac
}

# verify_image compares a file against the pinned digest and fails naming both
# the expected and the actual value.
verify_image() {
    local path="$1" algorithm="$2" expected="$3"
    local actual
    actual="$(image_digest_of "$algorithm" "$path")"

    if [ "$actual" != "$expected" ]; then
        image_fail "$algorithm mismatch for $path: expected $expected, actual $actual"
    fi

    image_log "verified $path ($algorithm $actual)"
}

# fetch_image <family> <cache-dir> prints the path of the verified image.
fetch_image() {
    if [ "$#" -ne 2 ]; then
        image_fail "usage: fetch_image <family> <cache-dir>"
    fi

    local family="$1" cache_dir="$2"
    load_image_pin "$family"

    mkdir -p "$cache_dir"
    local target
    target="$cache_dir/$(basename "$IMAGE_URL")"

    if [ -f "$target" ]; then
        image_log "reusing cached $target; re-verifying before use"
        verify_image "$target" "$IMAGE_ALGORITHM" "$IMAGE_DIGEST"
        printf '%s\n' "$target"
        return 0
    fi

    image_log "downloading $IMAGE_URL"
    local partial="$target.partial"
    rm -f "$partial"
    if ! curl --fail --location --silent --show-error \
        --proto '=https' --tlsv1.2 --retry 3 --retry-delay 5 \
        --output "$partial" "$IMAGE_URL"; then
        rm -f "$partial"
        image_fail "download failed: $IMAGE_URL"
    fi

    verify_image "$partial" "$IMAGE_ALGORITHM" "$IMAGE_DIGEST"
    mv "$partial" "$target"
    printf '%s\n' "$target"
}
