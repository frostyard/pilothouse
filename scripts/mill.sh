#!/usr/bin/env bash
# Run the mill (workflows/mill.yaml) in an isolated git worktree.
#
#   scripts/mill.sh <issue#|spec-file> [--auto] [--no-pr] [--no-deep] [--fresh]
#
#   --auto     unattended: auto-approve human gates (conductor --skip-gates)
#   --no-pr    never push or open a PR, keep the branch local
#   --no-deep  final gate runs native tests instead of make docker-test
#   --fresh    discard an existing worktree for this source and start over
#
# The claude-agent-sdk provider ignores working_dir, so process cwd is the
# isolation boundary: this script creates .worktrees/mill-<id> on branch
# mill/<id> and runs conductor from inside it. The main checkout is never
# touched; a failed run is cleaned up with:  git worktree remove --force <dir>
set -euo pipefail

usage() { grep '^#' "$0" | sed 's/^# \{0,1\}//' | head -12; exit 1; }

[ $# -ge 1 ] || usage
SOURCE="$1"; shift
AUTO=0 OPEN_PR=true DEEP=true FRESH=0
for arg in "$@"; do
    case "$arg" in
        --auto)    AUTO=1 ;;
        --no-pr)   OPEN_PR=false ;;
        --no-deep) DEEP=false ;;
        --fresh)   FRESH=1 ;;
        *) usage ;;
    esac
done

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

if [[ "$SOURCE" =~ ^[0-9]+$ ]]; then
    ID="issue-$SOURCE"
else
    [ -f "$SOURCE" ] || { echo "spec file not found: $SOURCE" >&2; exit 1; }
    SOURCE=$(realpath "$SOURCE")
    ID=$(basename "$SOURCE" | tr -c 'a-zA-Z0-9' '-' | sed 's/-*$//' | cut -c1-40)
fi
WT="$ROOT/.worktrees/mill-$ID"
BRANCH="mill/$ID"

if [ "$FRESH" = 1 ] && [ -d "$WT" ]; then
    git worktree remove --force "$WT"
    git branch -D "$BRANCH" 2>/dev/null || true
fi

if [ -d "$WT" ]; then
    echo "→ reusing existing worktree $WT (use --fresh to start over)"
else
    git worktree add "$WT" -b "$BRANCH"
fi

cd "$WT"
FLAGS=()
[ "$AUTO" = 1 ] && FLAGS+=(--skip-gates)

exec conductor run workflows/mill.yaml \
    -i "source=$SOURCE" \
    -i "deep_gate=$DEEP" \
    -i "open_pr=$OPEN_PR" \
    --log-file auto \
    "${FLAGS[@]}"
