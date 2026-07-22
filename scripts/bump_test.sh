#!/usr/bin/env bash
set -euo pipefail

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
SCRIPT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/bump.sh
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PASS=0

fail() { printf 'not ok - %s\n' "$1" >&2; exit 1; }
pass() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }

new_repo() {
    local name=$1
    local remote="$ROOT/$name.git"
    local repo="$ROOT/$name"
    git init --bare --initial-branch=main "$remote" >/dev/null
    git clone "$remote" "$repo" >/dev/null 2>&1
    git -C "$repo" config user.name Test
    git -C "$repo" config user.email test@example.invalid
    printf 'initial\n' >"$repo/file.txt"
    git -C "$repo" add file.txt
    git -C "$repo" commit -m initial >/dev/null
    git -C "$repo" push -u origin main >/dev/null 2>&1
    printf '%s\n' "$repo"
}

run_preflight() {
    local repo=$1
    shift
    (cd "$repo" && DOCKER=true "$SCRIPT" preflight "$@")
}

repo=$(new_repo clean)
run_preflight "$repo" >/dev/null || fail 'accepts clean synchronized main'
pass 'accepts clean synchronized main'

git -C "$repo" switch -c feature >/dev/null
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects feature branch'; fi
grep -q 'switch to main' "$ROOT/out" || fail 'explains branch failure'
pass 'rejects feature branch'

git -C "$repo" switch main >/dev/null
printf 'dirty\n' >>"$repo/file.txt"
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects dirty tree'; fi
grep -q 'commit or stash' "$ROOT/out" || fail 'explains dirty failure'
git -C "$repo" restore file.txt
pass 'rejects dirty tree'

repo=$(new_repo detached)
git -C "$repo" checkout --detach >/dev/null
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects detached HEAD'; fi
grep -q 'detached HEAD' "$ROOT/out" || fail 'explains detached HEAD failure'
pass 'rejects detached HEAD'

repo=$(new_repo no-origin)
git -C "$repo" remote remove origin
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects missing origin'; fi
grep -q 'origin remote is missing' "$ROOT/out" || fail 'explains missing origin'
pass 'rejects missing origin'

repo=$(new_repo unreachable)
git -C "$repo" remote set-url origin "$ROOT/does-not-exist.git"
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects unreachable origin'; fi
grep -q 'synchronize origin' "$ROOT/out" || fail 'explains fetch failure'
pass 'rejects unreachable origin'

advance_remote() {
    local repo=$1
    local name=$2
    local peer="$ROOT/$name-peer"
    git clone "$(git -C "$repo" remote get-url origin)" "$peer" >/dev/null 2>&1
    git -C "$peer" config user.name Test
    git -C "$peer" config user.email test@example.invalid
    printf '%s\n' "$name" >>"$peer/file.txt"
    git -C "$peer" commit -am "$name" >/dev/null
    git -C "$peer" push origin main >/dev/null 2>&1
}

repo=$(new_repo behind)
advance_remote "$repo" behind
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects behind main'; fi
grep -q 'behind' "$ROOT/out" || fail 'explains behind state'
pass 'rejects behind main'

repo=$(new_repo ahead)
printf 'ahead\n' >>"$repo/file.txt"
git -C "$repo" commit -am ahead >/dev/null
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects ahead main'; fi
grep -q 'ahead' "$ROOT/out" || fail 'explains ahead state'
pass 'rejects ahead main'

repo=$(new_repo diverged)
printf 'local\n' >>"$repo/file.txt"
git -C "$repo" commit -am local >/dev/null
advance_remote "$repo" remote
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects diverged main'; fi
grep -q 'diverged' "$ROOT/out" || fail 'explains diverged state'
pass 'rejects diverged main'

if DOCKER=definitely-missing "$SCRIPT" preflight >"$ROOT/out" 2>&1; then
    fail 'rejects missing Docker command'
fi
grep -q 'Docker command' "$ROOT/out" || fail 'explains missing Docker'
pass 'rejects missing Docker command'
