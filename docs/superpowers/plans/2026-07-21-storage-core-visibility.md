# Storage Core Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only Storage module that presents a bounded, coherent graph of block devices, filesystems, and local or network mounts to every authenticated user.

**Architecture:** The privileged daemon runs fixed `lsblk` and `findmnt` adapters, merges their typed results into a normalized snapshot, and exposes it through one parameterless broker query. The web module consumes only that snapshot and contributes a dashboard card, Attention findings, and a 30-second HTMX-refreshed hybrid operations page.

**Tech Stack:** Go 1.26.3, standard-library `context`, `encoding/json`, `os/exec`, `net/http`, templ, HTMX, vanilla CSS, testify.

**Implemented decision:** `findmnt --json --bytes --output ...` is consumed as
a flat `filesystems` array. Empty, `-`, and `null` capacity placeholders are
accepted only as a consistent unavailable-capacity set and are represented as
zero capacity; mixed placeholders and numeric capacity fields are rejected.

## Global Constraints

- Keep the module isolated under `internal/modules/storage`; do not import or modify the System collector.
- The web process must not inspect devices, mounts, sysfs, procfs, or invoke commands.
- Use only `broker.QueryStorageState = "org.frostyard.pilothouse.storage.state"`; reject every query parameter.
- Resolve tools only from compile-time absolute candidate paths and reject non-regular, non-root-owned, group-writable, or world-writable executables.
- Run external tools without a shell and cap each adapter's captured output at 4 MiB.
- Apply a five-second timeout per adapter and a twelve-second overall manager timeout.
- Cap snapshots at 4,096 resources, 8,192 relations, 1,024 mounts, 512 findings, 32 details per resource, 4 KiB per text field, graph depth 32, and 2 MiB serialized JSON.
- All authenticated users may query and view storage state; this plan adds no mutations.
- Run `make generate` after templ changes and never edit generated `*_templ.go` files.
- Before handoff run `make build`, `make test`, `make fmt`, and `make lint`, using matching Docker targets if native dependencies are unavailable.

## File Structure

- Create `internal/modules/storage/model.go`: exported broker presentation types, constants, stable-ID and health helpers.
- Create `internal/modules/storage/tools.go`: secure absolute-tool resolution and bounded command execution.
- Create `internal/modules/storage/core.go`: fixed `lsblk` and `findmnt` adapters and strict parsers.
- Create `internal/modules/storage/normalize.go`: graph merge, relation validation, capacity aggregation, findings, ordering, and limits.
- Create `internal/modules/storage/manager.go`: adapter orchestration and `Manager` implementation.
- Create `internal/modules/storage/module.go`: platform module, fixed query client, dashboard, health, and route.
- Create `internal/modules/storage/views.templ`: dashboard summary and hybrid operations page.
- Create `internal/modules/storage/*_test.go`: focused model, tool, parser, manager, module, and rendering tests.
- Modify `internal/broker/api.go`: add the fixed storage query constant.
- Modify `cmd/pilothoused/main.go` and `cmd/pilothoused/main_test.go`: instantiate and register the privileged manager/query.
- Modify `cmd/pilothouse/main.go`: register one Storage module and include it in Attention providers.
- Modify `internal/web/static/app.css`: add only storage topology/details styles not covered by existing primitives.

---

### Task 1: Storage Presentation Contract

**Files:**
- Create: `internal/modules/storage/model.go`
- Create: `internal/modules/storage/model_test.go`
- Modify: `internal/broker/api.go`
- Test: `internal/modules/storage/model_test.go`

**Interfaces:**
- Produces: `Manager`, `Snapshot`, `Summary`, `Resource`, `Relation`, `Mount`, `BackendStatus`, `Finding`, `Health`, `Detail`, and `QueryStorageState` used by every later task and plan.

- [ ] **Step 1: Write the failing contract tests**

```go
package storage

import (
	"testing"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/stretchr/testify/assert"
)

func TestStorageBrokerID(t *testing.T) {
	assert.Equal(t, "org.frostyard.pilothouse.storage.state", broker.QueryStorageState)
}

func TestStableIDIsDeterministicAndNamespaced(t *testing.T) {
	assert.Equal(t, stableID("disk", "8:0"), stableID("disk", "8:0"))
	assert.NotEqual(t, stableID("disk", "8:0"), stableID("filesystem", "8:0"))
	assert.Regexp(t, `^disk:[a-f0-9]{16}$`, stableID("disk", "8:0"))
}

func TestHealthSeverityOrder(t *testing.T) {
	assert.Greater(t, healthRank(HealthCritical), healthRank(HealthWarning))
	assert.Greater(t, healthRank(HealthWarning), healthRank(HealthUnknown))
	assert.Greater(t, healthRank(HealthUnknown), healthRank(HealthHealthy))
}
```

- [ ] **Step 2: Run the focused tests and verify the red state**

Run: `go test ./internal/modules/storage -run 'Test(StorageBrokerID|StableID|HealthSeverity)'`

Expected: FAIL because the package/types and `broker.QueryStorageState` do not exist.

- [ ] **Step 3: Add the fixed broker constant and model**

Add to `internal/broker/api.go`:

```go
QueryStorageState = "org.frostyard.pilothouse.storage.state"
```

Create these exact public types in `model.go`:

```go
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const (
	maxResources     = 4096
	maxRelations     = 8192
	maxMounts        = 1024
	maxFindings      = 512
	maxDetails       = 32
	maxFieldBytes    = 4 * 1024
	maxAdapterBytes  = 4 * 1024 * 1024
	maxSnapshotBytes = 2 * 1024 * 1024
	maxGraphDepth    = 32
)

type Health string

const (
	HealthHealthy  Health = "healthy"
	HealthUnknown  Health = "unknown"
	HealthWarning  Health = "warning"
	HealthCritical Health = "critical"
)

type Availability string

const (
	BackendAvailable   Availability = "available"
	BackendUnsupported Availability = "unsupported"
	BackendUnavailable Availability = "unavailable"
	BackendTimedOut    Availability = "timed-out"
	BackendTruncated   Availability = "truncated"
)

type Manager interface {
	State(context.Context) (Snapshot, error)
}

type Snapshot struct {
	Backends   []BackendStatus `json:"backends"`
	CollectedAt time.Time       `json:"collected_at"`
	Findings   []Finding       `json:"findings"`
	Mounts     []Mount         `json:"mounts"`
	Relations  []Relation      `json:"relations"`
	Resources  []Resource      `json:"resources"`
	Summary    Summary         `json:"summary"`
	Truncated  bool            `json:"truncated"`
}

type Summary struct {
	ActiveMounts       int    `json:"active_mounts"`
	FreeBytes          uint64 `json:"free_bytes"`
	HighestHealth      Health `json:"highest_health"`
	UnavailableBackends int   `json:"unavailable_backends"`
	UnhealthyResources int    `json:"unhealthy_resources"`
	UsableBytes        uint64 `json:"usable_bytes"`
	UsedBytes          uint64 `json:"used_bytes"`
}

type Resource struct {
	Details   []Detail `json:"details"`
	Health    Health   `json:"health"`
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Path      string   `json:"path,omitempty"`
	SizeBytes uint64   `json:"size_bytes,omitempty"`
	State     string   `json:"state,omitempty"`
}

type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Relation struct {
	From string `json:"from"`
	Kind string `json:"kind"`
	To   string `json:"to"`
}

type Mount struct {
	AvailableBytes uint64  `json:"available_bytes"`
	Filesystem     string  `json:"filesystem"`
	Health         Health  `json:"health"`
	ID             string  `json:"id"`
	Managed        bool    `json:"managed"`
	Options        []string `json:"options"`
	ReadOnly       bool    `json:"read_only"`
	ResourceID     string  `json:"resource_id,omitempty"`
	Source         string  `json:"source"`
	State          string  `json:"state"`
	Target         string  `json:"target"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

type BackendStatus struct {
	Availability Availability `json:"availability"`
	CollectedAt  time.Time    `json:"collected_at"`
	Name         string       `json:"name"`
}

type Finding struct {
	Detail     string `json:"detail"`
	ResourceID string `json:"resource_id"`
	Severity   Health `json:"severity"`
	Title      string `json:"title"`
}

func stableID(kind, identity string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + identity))
	return kind + ":" + hex.EncodeToString(sum[:8])
}

func healthRank(value Health) int {
	switch value {
	case HealthCritical:
		return 3
	case HealthWarning:
		return 2
	case HealthUnknown:
		return 1
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run and pass the contract tests**

Run: `go test ./internal/modules/storage -run 'Test(StorageBrokerID|StableID|HealthSeverity)'`

Expected: PASS.

- [ ] **Step 5: Commit the contract**

```bash
git add internal/broker/api.go internal/modules/storage/model.go internal/modules/storage/model_test.go
git commit -m "feat: define storage snapshot contract"
```

### Task 2: Secure Tool Runner And Core Parsers

**Files:**
- Create: `internal/modules/storage/tools.go`
- Create: `internal/modules/storage/tools_test.go`
- Create: `internal/modules/storage/core.go`
- Create: `internal/modules/storage/core_test.go`
- Create: `internal/modules/storage/testdata/lsblk.json`
- Create: `internal/modules/storage/testdata/findmnt.json`

**Interfaces:**
- Consumes: model types and limits from Task 1.
- Produces: `Adapter`, `AdapterResult`, `Toolset`, `NewToolset`, `newBlockAdapter`, and `newMountAdapter` for Task 3 and the advanced-adapter plan.

- [ ] **Step 1: Write runner security and limit tests**

Create tests that use `t.TempDir()`, write executable files, and assert all four cases explicitly:

```go
func TestResolveToolRequiresRootOwnedSafeRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lsblk")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	_, err := resolveTool([]string{path}, func(string) (fileIdentity, error) {
		return fileIdentity{Mode: 0o755, UID: 1000, Regular: true}, nil
	})
	assert.ErrorContains(t, err, "root-owned")
}

func TestBoundedRunnerRejectsOversizedOutput(t *testing.T) {
	runner := commandRunner{limit: 8, run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("123456789"), nil
	}}
	_, err := runner.Run(context.Background(), "/usr/bin/lsblk", "--json")
	assert.ErrorIs(t, err, errOutputTooLarge)
}
```

Also test rejection of a non-regular file and mode `0775`, plus acceptance of a root-owned regular file with mode `0755`.

- [ ] **Step 2: Write strict parser tests from fixed fixtures**

Use fixtures containing one disk, one partition, one ext4 filesystem, one local mount, one NFS mount, one bind mount, and one overlay mount. Assert:

```go
func TestCoreAdaptersParseTypedResults(t *testing.T) {
	blocks, err := parseLSBLK(mustFixture(t, "lsblk.json"))
	require.NoError(t, err)
	require.Len(t, blocks.Resources, 3)
	assert.Equal(t, "disk", blocks.Resources[0].Kind)
	assert.Contains(t, blocks.Relations, Relation{From: stableID("disk", "8:0"), To: stableID("partition", "8:1"), Kind: "contains"})

	mounts, err := parseFindmnt(mustFixture(t, "findmnt.json"))
	require.NoError(t, err)
	require.Len(t, mounts.Mounts, 4)
	assert.Equal(t, "server:/export", mounts.Mounts[1].Source)
	assert.True(t, mounts.Mounts[2].ReadOnly)
}
```

Add separate tests that reject unknown JSON fields, malformed byte counts, fields over 4 KiB, more than 4,096 block resources, and more than 1,024 mounts.

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/modules/storage -run 'Test(ResolveTool|BoundedRunner|CoreAdapters|ParseLSBLK|ParseFindmnt)'`

Expected: FAIL because runner and parser functions do not exist.

- [ ] **Step 4: Implement secure tools and the adapter boundary**

Use these exact boundaries:

```go
type Adapter interface {
	Collect(context.Context) (AdapterResult, error)
	Core() bool
	Name() string
}

type AdapterResult struct {
	Findings  []Finding
	Mounts    []Mount
	Relations []Relation
	Resources []Resource
	Truncated bool
}

type Toolset struct {
	Findmnt string
	LSBLK  string
}

func NewToolset() (Toolset, error) {
	lsblk, err := resolveSystemTool("lsblk", []string{"/usr/bin/lsblk", "/bin/lsblk"})
	if err != nil { return Toolset{}, err }
	findmnt, err := resolveSystemTool("findmnt", []string{"/usr/bin/findmnt", "/bin/findmnt"})
	if err != nil { return Toolset{}, err }
	return Toolset{LSBLK: lsblk, Findmnt: findmnt}, nil
}
```

`resolveSystemTool` must call `os.Lstat`, require `Mode().IsRegular()`, inspect `syscall.Stat_t.Uid == 0`, and reject `mode.Perm()&0o022 != 0`. `commandRunner.Run` must use `exec.CommandContext(ctx, path, args...)`, attach separate bounded stdout/stderr buffers, and return `errOutputTooLarge` before any parser sees oversized data.

- [ ] **Step 5: Implement fixed `lsblk` and `findmnt` adapters**

Use only these invocations:

```go
[]string{"--json", "--bytes", "--output", "NAME,KNAME,PATH,TYPE,MAJ:MIN,PKNAME,SIZE,FSTYPE,FSVER,LABEL,UUID,MOUNTPOINTS,MODEL,SERIAL,ROTA,RM,RO"}
[]string{"--json", "--bytes", "--output", "TARGET,SOURCE,FSTYPE,OPTIONS,SIZE,USED,AVAIL,USE%,MAJ:MIN"}
```

Decode with `json.Decoder.DisallowUnknownFields`. Convert block identity from validated `MAJ:MIN`; derive network and virtual mount identity from filesystem plus source. Keep only safe mount options `ro`, `rw`, `nosuid`, `nodev`, `noexec`, `relatime`, and `bind`. Reject invalid numerics rather than coercing them to zero. Sort parser output by ID or target before returning.

- [ ] **Step 6: Run all runner/parser tests**

Run: `go test ./internal/modules/storage -run 'Test(ResolveTool|BoundedRunner|CoreAdapters|ParseLSBLK|ParseFindmnt)'`

Expected: PASS.

- [ ] **Step 7: Commit core discovery**

```bash
git add internal/modules/storage/tools.go internal/modules/storage/tools_test.go internal/modules/storage/core.go internal/modules/storage/core_test.go internal/modules/storage/testdata
git commit -m "feat: collect core storage inventory"
```

### Task 3: Snapshot Aggregation And Limits

**Files:**
- Create: `internal/modules/storage/normalize.go`
- Create: `internal/modules/storage/normalize_test.go`
- Create: `internal/modules/storage/manager.go`
- Create: `internal/modules/storage/manager_test.go`

**Interfaces:**
- Consumes: `Adapter`, `AdapterResult`, and all model types.
- Produces: `NewSystemManager(adapters ...Adapter) *SystemManager` and `(*SystemManager).State(context.Context) (Snapshot, error)`.

- [ ] **Step 1: Write graph and aggregation tests**

Define a fake adapter with configurable delay/result/error. Cover these exact assertions:

```go
func TestNormalizeRejectsCycles(t *testing.T) {
	_, err := normalize(time.Unix(1, 0), []collectedResult{{name: "block", core: true, result: AdapterResult{
		Resources: []Resource{{ID: "a"}, {ID: "b"}},
		Relations: []Relation{{From: "a", To: "b", Kind: "contains"}, {From: "b", To: "a", Kind: "contains"}},
	}}})
	assert.ErrorContains(t, err, "cycle")
}

func TestNormalizeCountsFilesystemCapacityOnce(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{name: "mount", core: true, result: AdapterResult{Mounts: []Mount{
		{ID: "root", ResourceID: "fs:one", TotalBytes: 100, UsedBytes: 60, AvailableBytes: 40, State: "mounted"},
		{ID: "bind", ResourceID: "fs:one", TotalBytes: 100, UsedBytes: 60, AvailableBytes: 40, State: "mounted"},
	}}})
	require.NoError(t, err)
	assert.Equal(t, uint64(100), snapshot.Summary.UsableBytes)
	assert.Equal(t, uint64(60), snapshot.Summary.UsedBytes)
}
```

Also test deterministic sorting, missing relation endpoint rejection, depth 33 rejection, 80% warning, 90% critical, read-only warning, core adapter failure, optional adapter degradation, adapter timeout, and 2 MiB serialized limit truncation.

- [ ] **Step 2: Run tests and verify the red state**

Run: `go test ./internal/modules/storage -run 'Test(Normalize|SystemManager)'`

Expected: FAIL because normalization and manager orchestration do not exist.

- [ ] **Step 3: Implement deterministic normalization**

`normalize` must:

```go
func normalize(collectedAt time.Time, results []collectedResult) (Snapshot, error)
```

Merge by exact ID; reject conflicting duplicate resources; deduplicate exact relations; reject missing endpoints and directed cycles; sort resources by kind/name/ID, relations by from/kind/to, mounts by target/source/ID, findings by descending health then resource ID, and backends by name. Compute capacity once per non-empty `ResourceID`; exclude overlay and ambiguous empty identities. Generate mount findings at `>=80%` warning and `>=90%` critical, and warning for unexpected read-only mounts. Stop at fixed collection limits and set `Truncated` plus the affected backend to `BackendTruncated`.

- [ ] **Step 4: Implement concurrent manager orchestration**

Use one goroutine per adapter, a buffered result channel sized to `len(adapters)`, `context.WithTimeout(ctx, 12*time.Second)` overall, and `context.WithTimeout(overall, 5*time.Second)` per adapter. Convert optional errors to backend statuses. Return an error if any `Core()` adapter fails or times out. Do not return until all launched goroutines have reported or the overall context ends.

```go
func NewSystemManager(adapters ...Adapter) *SystemManager {
	return &SystemManager{adapters: slices.Clone(adapters), now: time.Now}
}
```

- [ ] **Step 5: Run aggregation tests**

Run: `go test ./internal/modules/storage -run 'Test(Normalize|SystemManager)'`

Expected: PASS, including race-safe completion.

- [ ] **Step 6: Run the package under the race detector**

Run: `go test -race ./internal/modules/storage`

Expected: PASS with no race reports.

- [ ] **Step 7: Commit the manager**

```bash
git add internal/modules/storage/normalize.go internal/modules/storage/normalize_test.go internal/modules/storage/manager.go internal/modules/storage/manager_test.go
git commit -m "feat: aggregate storage snapshots"
```

### Task 4: Privileged Query Registration

**Files:**
- Modify: `cmd/pilothoused/main.go`
- Modify: `cmd/pilothoused/main_test.go`

**Interfaces:**
- Consumes: `storage.NewToolset`, `storage.NewSystemManager`, `storage.NewBlockAdapter`, `storage.NewMountAdapter`, and `storage.Manager`.
- Produces: one non-admin fixed query registration.

- [ ] **Step 1: Export adapter constructors and write registration tests**

Add constructors returning `Adapter`:

```go
func NewBlockAdapter(path string) Adapter
func NewMountAdapter(path string) Adapter
```

Add a `fakeStorageManager` and tests asserting a non-admin identity can execute `broker.QueryStorageState`, parameters are rejected, and the fake snapshot is returned unchanged.

```go
func TestRegisterStorageAllowsAuthenticatedRead(t *testing.T) {
	queries := broker.NewQueryRegistry()
	expected := storage.Snapshot{Summary: storage.Summary{ActiveMounts: 2}}
	require.NoError(t, registerStorage(queries, fakeStorageManager{snapshot: expected}))
	result, err := queries.Execute(context.Background(), auth.Identity{Username: "viewer"}, broker.QueryStorageState, nil)
	require.NoError(t, err)
	assert.Equal(t, expected, result)
}
```

- [ ] **Step 2: Run the registration test and verify it fails**

Run: `go test ./cmd/pilothoused -run TestRegisterStorage`

Expected: FAIL because `registerStorage` does not exist.

- [ ] **Step 3: Register the fixed manager and reject parameters**

```go
func registerStorage(queries *broker.QueryRegistry, manager storage.Manager) error {
	return queries.Register(broker.QueryStorageState, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		if len(parameters) != 0 {
			return nil, fmt.Errorf("storage state query does not accept parameters")
		}
		return manager.State(ctx)
	})
}
```

In `run`, resolve the toolset, construct both core adapters, then register the manager before the broker server starts. Failure to resolve either core executable must fail daemon startup.

- [ ] **Step 4: Run registration tests**

Run: `go test ./cmd/pilothoused -run TestRegisterStorage`

Expected: PASS.

- [ ] **Step 5: Commit broker wiring**

```bash
git add cmd/pilothoused/main.go cmd/pilothoused/main_test.go internal/modules/storage/core.go
git commit -m "feat: register storage inventory query"
```

### Task 5: Web Module, Dashboard, And Attention

**Files:**
- Create: `internal/modules/storage/module.go`
- Create: `internal/modules/storage/module_test.go`
- Create: `internal/modules/storage/views.templ`
- Create: `internal/modules/storage/views_test.go`
- Modify: `cmd/pilothouse/main.go`

**Interfaces:**
- Consumes: `Snapshot`, `platform.Host.Query`, `platform.Module`, and `platform.HealthProvider`.
- Produces: `New() *Module`, `/storage`, dashboard card, and storage findings.

- [ ] **Step 1: Write fake-host module tests**

Copy the fake-host shape from `internal/modules/backups/module_test.go`, then assert:

```go
func TestModuleUsesOnlyStorageQuery(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Summary: Summary{ActiveMounts: 3}}}
	module := New()
	cards, err := module.Dashboard(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, broker.QueryStorageState, host.queryID)
	assert.Nil(t, host.queryParameters)
	assert.Equal(t, platform.SpanHalf, cards[0].Span)
}

func TestHealthMapsStorageSeverity(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Findings: []Finding{{ResourceID: "disk:abc", Severity: HealthCritical, Title: "Disk health failed", Detail: "Media errors reported"}}}}
	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	assert.Equal(t, platform.SeverityCritical, findings[0].Severity)
	assert.Equal(t, "/storage#disk-abc", findings[0].Path)
}
```

Test manifest ID/path/order, GET `/storage`, 12-second handler context behavior, and unavailable page rendering rather than raw broker error disclosure.

- [ ] **Step 2: Run module tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(Module|Health|Manifest|StoragePage)'`

Expected: FAIL because `Module` and views do not exist.

- [ ] **Step 3: Implement the module**

Use manifest `{ID: "storage", Name: "Storage", Path: "/storage", Icon: "disk", Order: 25}`. `Dashboard`, `Health`, and the GET handler must call one private helper:

```go
func queryState(ctx context.Context, host platform.Host) (Snapshot, error) {
	var snapshot Snapshot
	err := host.Query(ctx, broker.QueryStorageState, nil, &snapshot)
	return snapshot, err
}
```

Map storage health exactly: critical to `platform.SeverityCritical`, warning to `platform.SeverityWarning`, unknown to `platform.SeverityUnknown`, healthy to `platform.SeverityInfo`. Sanitize anchors by replacing every non-ASCII alphanumeric character with `-`.

- [ ] **Step 4: Wire Storage into navigation and Attention**

Create one `storageModule := storage.New()`, pass it to `attention.New(system, serviceModule, maintenanceModule, backupModule, storageModule)`, and add the same instance to `platform.NewRegistry` after System and before Sysext.

- [ ] **Step 5: Add minimal functional views**

Create `SummaryCard(Snapshot)` with active-mount count, usable/used capacity, and highest health. Create `Page(Snapshot, bool)` that renders either a stable unavailable state or the snapshot summary plus a simple mounted-storage table. These are real fallback views, not temporary stubs; Task 6 expands the same components into the hybrid layout. Add rendering tests for summary values, unavailable copy, escaped mount values, and no literal `@web.` syntax.

- [ ] **Step 6: Generate and run module, rendering, and command tests**

Run: `make generate && go test ./internal/modules/storage ./cmd/pilothouse`

Expected: PASS.

- [ ] **Step 7: Commit web integration**

```bash
git add internal/modules/storage/module.go internal/modules/storage/module_test.go internal/modules/storage/views.templ internal/modules/storage/views_test.go cmd/pilothouse/main.go
git commit -m "feat: register storage web module"
```

### Task 6: Hybrid Storage Views

**Files:**
- Modify: `internal/modules/storage/views.templ`
- Modify: `internal/modules/storage/views_test.go`
- Modify: `internal/web/static/app.css`

**Interfaces:**
- Consumes: `Snapshot` and `platform` rendering primitives.
- Produces: `SummaryCard(Snapshot)`, `Page(Snapshot, bool)`, and `SnapshotRegion(Snapshot, bool)`.

- [ ] **Step 1: Write rendering tests before the template**

Render a snapshot containing a warning, local mount, NFS mount, disk-to-partition relation, and unavailable optional backend. Assert all of these literals and attributes:

```go
assert.Contains(t, html, `id="storage-snapshot"`)
assert.Contains(t, html, `hx-get="/storage"`)
assert.Contains(t, html, `hx-trigger="every 30s"`)
assert.Contains(t, html, `hx-select="#storage-snapshot"`)
assert.Contains(t, html, `Mounted storage`)
assert.Contains(t, html, `Storage topology`)
assert.Contains(t, html, `server:/export`)
assert.Contains(t, html, `Backend unavailable`)
assert.NotContains(t, html, `@web.`)
```

Add tests for empty, fully unavailable, truncated, escaped labels, read-only badges, and narrow-screen structure classes.

- [ ] **Step 2: Run rendering tests and verify failure**

Run: `go test ./internal/modules/storage -run 'TestRender'`

Expected: FAIL because the templ components do not exist.

- [ ] **Step 3: Implement the templ components**

Build the approved hierarchy using existing `card`, `metric`, `table-card`, `table-toolbar`, `data-table`, `badge`, and `empty-state` classes. Put every `@web.Icon(...)` invocation in its own template node. Keep `SnapshotRegion` as the sole 30-second replacement target:

```templ
templ SnapshotRegion(snapshot Snapshot, unavailable bool) {
	<section id="storage-snapshot" class="storage-snapshot" hx-get="/storage" hx-trigger="every 30s" hx-select="#storage-snapshot" hx-target="#storage-snapshot" hx-swap="outerHTML">
		@StorageMetrics(snapshot.Summary)
		@StorageAttention(snapshot.Findings, snapshot.Truncated)
		@MountTable(snapshot.Mounts)
		@Topology(snapshot.Resources, snapshot.Relations)
		@Inventory(snapshot.Resources)
		@BackendStates(snapshot.Backends)
	</section>
}
```

- [ ] **Step 4: Add focused responsive topology CSS**

Add `.storage-snapshot`, `.storage-operations`, `.storage-topology`, `.storage-tree`, `.storage-node`, and `.storage-details` rules. Reuse existing colors and spacing variables. At the existing mobile breakpoint, collapse `.storage-operations` to one column and preserve horizontal table scrolling.

- [ ] **Step 5: Generate templ output and run rendering tests**

Run: `make generate && go test ./internal/modules/storage -run 'TestRender'`

Expected: generation succeeds and all rendering tests PASS.

- [ ] **Step 6: Commit views**

```bash
git add internal/modules/storage/views.templ internal/modules/storage/views_test.go internal/web/static/app.css
git commit -m "feat: render storage operations view"
```

### Task 7: Core Integration Verification

**Files:**
- Modify: `docs/modules.md`
- Test: all files changed by Tasks 1-6.

**Interfaces:**
- Consumes: the complete read-only Storage module.
- Produces: documented module/broker behavior and a verified baseline for the advanced-adapter and remote-management plans.

- [ ] **Step 1: Add module documentation**

Document that Storage uses `broker.QueryStorageState`, that the web process never invokes storage tools, that optional backends degrade independently, and that this first phase is read-only.

- [ ] **Step 2: Run generation and formatting**

Run: `make generate && make fmt`

Expected: both commands exit 0.

- [ ] **Step 3: Run focused race and registration tests**

Run: `go test -race ./internal/modules/storage ./cmd/pilothouse ./cmd/pilothoused`

Expected: PASS with no race reports.

- [ ] **Step 4: Run required project verification**

Run: `make build && make test && make lint`

Expected: all commands exit 0. If native PAM/systemd dependencies are missing, run `make docker-build && make docker-test && make docker-lint` and require all three to exit 0.

- [ ] **Step 5: Inspect the final diff for generated or unrelated files**

Run: `git status --short && git diff --check && git diff --stat`

Expected: only intended source, tests, CSS, and docs appear; no generated `*_templ.go` files or `.superpowers/` artifacts are staged.

- [ ] **Step 6: Commit documentation and verification fixes**

```bash
git add docs/modules.md
git commit -m "docs: describe storage inventory module"
```
