#!/bin/sh
set -eu

die() {
    printf 'bump: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "$2"
}

run_git() {
    "$git_command" "$@"
}

preflight() {
    require_command "$git_command" 'Git is required.'
    require_command "${DOCKER:-docker}" 'Docker command is unavailable.'

    branch=$(run_git symbolic-ref --quiet --short HEAD 2>/dev/null) ||
        die 'detached HEAD is not releasable; switch to main.'
    [ "$branch" = main ] || die 'releases must run from main; switch to main.'
    [ -z "$(run_git status --porcelain)" ] ||
        die 'working tree is not clean; commit or stash changes.'
    run_git remote get-url origin >/dev/null 2>&1 || die 'origin remote is missing.'
    fetch_failed=0
    run_git fetch --tags origin '+refs/heads/main:refs/remotes/origin/main' >/dev/null 2>&1 ||
        fetch_failed=1
    local_tag_refs=$(run_git for-each-ref --format='%(objectname) %(refname)' refs/tags | LC_ALL=C sort)
    if ! remote_tag_refs=$(run_git ls-remote --tags origin); then
        [ "$fetch_failed" -eq 0 ] || die 'could not synchronize origin/main and tags.'
        die 'could not compare local and origin tag refs.'
    fi
    remote_tag_refs=$(printf '%s\n' "$remote_tag_refs" |
        awk 'NF == 2 && $2 !~ /\^\{\}$/ { print $1 " " $2 }' | LC_ALL=C sort)
    [ "$local_tag_refs" = "$remote_tag_refs" ] ||
        die 'local and origin tag refs differ; reconcile tags before bumping.'
    [ "$fetch_failed" -eq 0 ] || die 'could not synchronize origin/main and tags.'

    local_head=$(run_git rev-parse HEAD)
    remote_head=$(run_git rev-parse refs/remotes/origin/main 2>/dev/null) ||
        die 'origin/main is unavailable after fetch.'
    [ "$local_head" = "$remote_head" ] && return 0

    if run_git merge-base --is-ancestor "$local_head" "$remote_head"; then
        die 'local main is behind origin/main; pull before bumping.'
    fi
    if run_git merge-base --is-ancestor "$remote_head" "$local_head"; then
        die 'local main is ahead of origin/main; push or reconcile before bumping.'
    fi
    die 'local main has diverged from origin/main; reconcile before bumping.'
}

validate_version() {
    version=$1
    printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
        die "svu returned invalid version: $version"
    [ "$(printf '%s\n' "$version" | wc -l | tr -d ' ')" = 1 ] ||
        die 'svu returned multiple version lines.'
}

remote_tag_output() {
    run_git ls-remote --tags origin "refs/tags/$1" "refs/tags/$1^{}"
}

remote_tag_commit() {
    version=$1
    output=$2
    peeled=$(printf '%s\n' "$output" | awk -v ref="refs/tags/$version^{}" '$2 == ref { print $1; exit }')
    [ -n "$peeled" ] && { printf '%s\n' "$peeled"; return; }
    printf '%s\n' "$output" | awk -v ref="refs/tags/$version" '$2 == ref { print $1; exit }'
}

release() {
    preflight

    verify_command=${BUMP_VERIFY_COMMAND:-'make --no-print-directory docker-bump-verify'}
    version_command=${BUMP_VERSION_COMMAND:-'make --silent --no-print-directory docker-next-version'}
    printf 'Running release verification...\n'
    sh -c "$verify_command" || die 'release verification failed; no tag was created.'

    version_file=$(mktemp)
    trap 'rm -f "$version_file"' EXIT HUP INT TERM
    sh -c "$version_command" >"$version_file" ||
        die 'could not calculate the next version; no tag was created.'
    line_count=$(awk 'END { print NR }' "$version_file")
    [ "$line_count" -eq 1 ] || die 'svu must return exactly one version line.'
    version=$(sed -n '1p' "$version_file")
    validate_version "$version"

    if run_git rev-parse --verify --quiet "refs/tags/$version" >/dev/null; then
        die "tag $version already exists locally."
    fi
    if ! before=$(remote_tag_output "$version"); then
        die "could not determine whether $version exists on origin."
    fi
    [ -z "$before" ] || die "tag $version already exists on origin."

    intended=$(run_git rev-parse HEAD)
    run_git tag -a "$version" -m "Version $version" ||
        die "could not create tag $version."
    if run_git push origin "refs/tags/$version"; then
        printf 'Published %s.\n' "$version"
        return 0
    fi

    if ! after=$(remote_tag_output "$version"); then
        die "push failed and publication state is indeterminate; local tag $version was preserved."
    fi
    remote_commit=$(remote_tag_commit "$version" "$after")
    if [ "$remote_commit" = "$intended" ]; then
        printf 'Published %s despite a reported transport failure.\n' "$version"
        return 0
    fi
    if [ -z "$after" ]; then
        run_git tag -d "$version" >/dev/null ||
            die "push failed; origin lacks $version, but the local tag could not be removed."
        die "push failed; origin lacks $version and the new local tag was removed. Retry make bump."
    fi
    die "push failed due to a remote tag conflict; local tag $version was preserved."
}

git_command=${BUMP_GIT_COMMAND:-git}

case "${1:-release}" in
    preflight) preflight ;;
    release) release ;;
    *) die "unknown command: $1" ;;
esac
