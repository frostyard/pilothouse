# Bump Tag Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `make bump` automatically reconcile moved and remote-only tags from authoritative `origin` values while continuing to reject local-only tags.

**Architecture:** Keep tag synchronization in the host-side `scripts/bump.sh` preflight. Replace the non-forcing tag fetch with an explicit forced tag refspec, then retain the direct local/remote ref comparison as a safety check for local-only or otherwise unreconciled state.

**Tech Stack:** POSIX shell, Bash test harness, Git, GNU Make, Docker.

## Global Constraints

- Releases still require a clean local `main` exactly equal to freshly fetched `origin/main`.
- `origin` is authoritative for existing and remote-only tag refs.
- Local-only tags must be preserved and rejected, not deleted automatically.
- Docker must receive neither host Git identity/configuration nor live `.git` data.
- No verification command may create or push a tag to the project origin.
- Relevant release guidance must be updated in `README.md`, `AGENTS.md`, and `yeti/OVERVIEW.md`; no `CLAUDE.md` exists in this checkout.

---

### Task 1: Reconcile Authoritative Origin Tags

**Files:**
- Modify: `scripts/bump_test.sh:117-133`
- Modify: `scripts/bump.sh:27-39`
- Modify: `README.md:57-70`
- Modify: `AGENTS.md:29-32`
- Modify: `yeti/OVERVIEW.md:201-205`
- Modify: `docs/superpowers/specs/2026-07-21-bump-workflow-design.md`

**Interfaces:**
- Consumes: `run_preflight <repo>` and `new_repo <name>` from `scripts/bump_test.sh`; Git remote named `origin`.
- Produces: Preflight behavior that force-fetches `refs/tags/*` from `origin`, updates moved local tags, fetches remote-only tags, and leaves local-only tags untouched for the existing parity check to reject.

- [ ] **Step 1: Add a failing moved-tag regression test**

Insert this test after the clean synchronized-main case and before the local-only-tag case in `scripts/bump_test.sh`:

```bash
repo=$(new_repo moved-tag)
git -C "$repo" tag dev
git -C "$repo" push origin refs/tags/dev >/dev/null 2>&1
git -C "$repo" commit --allow-empty -m moved-tag-target >/dev/null
git -C "$repo" push origin main >/dev/null 2>&1
git --git-dir="$ROOT/moved-tag.git" update-ref refs/tags/dev "$(git -C "$repo" rev-parse HEAD)"
run_preflight "$repo" >/dev/null || fail 'reconciles moved origin tag'
[ "$(git -C "$repo" rev-parse refs/tags/dev)" = "$(git --git-dir="$ROOT/moved-tag.git" rev-parse refs/tags/dev)" ] ||
    fail 'does not update moved local tag'
pass 'reconciles moved origin tag'
```

- [ ] **Step 2: Run the focused harness and verify the regression fails**

Run:

```bash
make test-bump
```

Expected: FAIL at `reconciles moved origin tag`, with preflight reporting that local and origin tag refs differ.

- [ ] **Step 3: Force-fetch authoritative origin tag refs**

Replace the fetch in `scripts/bump.sh` with explicit forced branch and tag refspecs:

```sh
    fetch_failed=0
    run_git fetch --no-prune origin \
        '+refs/heads/main:refs/remotes/origin/main' \
        '+refs/tags/*:refs/tags/*' >/dev/null 2>&1 ||
        fetch_failed=1
```

`--no-prune` overrides host Git pruning configuration so local-only tags remain
available for the subsequent `for-each-ref`/`ls-remote` comparison to reject.
Do not remove that comparison.

- [ ] **Step 4: Run the focused harness and verify all cases pass**

Run:

```bash
make test-bump
```

Expected: PASS, including `reconciles moved origin tag` and `rejects local-only semver tag`.

- [ ] **Step 5: Update user and agent release documentation**

In `README.md`, extend the release preflight description after the clean-main requirements:

```markdown
Before verification, preflight force-updates local tag refs from authoritative
`origin` values, so moved and remote-only tags are reconciled automatically.
Local-only tags are preserved and rejected rather than silently deleted.
```

In `AGENTS.md`, extend the `make bump` guidance with:

```markdown
Preflight treats `origin` as authoritative for moved and remote-only tags, but
preserves and rejects local-only tags.
```

In `yeti/OVERVIEW.md`, add this release-tooling note after the native build dependency paragraph:

```markdown
**Release tooling:** `make bump` runs host-side Git preflight and publication
around containerized verification and `svu`. Preflight force-fetches tag refs
from authoritative `origin`, reconciling moved and remote-only tags, while
preserving and rejecting local-only tags.
```

Keep `docs/superpowers/specs/2026-07-21-bump-workflow-design.md` aligned with the implemented refspec and test behavior. It must state that moved and remote-only tags reconcile automatically and local-only tags remain preserved and rejected.

- [ ] **Step 6: Run required verification**

Run each command independently:

```bash
make build
make test
make fmt
make lint
make test-bump
make docker-bump-verify
make docker-lint
make --silent docker-next-version
git diff --check
```

Expected: every command exits zero. `make --silent docker-next-version` emits exactly one line matching `^v[0-9]+\.[0-9]+\.[0-9]+$`; no command creates or pushes a release tag.

- [ ] **Step 7: Review the final diff**

Run:

```bash
git status --short
git diff -- scripts/bump.sh scripts/bump_test.sh README.md AGENTS.md yeti/OVERVIEW.md docs/superpowers/specs/2026-07-21-bump-workflow-design.md
```

Expected: only the intended fetch, regression test, and documentation changes appear. Confirm no generated files, release tags, or unrelated worktree changes were introduced.
