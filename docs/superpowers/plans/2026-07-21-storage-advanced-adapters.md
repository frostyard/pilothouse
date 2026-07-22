# Storage Advanced Adapters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the working Storage module with bounded health and topology adapters for SMART/NVMe, MD RAID, LVM, LUKS/device-mapper, multipath, ZFS, and Btrfs.

**Architecture:** Each backend is an optional `storage.Adapter` with a fixed executable/API contract and strict parser. The existing manager runs adapters concurrently, localizes failures into backend statuses, merges typed relations into the existing graph, and serves briefly cached expensive device-health observations.

**Tech Stack:** Go 1.26.3, standard-library JSON/text parsing and concurrency, fixed Linux storage tools, existing templ/HTMX Storage UI, testify.

**Implemented decisions:** SMART uses `smartctl --json=c --all <validated-disk>`
and parses only the consumed ATA/NVMe subtrees while tolerating unrelated
smartctl fields; `device.name` must exactly match the requested path. The
five-minute clone-on-read cache is keyed by the complete `smart` enricher
result, not by individual disk, and failed refreshes return that stale result
with a `Health data: Stale` detail. Multipath uses the literal raw formats
`%n|%w|%d|%N|%t` for maps and `%m|%d|%t|%o` for paths.

## Global Constraints

- This plan depends on `2026-07-21-storage-core-visibility.md` being fully implemented and verified.
- Keep every backend behind the existing `Adapter` interface; do not put backend conditionals into web handlers or templates.
- Missing optional tools produce `unsupported`; execution or parser failures produce `unavailable`; five-second deadlines produce `timed-out`.
- Resolve optional executables from compile-time absolute candidate paths with the same ownership/mode checks as core tools.
- Invoke no shell and accept no executable, path, field selector, or argument from HTTP/broker parameters.
- Derive any device or mount path argument only from validated core inventory collected within the same fixed query.
- Apply the existing 4 MiB adapter input, resource/relation/detail, graph-depth, and 2 MiB snapshot limits.
- Cache SMART/NVMe results for five minutes; label stale results when refresh fails after expiry.
- Do not add storage mutations in this plan.
- Run all required project verification commands before handoff.

## File Structure

- Create `internal/modules/storage/cache.go`: five-minute, clone-on-read health cache with stale fallback.
- Create `internal/modules/storage/smart.go`: SMART/NVMe scan and health normalization.
- Create `internal/modules/storage/mdraid.go`: `/proc/mdstat` plus fixed `mdadm --detail --export` enrichment.
- Create `internal/modules/storage/lvm.go`: fixed JSON reports for PV, VG, and LV topology/health.
- Create `internal/modules/storage/devicemapper.go`: LUKS/device-mapper and multipath topology/status.
- Create `internal/modules/storage/zfs.go`: fixed pool, status, and dataset parsers.
- Create `internal/modules/storage/btrfs.go`: fixed filesystem usage and device-stat parsers for validated Btrfs mounts.
- Create corresponding `*_test.go` files and backend fixtures under `internal/modules/storage/testdata/`.
- Modify `internal/modules/storage/tools.go`: resolve optional tools without failing daemon startup.
- Modify `internal/modules/storage/manager.go`: provide validated core inventory context to enrichers and cache health results.
- Modify `cmd/pilothoused/main.go`: register every optional adapter, including explicit unsupported adapters.
- Modify `internal/modules/storage/views.templ` and `views_test.go`: verify backend details and stale/unsupported states.

---

### Task 1: Optional Tool And Enricher Infrastructure

**Files:**
- Modify: `internal/modules/storage/tools.go`
- Modify: `internal/modules/storage/tools_test.go`
- Modify: `internal/modules/storage/manager.go`
- Modify: `internal/modules/storage/manager_test.go`
- Create: `internal/modules/storage/cache.go`
- Create: `internal/modules/storage/cache_test.go`

**Interfaces:**
- Consumes: core `Adapter`, `AdapterResult`, `Snapshot`, and secure runner.
- Produces: `Enricher`, `Inventory`, `ResolveOptionalTool`, `NewUnsupportedEnricher`, `NewHealthCache`, and `HealthCache` used by all later tasks.

- [ ] **Step 1: Write failing optional-tool and cache tests**

```go
func TestResolveOptionalToolReturnsUnsupportedWithoutError(t *testing.T) {
	path, supported, err := resolveOptionalTool([]string{"/does/not/exist"}, os.Lstat)
	require.NoError(t, err)
	assert.False(t, supported)
	assert.Empty(t, path)
}

func TestHealthCacheReturnsStaleValueAfterFailedRefresh(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newHealthCache(func() time.Time { return now })
	cache.Store("disk:one", AdapterResult{Resources: []Resource{{ID: "disk:one", Health: HealthHealthy}}})
	now = now.Add(6 * time.Minute)
	result, fresh, found := cache.Load("disk:one")
	require.True(t, found)
	assert.False(t, fresh)
	assert.Equal(t, HealthHealthy, result.Resources[0].Health)
}
```

Also test clone-on-read, a fresh result before five minutes, and concurrent load/store under `go test -race`.

- [ ] **Step 2: Write the failing enricher orchestration test**

```go
func TestManagerPassesValidatedCoreInventoryToEnrichers(t *testing.T) {
	enricher := &fakeEnricher{name: "smart"}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})
	_, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"/dev/sda"}, enricher.inventory.DevicePaths)
}
```

- [ ] **Step 3: Run focused tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(ResolveOptional|HealthCache|ManagerPasses)'`

Expected: FAIL because optional resolution, cache, and enrichers do not exist.

- [ ] **Step 4: Implement the exact enrichment boundary**

```go
type Inventory struct {
	DevicePaths []string
	Mounts      []Mount
	Resources   []Resource
}

type Enricher interface {
	Collect(context.Context, Inventory) (AdapterResult, error)
	Name() string
}

var ErrBackendUnsupported = errors.New("storage backend unsupported")

func NewUnsupportedEnricher(name string) Enricher {
	return unsupportedEnricher{name: name}
}

func NewHealthCache() *HealthCache {
	return newHealthCache(time.Now)
}
```

Build `Inventory.DevicePaths` only from core `Resource.Path` values that are clean absolute paths under `/dev`, contain no symlink component, and belong to `disk`, `partition`, `raid`, or `mapping` resources. Build `Inventory.Mounts` from the normalized core snapshot. Optional resolution returns unsupported only for `os.ErrNotExist`; unsafe existing candidates remain startup errors.

`unsupportedEnricher.Collect` returns `ErrBackendUnsupported`. The manager maps only that sentinel to `BackendUnsupported`; context deadline errors map to `BackendTimedOut`, and every other enricher error maps to `BackendUnavailable`.

- [ ] **Step 5: Implement five-minute cache semantics**

Use `sync.RWMutex`, clone slices on store/load, and store `collectedAt`. `Load` returns `(result, fresh, found)`. Never hold the cache lock while running a tool. The caller stores successful fresh results; on refresh failure it merges stale cached results and marks their details with `Detail{Label: "Health data", Value: "Stale"}`.

- [ ] **Step 6: Run infrastructure tests with race detection**

Run: `go test -race ./internal/modules/storage -run 'Test(ResolveOptional|HealthCache|ManagerPasses)'`

Expected: PASS with no race reports.

- [ ] **Step 7: Commit infrastructure**

```bash
git add internal/modules/storage/tools.go internal/modules/storage/tools_test.go internal/modules/storage/manager.go internal/modules/storage/manager_test.go internal/modules/storage/cache.go internal/modules/storage/cache_test.go
git commit -m "feat: support optional storage enrichers"
```

### Task 2: SMART And NVMe Health

**Files:**
- Create: `internal/modules/storage/smart.go`
- Create: `internal/modules/storage/smart_test.go`
- Create: `internal/modules/storage/testdata/smart-ata.json`
- Create: `internal/modules/storage/testdata/smart-nvme.json`

**Interfaces:**
- Consumes: validated disk paths, fixed runner, and model details; the manager owns caching and stale fallback.
- Produces: `NewSMARTEnricher(path string) Enricher`.

- [ ] **Step 1: Write strict ATA/NVMe parser tests**

Fixtures must cover healthy ATA, failing ATA with reallocated/pending sectors, healthy NVMe, and warning NVMe with percentage-used/media errors. Assert normalized fields:

```go
func TestParseSMARTNVMeMediaErrorsAreCritical(t *testing.T) {
	health, err := parseSMART(mustFixture(t, "smart-nvme.json"), "/dev/nvme0n1")
	require.NoError(t, err)
	assert.Equal(t, HealthCritical, health.Health)
	assert.Contains(t, health.Details, Detail{Label: "Temperature", Value: "71 C"})
	assert.Contains(t, health.Details, Detail{Label: "Percentage used", Value: "82%"})
	assert.Contains(t, health.Details, Detail{Label: "Media errors", Value: "4"})
}
```

Reject mismatched authoritative device names, malformed consumed health booleans, counters that overflow `uint64`, and consumed fields over 4 KiB. Accept unrelated fields emitted by normal verbose `smartctl --all` output while decoding and validating every consumed subtree strictly. Accept transport annotations such as `info_name: "/dev/sda [SAT]"` when `device.name` exactly matches the validated requested path.

- [ ] **Step 2: Verify parser tests fail**

Run: `go test ./internal/modules/storage -run 'TestParseSMART'`

Expected: FAIL because `parseSMART` does not exist.

- [ ] **Step 3: Implement the fixed collector**

For each validated whole-disk path, run only:

```go
runner.Run(ctx, smartctlPath, "--json=c", "--all", devicePath)
```

Run device reads concurrently with a maximum of four workers under the adapter's shared five-second context. Normalize overall failure or non-zero media/data-integrity errors to critical; temperature at least 70 C, pending/reallocated sectors, or percentage used at least 80 to warning; absent health data to unknown. Include model, serial, temperature, power-on hours, wear, and error counters only when present and bounded.

- [ ] **Step 4: Add realistic-output and manager-cache behavior tests and pass all SMART tests**

Run: `go test ./internal/modules/storage -run 'Test(SMART|ParseSMART)'`

Expected: PASS, including no second runner call within five minutes and stale fallback after a failed refresh.

- [ ] **Step 5: Commit SMART support**

```bash
git add internal/modules/storage/smart.go internal/modules/storage/smart_test.go internal/modules/storage/testdata/smart-*.json
git commit -m "feat: report SMART and NVMe health"
```

### Task 3: MD RAID Topology And Health

**Files:**
- Create: `internal/modules/storage/mdraid.go`
- Create: `internal/modules/storage/mdraid_test.go`
- Create: `internal/modules/storage/testdata/mdstat-healthy.txt`
- Create: `internal/modules/storage/testdata/mdstat-degraded.txt`
- Create: `internal/modules/storage/testdata/mdadm-detail.txt`

**Interfaces:**
- Produces: `NewMDRAIDEnricher(root, mdadmPath string) Enricher`.

- [ ] **Step 1: Write failing parser tests**

Assert RAID level, expected/active member counts, exact member relations, degraded critical health, and recovery progress. Add malformed-member, duplicate-array, oversized-line, and more-than-4,096-resource cases.

```go
assert.Contains(t, result.Relations, Relation{From: stableID("disk", "8:1"), To: stableID("raid", "md0"), Kind: "member-of"})
assert.Contains(t, result.Findings, Finding{ResourceID: stableID("raid", "md0"), Severity: HealthCritical, Title: "RAID array is degraded", Detail: "1 of 2 members active"})
```

- [ ] **Step 2: Run and observe failure**

Run: `go test ./internal/modules/storage -run 'Test(MDRAID|ParseMD)'`

Expected: FAIL because the adapter does not exist.

- [ ] **Step 3: Implement bounded MD collection**

Read only `<root>/proc/mdstat`. For each validated `/dev/md*` array found there, run only `mdadm --detail --export <array>`. Parse allowlisted `MD_*` keys, resolve members against validated core inventory, and reject a detail response for a different array.

- [ ] **Step 4: Run and pass MD tests**

Run: `go test ./internal/modules/storage -run 'Test(MDRAID|ParseMD)'`

Expected: PASS.

- [ ] **Step 5: Commit MD support**

```bash
git add internal/modules/storage/mdraid.go internal/modules/storage/mdraid_test.go internal/modules/storage/testdata/mdstat-* internal/modules/storage/testdata/mdadm-detail.txt
git commit -m "feat: report MD RAID topology"
```

### Task 4: LVM, LUKS/Device-Mapper, And Multipath

**Files:**
- Create: `internal/modules/storage/lvm.go`
- Create: `internal/modules/storage/lvm_test.go`
- Create: `internal/modules/storage/devicemapper.go`
- Create: `internal/modules/storage/devicemapper_test.go`
- Create: `internal/modules/storage/testdata/lvm.json`
- Create: `internal/modules/storage/testdata/dm-info.txt`
- Create: `internal/modules/storage/testdata/multipath.txt`

**Interfaces:**
- Produces: `NewLVMEnricher(paths LVMTools) Enricher`, `NewDeviceMapperEnricher(dmsetupPath, multipathdPath string) Enricher`.

- [ ] **Step 1: Write LVM graph tests**

Test PV-to-VG `member-of`, VG-to-LV `contains`, LV resource sizing, data/metadata utilization, partial/missing-device critical health, malformed JSON, unknown references, duplicate IDs, and byte limits.

- [ ] **Step 2: Write device-mapper/multipath tests**

Test crypt mapping `maps-to`, active/inactive state, multipath path counts, degraded warning, failed-path critical, and rejection of table/key material. Assert no parser or fixture contains a crypt key or raw `dmsetup table` output.

- [ ] **Step 3: Run tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(LVM|DeviceMapper|Multipath|ParseLVM|ParseDM)'`

Expected: FAIL because adapters do not exist.

- [ ] **Step 4: Implement fixed LVM reports**

Run `pvs`, `vgs`, and `lvs` only with `--reportformat json --units b --nosuffix` and explicit `-o` field lists defined as constants in `lvm.go`. Do not request environment, tags, or arbitrary metadata. Decode with unknown-field rejection and correlate by UUID.

- [ ] **Step 5: Implement safe mapper and multipath reports**

Use only `dmsetup info --columns --noheadings --separator | -o name,uuid,major,minor,open,segments`, `multipathd show maps raw format %n|%w|%d|%N|%t`, and `multipathd show paths raw format %m|%d|%t|%o`. The map report supplies alias, UUID, sysfs device, path count, and dm state; the path report supplies map alias, path device, dm state, and checker state so degraded/failed counts are measured rather than inferred. Never run `dmsetup table`. Treat `CRYPT-LUKS` UUID prefixes as encrypted mappings and correlate major/minor or validated device-path identities.

- [ ] **Step 6: Run and pass all layered-device tests**

Run: `go test ./internal/modules/storage -run 'Test(LVM|DeviceMapper|Multipath|ParseLVM|ParseDM)'`

Expected: PASS.

- [ ] **Step 7: Commit layered storage support**

```bash
git add internal/modules/storage/lvm.go internal/modules/storage/lvm_test.go internal/modules/storage/devicemapper.go internal/modules/storage/devicemapper_test.go internal/modules/storage/testdata/lvm.json internal/modules/storage/testdata/dm-info.txt internal/modules/storage/testdata/multipath.txt
git commit -m "feat: report layered block storage"
```

### Task 5: ZFS And Btrfs

**Files:**
- Create: `internal/modules/storage/zfs.go`
- Create: `internal/modules/storage/zfs_test.go`
- Create: `internal/modules/storage/btrfs.go`
- Create: `internal/modules/storage/btrfs_test.go`
- Create: `internal/modules/storage/testdata/zpool-list.txt`
- Create: `internal/modules/storage/testdata/zpool-status.txt`
- Create: `internal/modules/storage/testdata/zfs-list.txt`
- Create: `internal/modules/storage/testdata/btrfs-usage.txt`
- Create: `internal/modules/storage/testdata/btrfs-stats.txt`

**Interfaces:**
- Produces: `NewZFSEnricher(paths ZFSTools) Enricher`, `NewBtrfsEnricher(path string) Enricher`.

- [ ] **Step 1: Write ZFS tests**

Assert pool/dataset relations, pool allocation, degraded/faulted critical health, error counts, deterministic ordering, malformed tabular fields, missing pool references, and aggregate capacity that counts a top-level allocatable pool once without also counting its mounted datasets.

- [ ] **Step 2: Write Btrfs tests**

Assert filesystem/device/subvolume resources, mount attachment, allocation details, missing-device critical health, non-zero device-error warning, duplicate filesystem UUID rejection, bounded output, and aggregate capacity that does not count devices, filesystem, subvolumes, and mounts as separate usable bytes.

- [ ] **Step 3: Run tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(ZFS|Btrfs|ParseZFS|ParseBtrfs)'`

Expected: FAIL because adapters do not exist.

- [ ] **Step 4: Implement fixed ZFS collection**

Use only:

```text
zpool list -Hp -o name,size,alloc,free,cap,health
zpool status -P
zfs list -Hp -o name,type,used,available,refer,mountpoint
```

Parse tab-separated list output and a bounded state-machine for status output. Accept only pool names returned by the fixed list command.

- [ ] **Step 5: Implement fixed Btrfs collection**

For each validated active Btrfs mount from `Inventory.Mounts`, run only:

```text
btrfs filesystem usage -b --raw <target>
btrfs device stats <target>
btrfs subvolume list -o <target>
```

Reject a result that identifies a different filesystem/mount, and stop at the shared resource and output limits.

- [ ] **Step 6: Run and pass filesystem-backend tests**

Run: `go test ./internal/modules/storage -run 'Test(ZFS|Btrfs|ParseZFS|ParseBtrfs)'`

Expected: PASS.

- [ ] **Step 7: Commit pool/filesystem support**

```bash
git add internal/modules/storage/zfs.go internal/modules/storage/zfs_test.go internal/modules/storage/btrfs.go internal/modules/storage/btrfs_test.go internal/modules/storage/testdata/zpool-* internal/modules/storage/testdata/zfs-list.txt internal/modules/storage/testdata/btrfs-*.txt
git commit -m "feat: report ZFS and Btrfs storage"
```

### Task 6: Daemon Wiring And Backend Presentation

**Files:**
- Modify: `internal/modules/storage/tools.go`
- Modify: `cmd/pilothoused/main.go`
- Modify: `cmd/pilothoused/main_test.go`
- Modify: `internal/modules/storage/views.templ`
- Modify: `internal/modules/storage/views_test.go`

**Interfaces:**
- Consumes: all advanced constructors and existing hybrid Storage page.
- Produces: explicit availability for every specified backend on every host.

- [ ] **Step 1: Write daemon composition tests**

Factor adapter construction into `newStorageManager(toolResolver, root)` and test a host with no optional tools still creates a manager whose snapshot contains unsupported statuses for `smart`, `mdraid`, `lvm`, `device-mapper`, `multipath`, `zfs`, and `btrfs`.

- [ ] **Step 2: Write rendering tests for advanced details**

Assert exact visible labels for Temperature, Percentage used, RAID members, Recovery progress, LVM data usage, Encrypted mapping, Multipath paths, ZFS pool health, Btrfs device errors, Health data stale, and Backend unsupported. Continue asserting no literal `@web.` syntax.

- [ ] **Step 3: Run tests and verify failure**

Run: `go test ./cmd/pilothoused ./internal/modules/storage -run 'Test(StorageManagerComposition|RenderAdvanced)'`

Expected: FAIL until all adapters are composed and details rendered.

- [ ] **Step 4: Compose optional adapters**

Resolve each optional tool independently. Instantiate a real enricher when all tools it needs are safe and present; otherwise instantiate `NewUnsupportedEnricher(name)`. Keep core `lsblk` and `findmnt` startup-fatal. Reuse one `NewHealthCache()` result for SMART/NVMe only.

- [ ] **Step 5: Render generic typed details and backend states**

Extend existing resource details and backend-state components; do not add backend-specific HTTP branches. Use the supplied `Detail.Label` and escaped `Detail.Value`, preserving the existing 32-detail cap.

- [ ] **Step 6: Generate and run composition/render tests**

Run: `make generate && go test ./cmd/pilothoused ./internal/modules/storage -run 'Test(StorageManagerComposition|RenderAdvanced)'`

Expected: PASS.

- [ ] **Step 7: Commit integration**

```bash
git add internal/modules/storage/tools.go cmd/pilothoused/main.go cmd/pilothoused/main_test.go internal/modules/storage/views.templ internal/modules/storage/views_test.go
git commit -m "feat: integrate advanced storage backends"
```

### Task 7: Advanced Adapter Verification

**Files:**
- Test: all files changed by Tasks 1-6.

- [ ] **Step 1: Run generation and formatting**

Run: `make generate && make fmt`

Expected: both commands exit 0.

- [ ] **Step 2: Run storage tests with race detection**

Run: `go test -race ./internal/modules/storage ./cmd/pilothoused`

Expected: PASS with no race reports.

- [ ] **Step 3: Run required project verification**

Run: `make build && make test && make lint`

Expected: all commands exit 0. If native dependencies are missing, use and pass `make docker-build`, `make docker-test`, and `make docker-lint`.

- [ ] **Step 4: Inspect final scope**

Run: `git status --short && git diff --check && git diff --stat`

Expected: changes are confined to advanced Storage adapters, fixtures, daemon composition, and Storage rendering; no mutation action exists.
