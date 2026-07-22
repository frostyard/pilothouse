package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAdapter struct {
	core   bool
	delay  time.Duration
	err    error
	name   string
	result AdapterResult
}

func (a fakeAdapter) Collect(ctx context.Context) (AdapterResult, error) {
	if a.delay > 0 {
		select {
		case <-time.After(a.delay):
		case <-ctx.Done():
			return AdapterResult{}, ctx.Err()
		}
	}
	return a.result, a.err
}

func (a fakeAdapter) Core() bool   { return a.core }
func (a fakeAdapter) Name() string { return a.name }

type fakeEnricher struct {
	cache     bool
	calls     int
	err       error
	inventory Inventory
	mutate    bool
	name      string
	result    AdapterResult
}

func (e *fakeEnricher) Collect(_ context.Context, inventory Inventory) (AdapterResult, error) {
	e.calls++
	e.inventory = inventory
	if e.mutate {
		inventory.DevicePaths[0] = "modified"
	}
	return e.result, e.err
}

func (e *fakeEnricher) Name() string      { return e.name }
func (e *fakeEnricher) CacheHealth() bool { return e.cache }

type blockingEnricher struct {
	collect func(context.Context, Inventory) (AdapterResult, error)
	name    string
}

func (e blockingEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	return e.collect(ctx, inventory)
}

func (e blockingEnricher) Name() string { return e.name }

func TestSystemManagerFailsWhenCoreAdapterFails(t *testing.T) {
	manager := NewSystemManager(fakeAdapter{core: true, err: errors.New("failed"), name: "block"})
	_, err := manager.State(context.Background())
	assert.ErrorContains(t, err, "block")
}

func TestSystemManagerDegradesOptionalAdapter(t *testing.T) {
	manager := NewSystemManager(fakeAdapter{name: "health", err: errors.New("failed")})
	snapshot, err := manager.State(context.Background())
	require.NoError(t, err)
	require.Len(t, snapshot.Backends, 1)
	assert.Equal(t, BackendUnavailable, snapshot.Backends[0].Availability)
}

func TestSystemManagerTimesOutAdapter(t *testing.T) {
	manager := NewSystemManager(fakeAdapter{name: "health", delay: 6 * time.Second})
	snapshot, err := manager.State(context.Background())
	require.NoError(t, err)
	require.Len(t, snapshot.Backends, 1)
	assert.Equal(t, BackendTimedOut, snapshot.Backends[0].Availability)
}

func TestManagerPassesValidatedCoreInventoryToEnrichers(t *testing.T) {
	enricher := &fakeEnricher{name: "smart"}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})
	manager.lstat = existingDeviceLstat(t)

	_, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"/dev/sda"}, enricher.inventory.DevicePaths)
}

func TestManagerClonesInventoryForEachEnricher(t *testing.T) {
	first := &fakeEnricher{name: "first", mutate: true}
	second := &fakeEnricher{name: "second"}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{first, second})
	manager.lstat = existingDeviceLstat(t)

	_, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"/dev/sda"}, second.inventory.DevicePaths)
}

func TestManagerRetainsNewEnricherResourcesAndRelations(t *testing.T) {
	raid := Resource{ID: "raid:one", Kind: "raid", Name: "md0", Path: "/dev/md0", Health: HealthWarning, State: "degraded"}
	enricher := &fakeEnricher{name: "mdraid", result: AdapterResult{
		Resources: []Resource{raid},
		Relations: []Relation{{From: "disk:one", To: raid.ID, Kind: "member-of"}},
		Findings:  []Finding{{ResourceID: raid.ID, Severity: HealthWarning, Title: "RAID array is degraded"}},
	}}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Contains(t, snapshot.Resources, raid)
	assert.Contains(t, snapshot.Relations, Relation{From: "disk:one", To: raid.ID, Kind: "member-of"})
	assert.Contains(t, snapshot.Findings, Finding{ResourceID: raid.ID, Severity: HealthWarning, Title: "RAID array is degraded"})
	assert.Equal(t, BackendAvailable, backendStatus(snapshot.Backends, "mdraid").Availability)
}

func TestManagerMergesExistingEnricherResourceHealthDetailsAndState(t *testing.T) {
	enricher := &fakeEnricher{name: "mdraid", result: AdapterResult{Resources: []Resource{{
		ID: "disk:one", Health: HealthWarning, State: "degraded", Details: []Detail{{Label: "RAID", Value: "member"}},
	}}}}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	resource := snapshot.Resources[0]
	assert.Equal(t, HealthWarning, resource.Health)
	assert.Equal(t, "degraded", resource.State)
	assert.Contains(t, resource.Details, Detail{Label: "RAID", Value: "member"})
}

func TestManagerLocalizesDanglingEnricherAdditions(t *testing.T) {
	enricher := &fakeEnricher{name: "mdraid", result: AdapterResult{
		Relations: []Relation{{From: "disk:one", To: "raid:missing", Kind: "member-of"}},
		Findings:  []Finding{{ResourceID: "raid:missing", Severity: HealthWarning, Title: "RAID array is degraded"}},
	}}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.NotContains(t, snapshot.Relations, Relation{From: "disk:one", To: "raid:missing", Kind: "member-of"})
	assert.NotContains(t, snapshot.Findings, Finding{ResourceID: "raid:missing", Severity: HealthWarning, Title: "RAID array is degraded"})
	assert.Equal(t, BackendUnavailable, backendStatus(snapshot.Backends, "mdraid").Availability)
}

func TestManagerMapsUnsupportedEnricher(t *testing.T) {
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{NewUnsupportedEnricher("smart")})

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	require.Len(t, snapshot.Backends, 2)
	assert.Equal(t, BackendUnsupported, snapshot.Backends[1].Availability)
}

func TestManagerMapsWrappedEnricherDeadline(t *testing.T) {
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{&fakeEnricher{name: "smart", err: fmt.Errorf("collect: %w", context.DeadlineExceeded)}})

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	require.Len(t, snapshot.Backends, 2)
	assert.Equal(t, BackendTimedOut, snapshot.Backends[1].Availability)
}

func TestManagerMarksStaleEnricherResult(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newHealthCache(func() time.Time { return now })
	cache.Store("smart", AdapterResult{Resources: []Resource{{ID: "disk:one", Details: []Detail{{Label: "temperature", Value: "30 C"}}}}})
	now = now.Add(6 * time.Minute)
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{&fakeEnricher{name: "smart", cache: true, err: errors.New("unavailable")}})
	manager.cache = cache

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Contains(t, snapshot.Resources[0].Details, Detail{Label: "Health data", Value: "Stale"})
}

func TestManagerOwnsSMARTCacheAndStaleFallback(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newHealthCache(func() time.Time { return now })
	cache.Store("other", AdapterResult{Resources: []Resource{{ID: "other"}}})
	enricher := &fakeEnricher{name: "smart", cache: true, result: AdapterResult{Resources: []Resource{{ID: "disk:one", Details: []Detail{{Label: "Temperature", Value: "30 C"}}}}}}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})
	manager.cache = cache

	_, err := manager.State(context.Background())
	require.NoError(t, err)
	require.Len(t, cache.entries, 2)
	collectedAt := cache.entries["smart"].collectedAt
	now = now.Add(time.Minute)
	_, err = manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, enricher.calls)
	assert.Equal(t, 2, len(cache.entries))
	assert.Equal(t, collectedAt, cache.entries["smart"].collectedAt)

	now = now.Add(healthCacheTTL + time.Second)
	enricher.err = errors.New("unavailable")
	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 2, enricher.calls)
	assert.Equal(t, 2, len(cache.entries))
	assert.Equal(t, 1, detailCount(snapshot.Resources[0].Details, staleHealthDetail))
	assert.Contains(t, cache.entries, "other")
	secondSnapshot, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, enricher.calls)
	assert.Equal(t, 1, detailCount(secondSnapshot.Resources[0].Details, staleHealthDetail))
}

func TestManagerDoesNotCacheDynamicEnrichers(t *testing.T) {
	enricher := &fakeEnricher{name: "mdraid"}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})

	_, err := manager.State(context.Background())
	require.NoError(t, err)
	_, err = manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, enricher.calls)
}

func TestManagerCachesSMARTReturns(t *testing.T) {
	calls := 0
	enricher := newSMARTEnricher("smartctl")
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return mustFixture(t, "smart-ata.json"), nil
	}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{enricher})
	manager.lstat = existingDeviceLstat(t)

	_, err := manager.State(context.Background())
	require.NoError(t, err)
	_, err = manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestManagerRunsEnrichersConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	newEnricher := func(name string) Enricher {
		return blockingEnricher{name: name, collect: func(context.Context, Inventory) (AdapterResult, error) {
			started <- name
			<-release
			return AdapterResult{}, nil
		}}
	}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{newEnricher("first"), newEnricher("second")})
	done := make(chan error, 1)
	go func() { _, err := manager.State(context.Background()); done <- err }()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	startedNames := make([]string, 0, 2)
	for range 2 {
		select {
		case name := <-started:
			startedNames = append(startedNames, name)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("enrichers did not start concurrently")
		}
	}
	require.ElementsMatch(t, []string{"first", "second"}, startedNames)
	close(release)
	released = true
	require.NoError(t, <-done)
}

func TestManagerLocalizesEnricherTimeout(t *testing.T) {
	timedOut := blockingEnricher{name: "timed-out", collect: func(ctx context.Context, _ Inventory) (AdapterResult, error) {
		<-ctx.Done()
		return AdapterResult{}, ctx.Err()
	}}
	available := blockingEnricher{name: "available", collect: func(context.Context, Inventory) (AdapterResult, error) {
		return AdapterResult{}, nil
	}}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{timedOut, available})
	manager.enricherTimeout = 10 * time.Millisecond

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []BackendStatus{{Name: "available", Availability: BackendAvailable}, {Name: "block", Availability: BackendAvailable}, {Name: "timed-out", Availability: BackendTimedOut}}, backendStatuses(snapshot.Backends))
}

func TestInventoryExcludesMissingAndSymlinkDevicePaths(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	require.NoError(t, err)
	lstat := func(path string) (os.FileInfo, error) {
		switch path {
		case "/", "/dev", "/dev/sda":
			return info, nil
		case "/dev/link":
			return symlinkFileInfo{FileInfo: info}, nil
		case "/dev/link/sda":
			return info, nil
		default:
			return nil, os.ErrNotExist
		}
	}
	snapshot := Snapshot{Resources: []Resource{
		{ID: "disk:valid", Kind: "disk", Path: "/dev/sda"},
		{ID: "disk:missing", Kind: "disk", Path: "/dev/missing"},
		{ID: "disk:link", Kind: "disk", Path: "/dev/link/sda"},
	}}

	inventory := inventoryFromSnapshot(snapshot, lstat)

	assert.Equal(t, []string{"/dev/sda"}, inventory.DevicePaths)
}

func TestMergeEnrichedDetailsCapsAtMaximum(t *testing.T) {
	core := makeDetails("core", 30)
	enriched := makeDetails("enriched", 4)
	snapshot := Snapshot{Resources: []Resource{{ID: "disk:one", Details: core}}}

	mergeEnrichedResources(&snapshot, []AdapterResult{{Resources: []Resource{{ID: "disk:one", Details: enriched}}}})

	assert.Equal(t, append(core, enriched[:2]...), snapshot.Resources[0].Details)
}

func TestMergeEnrichedDetailsRetainsStaleMarkerAtMaximum(t *testing.T) {
	core := makeDetails("core", 30)
	enriched := append(makeDetails("enriched", 3), staleHealthDetail)
	snapshot := Snapshot{Resources: []Resource{{ID: "disk:one", Details: core}}}

	mergeEnrichedResources(&snapshot, []AdapterResult{{Resources: []Resource{{ID: "disk:one", Details: enriched}}}})

	assert.Equal(t, append(append(core, enriched[:1]...), staleHealthDetail), snapshot.Resources[0].Details)
}

func TestStaleDetailsRetainMarkerAtMaximum(t *testing.T) {
	result := AdapterResult{Resources: []Resource{{Details: makeDetails("cached", maxDetails)}}}

	markStale(&result)

	assert.Len(t, result.Resources[0].Details, maxDetails)
	assert.Equal(t, staleHealthDetail, result.Resources[0].Details[maxDetails-1])
}

func TestStaleDetailsTrimOverMaximumAndRetainMarker(t *testing.T) {
	result := AdapterResult{Resources: []Resource{{Details: makeDetails("cached", maxDetails+2)}}}

	markStale(&result)

	assert.Len(t, result.Resources[0].Details, maxDetails)
	assert.Equal(t, staleHealthDetail, result.Resources[0].Details[maxDetails-1])
}

type symlinkFileInfo struct{ os.FileInfo }

func (info symlinkFileInfo) Mode() os.FileMode { return info.FileInfo.Mode() | os.ModeSymlink }

func makeDetails(prefix string, count int) []Detail {
	details := make([]Detail, count)
	for index := range details {
		details[index] = Detail{Label: fmt.Sprintf("%s-%d", prefix, index)}
	}
	return details
}

func detailCount(details []Detail, want Detail) int {
	count := 0
	for _, detail := range details {
		if detail == want {
			count++
		}
	}
	return count
}

func backendStatuses(backends []BackendStatus) []BackendStatus {
	statuses := slices.Clone(backends)
	for index := range statuses {
		statuses[index].CollectedAt = time.Time{}
	}
	return statuses
}

func backendStatus(backends []BackendStatus, name string) BackendStatus {
	for _, backend := range backends {
		if backend.Name == name {
			return backend
		}
	}
	return BackendStatus{}
}

func existingDeviceLstat(t *testing.T) func(string) (os.FileInfo, error) {
	t.Helper()
	info, err := os.Stat(t.TempDir())
	require.NoError(t, err)
	return func(path string) (os.FileInfo, error) {
		if path == "/" || path == "/dev" || path == "/dev/sda" {
			return info, nil
		}
		return nil, os.ErrNotExist
	}
}

func coreFixtureAdapter() Adapter {
	return fakeAdapter{core: true, name: "block", result: AdapterResult{Resources: []Resource{{ID: "disk:one", Kind: "disk", Name: "sda", Path: "/dev/sda"}}}}
}
