#!/bin/sh
set -eu

die() {
    printf 'bump: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "$2"
}

preflight() {
    require_command git 'Git is required.'
    require_command "${DOCKER:-docker}" 'Docker command is unavailable.'

    branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null) ||
        die 'detached HEAD is not releasable; switch to main.'
    [ "$branch" = main ] || die 'releases must run from main; switch to main.'
    [ -z "$(git status --porcelain)" ] ||
        die 'working tree is not clean; commit or stash changes.'
    git remote get-url origin >/dev/null 2>&1 || die 'origin remote is missing.'
    git fetch --tags origin '+refs/heads/main:refs/remotes/origin/main' >/dev/null 2>&1 ||
        die 'could not synchronize origin/main and tags.'

    local_head=$(git rev-parse HEAD)
    remote_head=$(git rev-parse refs/remotes/origin/main 2>/dev/null) ||
        die 'origin/main is unavailable after fetch.'
    [ "$local_head" = "$remote_head" ] && return 0

    if git merge-base --is-ancestor "$local_head" "$remote_head"; then
        die 'local main is behind origin/main; pull before bumping.'
    fi
    if git merge-base --is-ancestor "$remote_head" "$local_head"; then
        die 'local main is ahead of origin/main; push or reconcile before bumping.'
    fi
    die 'local main has diverged from origin/main; reconcile before bumping.'
}

case "${1:-release}" in
    preflight) preflight ;;
    release) die 'release orchestration is not implemented yet.' ;;
    *) die "unknown command: $1" ;;
esac
