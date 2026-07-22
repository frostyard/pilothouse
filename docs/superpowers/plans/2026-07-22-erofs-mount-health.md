# EROFS Mount Health Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop expected read-only, fully allocated EROFS mounts from producing false storage health findings while preserving warnings for other filesystems.

**Architecture:** Keep mount discovery and presentation unchanged, and narrow only the normalization policy that derives health findings. A small filesystem predicate will identify EROFS as expected immutable storage and bypass both capacity and read-only findings for those mounts; all other mount health behavior remains intact.

**Tech Stack:** Go 1.26, `testify`, templ generation, Make

## Global Constraints

- Preserve EROFS mounts in inventory, including capacity, utilization, and the read-only badge.
- Suppress both capacity and read-only health findings only for filesystem type `erofs`.
- Preserve existing capacity thresholds and unexpected read-only warnings for every non-EROFS filesystem.
- Do not change broker APIs, mount collection, or privileged behavior.
- Update `README.md`, the storage design documentation, and `yeti/OVERVIEW.md` with the health-policy rationale.
- Run `make generate`, `make build`, `make test`, `make fmt`, and `make lint` before completion.
- Do not create a Git commit unless the user explicitly requests one.

---

## File Structure

- `internal/modules/storage/normalize_test.go`: regression coverage for EROFS and retained non-EROFS behavior.
- `internal/modules/storage/normalize.go`: mount-health policy and the EROFS predicate.
- `README.md`: user-facing storage health behavior.
- `docs/superpowers/specs/2026-07-21-storage-module-design.md`: authoritative storage health semantics.
- `yeti/OVERVIEW.md`: AI-facing architecture and rationale.

### Task 1: Exempt Expected EROFS Mount State From Health Findings

**Files:**
- Modify: `internal/modules/storage/normalize_test.go:63-72`
- Modify: `internal/modules/storage/normalize.go:76-91`
- Modify: `README.md:7-31`
- Modify: `docs/superpowers/specs/2026-07-21-storage-module-design.md:99-113`
- Modify: `yeti/OVERVIEW.md:58-78`

**Interfaces:**
- Consumes: `Mount.Filesystem`, `Mount.ReadOnly`, `Mount.UsedPercent`, and the existing `normalize(time.Time, []collectedResult) (Snapshot, error)` pipeline.
- Produces: `isExpectedImmutableMount(Mount) bool`, used only by mount health normalization.

- [x] **Step 1: Generate templ output required to compile the package**

Run: `make generate`

Expected: templ generation exits successfully and restores generated `*_templ.go` files needed by storage package tests.

- [x] **Step 2: Write focused regression tests**

Replace `TestNormalizeCreatesMountFindings` with tests that isolate the exempt and retained policies:

```go
func TestNormalizeIgnoresExpectedEROFSState(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{result: AdapterResult{Mounts: []Mount{
		{ID: "root", ResourceID: "root", Target: "/", Filesystem: "erofs", ReadOnly: true, UsedPercent: 100, State: "mounted", Health: HealthHealthy},
		{ID: "root-home", ResourceID: "root-home", Target: "/root", Filesystem: "erofs", ReadOnly: true, UsedPercent: 100, State: "mounted", Health: HealthHealthy},
		{ID: "usr", ResourceID: "usr", Target: "/usr", Filesystem: "erofs", ReadOnly: true, UsedPercent: 100, State: "mounted", Health: HealthHealthy},
	}}}})
	require.NoError(t, err)
	assert.Empty(t, snapshot.Findings)
	for _, mount := range snapshot.Mounts {
		assert.Equal(t, HealthHealthy, mount.Health)
		assert.True(t, mount.ReadOnly)
		assert.Equal(t, float64(100), mount.UsedPercent)
	}
}

func TestNormalizeCreatesFindingsForNonEROFSState(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{result: AdapterResult{Mounts: []Mount{
		{ID: "warning", ResourceID: "warning", Filesystem: "ext4", UsedPercent: 80, State: "mounted"},
		{ID: "critical", ResourceID: "critical", Filesystem: "xfs", UsedPercent: 90, State: "mounted"},
		{ID: "readonly", ResourceID: "readonly", Filesystem: "ext4", ReadOnly: true, State: "mounted"},
	}}}})
	require.NoError(t, err)
	assert.Equal(t, []Health{HealthCritical, HealthWarning, HealthWarning}, findingSeverities(snapshot.Findings))
	assert.Contains(t, findingTitles(snapshot.Findings), "Mount capacity is critical")
	assert.Contains(t, findingTitles(snapshot.Findings), "Mount capacity is high")
	assert.Contains(t, findingTitles(snapshot.Findings), "Mount is read-only")
}
```

- [x] **Step 3: Run the regression test to verify it fails for the reported behavior**

Run: `go test ./internal/modules/storage -run 'TestNormalize(IgnoresExpectedEROFSState|CreatesFindingsForNonEROFSState)$'`

Expected: `TestNormalizeIgnoresExpectedEROFSState` fails because EROFS mounts currently produce critical capacity and read-only findings; the non-EROFS test passes.

- [x] **Step 4: Implement the narrow EROFS health exemption**

Guard the existing derived mount-health checks and add the local predicate:

```go
	for i := range snapshot.Mounts {
		mount := &snapshot.Mounts[i]
		if isExpectedImmutableMount(*mount) {
			continue
		}
		if mount.UsedPercent >= 90 {
			mount.Health = HealthCritical
			appendFinding(&snapshot, &state.truncated, "", Finding{ResourceID: mount.ResourceID, Severity: HealthCritical, Title: "Mount capacity is critical", Detail: mount.Target})
		} else if mount.UsedPercent >= 80 {
			mount.Health = HealthWarning
			appendFinding(&snapshot, &state.truncated, "", Finding{ResourceID: mount.ResourceID, Severity: HealthWarning, Title: "Mount capacity is high", Detail: mount.Target})
		}
		if mount.ReadOnly && !mount.Managed {
			if healthRank(mount.Health) < healthRank(HealthWarning) {
				mount.Health = HealthWarning
			}
			appendFinding(&snapshot, &state.truncated, "", Finding{ResourceID: mount.ResourceID, Severity: HealthWarning, Title: "Mount is read-only", Detail: mount.Target})
		}
	}
```

Add below `normalize`:

```go
func isExpectedImmutableMount(mount Mount) bool {
	return mount.Filesystem == "erofs"
}
```

- [x] **Step 5: Run focused storage tests to verify the fix**

Run: `go test ./internal/modules/storage -run 'TestNormalize'`

Expected: all normalization tests pass, including the EROFS regression and retained non-EROFS findings.

- [x] **Step 6: Document the policy and rationale**

In `README.md`, add this storage behavior to the feature list:

```markdown
- Storage health that distinguishes expected immutable EROFS mounts from unexpected read-only or capacity-exhausted writable filesystems
```

In `docs/superpowers/specs/2026-07-21-storage-module-design.md`, replace the filesystem health bullet with:

```markdown
- Filesystems and mounts: capacity thresholds, unexpected read-only transitions, inaccessible sources, and inactive managed definitions. EROFS mounts are expected immutable image content, so their read-only state and fully allocated image capacity remain visible in inventory but do not produce health findings.
```

In the storage module row in `yeti/OVERVIEW.md`, append:

```markdown
Expected immutable EROFS mounts retain their inventory usage and read-only state but are excluded from capacity and read-only health findings; other filesystems retain those checks.
```

- [x] **Step 7: Format and run all required verification**

Run each command independently:

```bash
make generate
make fmt
make build
make test
make lint
git diff --check
```

Expected: every command exits successfully; `git diff --check` reports no whitespace errors.

- [x] **Step 8: Review the final diff for scope**

Run: `git diff -- internal/modules/storage/normalize.go internal/modules/storage/normalize_test.go README.md docs/superpowers/specs/2026-07-21-storage-module-design.md yeti/OVERVIEW.md docs/superpowers/plans/2026-07-22-erofs-mount-health.md`

Expected: the diff contains only the EROFS health policy, regression tests, and corresponding documentation; no mount collection, broker, UI, or unrelated behavior changes.
