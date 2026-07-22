# Storage Final Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct the final-review defects in managed remote storage without expanding the broker protocol.

**Architecture:** Compose one `SystemRemoteManager` for both the storage query and fixed actions. Its lifecycle synchronization uses a global Create lock and per-definition locks while snapshot reads remain independent. Snapshot normalization retains only valid reference-bearing findings after truncation.

**Tech Stack:** Go, testify, templ, systemd D-Bus abstraction, Docker Make targets.

## Global Constraints

- Keep privileged operations behind existing fixed broker query/actions only.
- Write regression tests before each production behavior change and observe RED.
- Run `make generate` after the templ change; never edit generated templ Go.
- Preserve secret sentinel/audit coverage and no generic broker surface.
- Finish with race tests, Docker build/test/fmt/lint, ignored final report, clean status, and commits.

---

### Task 1: Composition And Parser Regressions

**Files:** `cmd/pilothoused/main.go`, `cmd/pilothoused/main_test.go`, `internal/modules/storage/core.go`, `internal/modules/storage/core_test.go`

- [ ] Write a composition test that registers the same `SystemRemoteManager` for query/actions, creates an inactive needs-attention definition, executes the query, and asserts `Managed`, `remote:<id>`, finding, and `managedMountID`.
- [ ] Run the new composition test and observe it fail because the query is registered with the inner manager.
- [ ] Construct the remote manager before `registerStorage` and pass it to both registrations.
- [ ] Write an lsblk fixture/test for absent and null `size`, plus malformed present size rejection; observe RED.
- [ ] Change the device size representation to tolerate JSON null/absent as zero but retain strict validation for non-null input.
- [ ] Run focused daemon/storage tests and commit the composition/parser fix.

### Task 2: Remote Rendering And Lifecycle Safety

**Files:** `internal/modules/storage/units.go`, `internal/modules/storage/units_test.go`, `internal/modules/storage/remote_manager.go`, `internal/modules/storage/remote_manager_test.go`

- [ ] Add exact guest and credentialed SMB mount-unit golden tests; observe guest failure.
- [ ] Add `guest` for SMB definitions with no username and no guest option for credentialed definitions.
- [ ] Add an interrupted Delete resume test which fails after the manifest must already be needs-attention, then resumes with absent canonical artifacts.
- [ ] Persist needs-attention before the first destructive Delete lifecycle/artifact operation, remove the manifest only on full success, and retain foreign-artifact refusal.
- [ ] Run focused remote rendering/lifecycle tests and commit.

### Task 3: Direct Manager Concurrency

**Files:** `internal/modules/storage/remote_manager.go`, `internal/modules/storage/remote_manager_test.go`

- [ ] Add direct tests proving State returns while a lifecycle operation is blocked, equal IDs serialize, and different IDs overlap; observe RED against the global mutex.
- [ ] Replace the global mutex with a Create mutex and a per-ID keyed lifecycle lock whose entry is removed after unlock.
- [ ] Keep Create globally serialized for target inventory checks, and serialize its final lifecycle under the definition lock.
- [ ] Make State avoid lifecycle locks and treat transient missing/invalid manifest artifacts during cleanup as absent rather than failing the full snapshot.
- [ ] Run `go test -race ./internal/modules/storage -run 'TestRemoteManager'` and commit.

### Task 4: Snapshot Integrity And Capacity Documentation

**Files:** `internal/modules/storage/normalize.go`, `internal/modules/storage/normalize_test.go`

- [ ] Add a truncation test asserting findings with removed resource IDs are absent and no dangling anchor-producing finding remains; observe RED.
- [ ] Filter resource-scoped findings as part of snapshot reference sanitization, then revalidate the resulting snapshot graph/references.
- [ ] Add the clarifying comment beside aggregate capacity accounting that empty ResourceID mounts are intentionally excluded because network/ambiguous mounts cannot be safely attributed to local capacity.
- [ ] Run normalize tests and commit.

### Task 5: Managed Lifecycle Controls

**Files:** `internal/modules/storage/views.templ`, `internal/modules/storage/views_test.go`

- [ ] Add rendering tests proving mounted managed entries show Unmount but not Mount, inactive entries show Mount but not Unmount, and recoverable managed entries retain Delete; observe RED.
- [ ] Render state-appropriate lifecycle controls while preserving admin and managed-ID validation.
- [ ] Run `make generate` and focused rendering tests, then commit.

### Task 6: Full Verification And Handoff

**Files:** `.superpowers/sdd/final-review-fix-report.md`

- [ ] Run focused regression tests after each task and `go test -race ./internal/broker ./internal/modules/storage ./cmd/pilothouse ./cmd/pilothoused ./internal/web`.
- [ ] Run `make generate`, `make docker-build`, `make docker-test`, `make docker-fmt`, and `make docker-lint`.
- [ ] Record each RED/GREEN command and exact final verification in the ignored report.
- [ ] Inspect `git status --short`, `git diff --check`, commit intended source/tests/docs, and verify a clean worktree excluding the ignored report.
