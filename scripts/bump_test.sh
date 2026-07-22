#!/usr/bin/env bash
set -euo pipefail

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
mkdir "$ROOT/bin"
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

write_command() {
    local name=$1
    shift
    printf '#!/bin/sh\n%s\n' "$*" >"$ROOT/bin/$name"
    chmod +x "$ROOT/bin/$name"
    printf '%s\n' "$ROOT/bin/$name"
}

run_release() {
    local repo=$1 verify=$2 version=$3
    local git_command=${4:-git}
    (cd "$repo" && DOCKER=true BUMP_GIT_COMMAND="$git_command" \
        BUMP_VERIFY_COMMAND="$verify" BUMP_VERSION_COMMAND="$version" "$SCRIPT" release)
}

write_git_wrapper() {
    local name=$1 mode=$2
    local wrapper="$ROOT/bin/$name"
    printf '%s\n' '#!/bin/sh' 'set -eu' "mode='$mode'" \
        'if [ "$1" = push ]; then' \
        '    case "$mode" in' \
        '        absent|indeterminate) exit 41 ;;' \
        '        accepted) git "$@"; exit 41 ;;' \
        '        conflict)' \
        '            peer=$(mktemp -d)' \
        '            trap "rm -rf \"$peer\"" EXIT HUP INT TERM' \
        '            git clone "$(git remote get-url origin)" "$peer" >/dev/null 2>&1' \
        '            git -C "$peer" config user.name Test' \
        '            git -C "$peer" config user.email test@example.invalid' \
        '            git -C "$peer" commit --allow-empty -m competing-release >/dev/null' \
        '            git -C "$peer" tag -a v9.8.7 -m competing-release' \
        '            git -C "$peer" push origin refs/tags/v9.8.7 >/dev/null 2>&1' \
        '            exit 41 ;;' \
        '    esac' \
        'fi' \
        'if [ "$mode" = indeterminate ] && [ "$1" = ls-remote ] &&' \
        '   git rev-parse --verify --quiet refs/tags/v9.8.7 >/dev/null; then' \
        '    exit 42' \
        'fi' \
        'exec git "$@"' >"$wrapper"
    chmod +x "$wrapper"
    printf '%s\n' "$wrapper"
}

make_contracts() {
    grep -q '^SVU_VERSION ?= v3\.4\.1$' "$REPO_ROOT/Makefile" || fail 'pins svu version'
    grep -q '^bump-preflight:' "$REPO_ROOT/Makefile" || fail 'exposes bump preflight'
    grep -q '^bump-verify:' "$REPO_ROOT/Makefile" || fail 'exposes strict release verification'
    grep -q '^docker-bump-verify:' "$REPO_ROOT/Makefile" || fail 'exposes Docker verification'
    grep -q '^docker-next-version:' "$REPO_ROOT/Makefile" || fail 'exposes Docker svu calculation'
    grep -q '^docker-tools-check:' "$REPO_ROOT/Makefile" || fail 'exposes Docker tool smoke check'
    grep -q '^test-bump:' "$REPO_ROOT/Makefile" || fail 'runs bump harness'
    ! grep -q 'GIT_COMMON_DIR' "$REPO_ROOT/Makefile" || fail 'does not mount host Git metadata'
    ! grep -q '\$(shell.*git' "$REPO_ROOT/Makefile" || fail 'avoids parse-time Git commands'
    grep -q 'git clone --no-local' "$REPO_ROOT/Makefile" || fail 'prepares isolated bump inputs'
    grep -q 'rm -rf "$$source/.git"' "$REPO_ROOT/Makefile" || fail 'removes Git metadata from verification source'
    grep -q 'git -C "$$repo" remote remove origin' "$REPO_ROOT/Makefile" ||
        fail 'removes mirror remote configuration'
    grep -q 'target=/repository,readonly' "$REPO_ROOT/Makefile" ||
        fail 'mounts only the sanitized svu repository'
    awk '/^docker-next-version:/,/^docker-tools-check:/' "$REPO_ROOT/Makefile" |
        grep -q '^[[:space:]]*@set -eu;' || fail 'starts version calculation in strict mode'
    grep -q 'ARG SVU_VERSION' "$REPO_ROOT/.docker/Dockerfile" || fail 'Dockerfile accepts svu version'
    grep -q 'github.com/caarlos0/svu/v3@${SVU_VERSION}' "$REPO_ROOT/.docker/Dockerfile" ||
        fail 'Dockerfile installs pinned svu'
    pass 'Make and Docker contracts are present'
}

make_contracts

gofmt_failure=$(write_command gofmt-failure 'exit 37')
if make -s -C "$REPO_ROOT" format-check GOFMT="$gofmt_failure" GOFILES=ignored >/dev/null 2>&1; then
    fail 'preserves gofmt failures'
fi
pass 'preserves gofmt failures'

gofmt_dirty=$(write_command gofmt-dirty 'printf "%s\\n" dirty.go')
if make -s -C "$REPO_ROOT" format-check GOFMT="$gofmt_dirty" GOFILES=ignored >/dev/null 2>&1; then
    fail 'rejects unformatted Go files'
fi
pass 'rejects unformatted Go files'

repo=$(new_repo clean)
run_preflight "$repo" >/dev/null || fail 'accepts clean synchronized main'
pass 'accepts clean synchronized main'

repo=$(new_repo local-only-tag)
git -C "$repo" tag -a v1.2.3 -m local-only
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'accepts local-only semver tag'; fi
grep -q 'local and origin tag refs differ' "$ROOT/out" || fail 'explains local-only tag failure'
pass 'rejects local-only semver tag'

repo=$(new_repo mismatched-tag)
git -C "$repo" commit --allow-empty -m local-tag-target >/dev/null
git -C "$repo" tag -a v1.2.3 -m local-tag
git --git-dir="$ROOT/mismatched-tag.git" tag v1.2.3 "$(git -C "$repo" rev-parse HEAD~1)"
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'accepts mismatched semver tag'; fi
grep -q 'local and origin tag refs differ' "$ROOT/out" || fail 'explains mismatched tag failure'
pass 'rejects mismatched semver tag'

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

repo=$(new_repo missing-docker)
if (cd "$repo" && DOCKER=definitely-missing "$SCRIPT" preflight) >"$ROOT/out" 2>&1; then
    fail 'rejects missing Docker command'
fi
grep -q 'Docker command' "$ROOT/out" || fail 'explains missing Docker'
pass 'rejects missing Docker command'

repo=$(new_repo publish)
verify=$(write_command verify-success 'exit 0')
version=$(write_command version-success 'printf "%s\\n" v9.8.7')
(cd "$repo" && DOCKER=true BUMP_VERIFY_COMMAND="$verify" BUMP_VERSION_COMMAND="$version" "$SCRIPT") ||
    fail 'publishes valid version'
[ "$(git -C "$repo" rev-parse 'v9.8.7^{}')" = "$(git -C "$repo" rev-parse HEAD)" ] ||
    fail 'local tag points to HEAD'
git --git-dir="$ROOT/publish.git" rev-parse 'v9.8.7^{}' >/dev/null || fail 'remote tag exists'
[ "$(git -C "$repo" for-each-ref --format='%(contents)' refs/tags/v9.8.7)" = 'Version v9.8.7' ] ||
    fail 'tag has expected annotation'
pass 'publishes annotated tag'

repo=$(new_repo verify-failure)
marker="$ROOT/version-called"
verify=$(write_command verify-failure 'exit 23')
version=$(write_command version-marker "touch '$marker'; printf '%s\\n' v9.8.7")
if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
    fail 'stops after verification failure'
fi
[ ! -e "$marker" ] || fail 'version command ran after verification failure'
pass 'stops before version calculation when verification fails'

repo=$(new_repo missing-git-command)
if (cd "$repo" && DOCKER=true BUMP_GIT_COMMAND=definitely-missing "$SCRIPT" preflight) \
    >"$ROOT/out" 2>&1; then
    fail 'accepts missing Git command'
fi
grep -q 'Git is required' "$ROOT/out" || fail 'explains missing Git command'
pass 'rejects missing Git command'

for value in '' '1.2.3' 'v1.2.3-rc.1' $'v1.2.3\nv1.2.4'; do
    repo=$(new_repo "invalid-$RANDOM")
    verify=$(write_command "verify-$RANDOM" 'exit 0')
    version=$(write_command "version-$RANDOM" "printf '%s\\n' '$value'")
    if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
        fail "accepts invalid version: $value"
    fi
done
pass 'rejects empty, unprefixed, prerelease, and multiline versions'

repo=$(new_repo local-existing)
git -C "$repo" tag -a v9.8.7 -m existing
verify=$(write_command verify-local-existing 'exit 0')
version=$(write_command version-local-existing 'printf "%s\\n" v9.8.7')
if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
    fail 'accepts existing local tag'
fi
git -C "$repo" rev-parse v9.8.7 >/dev/null || fail 'deleted pre-existing local tag'
pass 'preserves and rejects existing local tag'

repo=$(new_repo remote-existing)
verify=$(write_command verify-remote-existing 'exit 0')
version=$(write_command version-remote-existing \
    'git tag -a v9.8.7 -m racing-release; git push origin v9.8.7 >/dev/null 2>&1; git tag -d v9.8.7 >/dev/null; printf "%s\\n" v9.8.7')
if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
    fail 'accepts existing remote tag'
fi
pass 'rejects existing remote tag'

repo=$(new_repo push-absent)
verify=$(write_command verify-push-absent 'exit 0')
version=$(write_command version-push-absent 'printf "%s\\n" v9.8.7')
wrapper=$(write_git_wrapper git-push-absent absent)
if run_release "$repo" "$verify" "$version" "$wrapper" >"$ROOT/out" 2>&1; then
    fail 'reports failed push as success'
fi
if git -C "$repo" rev-parse --verify refs/tags/v9.8.7 >/dev/null 2>&1; then
    fail 'retains new tag after confirmed remote absence'
fi
pass 'rolls back new local tag after confirmed absence'

repo=$(new_repo push-accepted)
verify=$(write_command verify-push-accepted 'exit 0')
version=$(write_command version-push-accepted 'printf "%s\\n" v9.8.7')
wrapper=$(write_git_wrapper git-push-accepted accepted)
run_release "$repo" "$verify" "$version" "$wrapper" >"$ROOT/out" 2>&1 ||
    fail 'does not recover accepted push'
git --git-dir="$ROOT/push-accepted.git" rev-parse 'v9.8.7^{}' >/dev/null ||
    fail 'accepted remote tag missing'
pass 'recognizes accepted push despite transport failure'

repo=$(new_repo push-conflict)
verify=$(write_command verify-push-conflict 'exit 0')
version=$(write_command version-push-conflict 'printf "%s\\n" v9.8.7')
wrapper=$(write_git_wrapper git-push-conflict conflict)
if run_release "$repo" "$verify" "$version" "$wrapper" >"$ROOT/out" 2>&1; then
    fail 'reports remote tag conflict as success'
fi
grep -q 'remote tag conflict' "$ROOT/out" || fail 'does not explain remote tag conflict'
grep -q 'local tag v9.8.7 was preserved' "$ROOT/out" || fail 'does not explain preserved local tag'
git -C "$repo" rev-parse v9.8.7 >/dev/null || fail 'deletes tag after remote tag conflict'
pass 'preserves local tag after remote tag conflict'

repo=$(new_repo push-indeterminate)
verify=$(write_command verify-push-indeterminate 'exit 0')
version=$(write_command version-push-indeterminate 'printf "%s\\n" v9.8.7')
wrapper=$(write_git_wrapper git-push-indeterminate indeterminate)
if run_release "$repo" "$verify" "$version" "$wrapper" >"$ROOT/out" 2>&1; then
    fail 'reports indeterminate push as success'
fi
grep -q 'indeterminate' "$ROOT/out" || fail 'does not explain indeterminate push'
git -C "$repo" rev-parse v9.8.7 >/dev/null || fail 'deletes tag in indeterminate state'
pass 'preserves local tag when remote state is indeterminate'
