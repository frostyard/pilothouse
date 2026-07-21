package storage

import (
	"context"
	"errors"
	"fmt"
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
	err       error
	inventory Inventory
	mutate    bool
	name      string
	result    AdapterResult
}

func (e *fakeEnricher) Collect(_ context.Context, inventory Inventory) (AdapterResult, error) {
	e.inventory = inventory
	if e.mutate {
		inventory.DevicePaths[0] = "modified"
	}
	return e.result, e.err
}

func (e *fakeEnricher) Name() string { return e.name }

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

	_, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"/dev/sda"}, enricher.inventory.DevicePaths)
}

func TestManagerClonesInventoryForEachEnricher(t *testing.T) {
	first := &fakeEnricher{name: "first", mutate: true}
	second := &fakeEnricher{name: "second"}
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{first, second})

	_, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"/dev/sda"}, second.inventory.DevicePaths)
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
	manager := newSystemManagerWithEnrichers([]Adapter{coreFixtureAdapter()}, []Enricher{&fakeEnricher{name: "smart", err: errors.New("unavailable")}})
	manager.cache = cache

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Contains(t, snapshot.Resources[0].Details, Detail{Label: "Health data", Value: "Stale"})
}

func coreFixtureAdapter() Adapter {
	return fakeAdapter{core: true, name: "block", result: AdapterResult{Resources: []Resource{{ID: "disk:one", Kind: "disk", Name: "sda", Path: "/dev/sda"}}}}
}
