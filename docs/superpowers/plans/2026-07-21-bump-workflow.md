# Reproducible Bump Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely verify, calculate, create, and push a release tag with host Git
and pinned Docker tooling.

**Architecture:** `scripts/bump.sh` owns host Git preflight and tag publication.
Docker verification consumes a temporary clean clone with `.git` removed;
version calculation consumes a temporary sanitized bare mirror with `origin`
removed and a read-only mount. Docker never receives live host Git metadata or
configuration.

**Tech Stack:** GNU Make, POSIX shell, Git, Docker, Go 1.26.5,
golangci-lint v2.11.4, svu v3.4.1.

## Global Constraints

- Releases require a clean local `main` exactly equal to freshly fetched
  `origin/main`.
- Freshly fetched local direct tag refs must exactly equal origin direct tag
  refs, including object IDs; ignore only peeled `^{}` ls-remote records.
- Docker receives neither host Git identity/configuration nor live `.git` data.
- `svu next` stdout must be exactly one `^v[0-9]+\.[0-9]+\.[0-9]+$` line.
- Tests use temporary repositories and fake commands; they never mutate the
  developer checkout or contact its origin.
- No verification command creates or pushes a release tag.

---

### Task 1: Host Preflight And Publication

**Files:**
- Modify: `scripts/bump.sh`
- Modify: `scripts/bump_test.sh`

- [ ] Fetch `origin/main` and tags before release work.
- [ ] Compare sorted local direct tag `object-id ref` records against
  `git ls-remote --tags origin` direct records, excluding only peeled refs.
- [ ] Keep exact local `HEAD`/`origin/main` equality and clear ahead, behind,
  and diverged diagnostics.
- [ ] Validate versions solely with `^v[0-9]+\.[0-9]+\.[0-9]+$`.
- [ ] Preserve a newly created local tag when a failed push reveals a non-empty
  remote tag resolving to another commit, reporting a remote tag conflict.

### Task 2: Docker Isolation

**Files:**
- Modify: `Makefile`
- Test: `scripts/bump_test.sh`

- [ ] Build `docker-bump-verify` input with `git clone --no-local`, remove the
  clone's `.git`, and mount only the resulting source tree.
- [ ] Build `docker-next-version` input with `git clone --mirror --no-local`,
  remove `origin`, and mount the bare mirror read-only at `/repository`.
- [ ] Do not mount a live checkout `.git`, use `GIT_COMMON_DIR`, or expose a
  host `.git/config` to Docker.

### Task 3: Final Verification

**Files:**
- Modify: `docs/superpowers/specs/2026-07-21-bump-workflow-design.md`
- Modify: `docs/superpowers/plans/2026-07-21-bump-workflow.md`
- Create: `.superpowers/sdd/final-fix-report.md`

- [ ] Run `make test-bump`, `make docker-bump-verify`, and `make docker-lint`.
- [ ] Run `make --silent docker-next-version`; record its one-line stdout and
  verify it matches `^v[0-9]+\.[0-9]+\.[0-9]+$`.
- [ ] Confirm no release tag was created or pushed, review the diff, and record
  commits, command results, concerns, and finding resolutions.
