package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var ErrBackendUnsupported = errors.New("storage backend unsupported")

type Inventory struct {
	DevicePaths []string
	Mounts      []Mount
	Resources   []Resource
}

type Enricher interface {
	Collect(context.Context, Inventory) (AdapterResult, error)
	Name() string
}

type unsupportedEnricher struct{ name string }

func NewUnsupportedEnricher(name string) Enricher { return unsupportedEnricher{name: name} }

func (e unsupportedEnricher) Collect(context.Context, Inventory) (AdapterResult, error) {
	return AdapterResult{}, ErrBackendUnsupported
}

func (e unsupportedEnricher) Name() string { return e.name }

type SystemManager struct {
	adapters        []Adapter
	cache           *HealthCache
	enrichers       []Enricher
	enricherTimeout time.Duration
	lstat           func(string) (os.FileInfo, error)
	now             func() time.Time
}

func NewSystemManager(adapters ...Adapter) *SystemManager {
	return newSystemManagerWithEnrichers(adapters, nil)
}

func NewSystemManagerWithEnrichers(adapters []Adapter, enrichers []Enricher) *SystemManager {
	return newSystemManagerWithEnrichers(adapters, enrichers)
}

func newSystemManagerWithEnrichers(adapters []Adapter, enrichers []Enricher) *SystemManager {
	return &SystemManager{adapters: slices.Clone(adapters), cache: NewHealthCache(), enrichers: slices.Clone(enrichers), enricherTimeout: 5 * time.Second, lstat: os.Lstat, now: time.Now}
}

func (m *SystemManager) State(ctx context.Context) (Snapshot, error) {
	overall, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	results := make(chan collectedResult, len(m.adapters))
	for _, adapter := range m.adapters {
		go func(adapter Adapter) {
			adapterCtx, cancel := context.WithTimeout(overall, 5*time.Second)
			defer cancel()
			result, err := adapter.Collect(adapterCtx)
			if adapterCtx.Err() == context.DeadlineExceeded {
				err = context.DeadlineExceeded
			}
			results <- collectedResult{name: adapter.Name(), core: adapter.Core(), result: result, err: err}
		}(adapter)
	}
	collected := make([]collectedResult, 0, len(m.adapters))
	received := make(map[string]bool, len(m.adapters))
	for len(collected) < len(m.adapters) {
		select {
		case result := <-results:
			collected = append(collected, result)
			received[result.name] = true
		case <-overall.Done():
			for _, adapter := range m.adapters {
				if !received[adapter.Name()] {
					collected = append(collected, collectedResult{name: adapter.Name(), core: adapter.Core(), err: context.DeadlineExceeded})
				}
			}
		}
	}
	for _, result := range collected {
		if result.core && result.err != nil {
			return Snapshot{}, fmt.Errorf("core adapter %s: %w", result.name, result.err)
		}
	}
	core := make([]collectedResult, 0, len(collected))
	for _, result := range collected {
		if result.core {
			core = append(core, result)
		}
	}
	coreSnapshot, err := normalize(m.now(), core)
	if err != nil {
		return Snapshot{}, err
	}
	inventory := inventoryFromSnapshot(coreSnapshot, m.lstat)
	type enricherResult struct {
		index  int
		result AdapterResult
		err    error
	}
	enricherResults := make(chan enricherResult, len(m.enrichers))
	for index, enricher := range m.enrichers {
		go func(index int, enricher Enricher) {
			result, err := m.collectEnricher(overall, enricher, cloneInventory(inventory))
			enricherResults <- enricherResult{index: index, result: result, err: err}
		}(index, enricher)
	}
	enriched := make([]AdapterResult, len(m.enrichers))
	enricherErrors := make([]error, len(m.enrichers))
	receivedEnrichers := make([]bool, len(m.enrichers))
	for count := 0; count < len(m.enrichers); {
		select {
		case result := <-enricherResults:
			if !receivedEnrichers[result.index] {
				receivedEnrichers[result.index] = true
				count++
				enriched[result.index] = result.result
				enricherErrors[result.index] = result.err
			}
		case <-overall.Done():
			for index := range m.enrichers {
				if !receivedEnrichers[index] {
					receivedEnrichers[index] = true
					count++
					enricherErrors[index] = context.DeadlineExceeded
				}
			}
		}
	}
	for index, enricher := range m.enrichers {
		collected = append(collected, collectedResult{name: enricher.Name(), result: enriched[index], err: enricherErrors[index]})
	}
	snapshot, err := normalize(m.now(), collected)
	if err != nil {
		return Snapshot{}, err
	}
	for index, enricher := range m.enrichers {
		if enricherErrors[index] == nil {
			continue
		}
		for i := range snapshot.Backends {
			if snapshot.Backends[i].Name == enricher.Name() {
				snapshot.Backends[i].Availability = enricherAvailability(enricher, collected)
			}
		}
	}
	return snapshot, nil
}

func enricherAvailability(enricher Enricher, collected []collectedResult) Availability {
	for _, result := range collected {
		if result.name != enricher.Name() {
			continue
		}
		if errors.Is(result.err, ErrBackendUnsupported) {
			return BackendUnsupported
		}
		if errors.Is(result.err, context.DeadlineExceeded) {
			return BackendTimedOut
		}
		if result.err != nil {
			return BackendUnavailable
		}
		return BackendAvailable
	}
	return BackendUnavailable
}

func (m *SystemManager) collectEnricher(ctx context.Context, enricher Enricher, inventory Inventory) (AdapterResult, error) {
	cached, fresh, found := m.cache.Load(enricher.Name())
	if found && fresh {
		return cached, nil
	}
	enricherCtx, cancel := context.WithTimeout(ctx, m.enricherTimeout)
	defer cancel()
	result, err := enricher.Collect(enricherCtx, inventory)
	if enricherCtx.Err() == context.DeadlineExceeded {
		err = context.DeadlineExceeded
	}
	if err == nil {
		m.cache.Store(enricher.Name(), result)
		return result, nil
	}
	if found {
		markStale(&cached)
		return cached, err
	}
	return AdapterResult{}, err
}

func inventoryFromSnapshot(snapshot Snapshot, lstat func(string) (os.FileInfo, error)) Inventory {
	inventory := Inventory{Mounts: slices.Clone(snapshot.Mounts), Resources: slices.Clone(snapshot.Resources)}
	for _, resource := range snapshot.Resources {
		if resource.Kind != "disk" && resource.Kind != "partition" && resource.Kind != "raid" && resource.Kind != "mapping" {
			continue
		}
		path := filepath.Clean(resource.Path)
		if resource.Path != path || !filepath.IsAbs(path) || !strings.HasPrefix(path, "/dev/") {
			continue
		}
		if hasSymlinkComponent(path, lstat) {
			continue
		}
		inventory.DevicePaths = append(inventory.DevicePaths, path)
	}
	return inventory
}

func cloneInventory(inventory Inventory) Inventory {
	result := cloneAdapterResult(AdapterResult{Mounts: inventory.Mounts, Resources: inventory.Resources})
	return Inventory{DevicePaths: slices.Clone(inventory.DevicePaths), Mounts: result.Mounts, Resources: result.Resources}
}

func hasSymlinkComponent(path string, lstat func(string) (os.FileInfo, error)) bool {
	current := "/"
	info, err := lstat(current)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, component)
		info, err := lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

var staleHealthDetail = Detail{Label: "Health data", Value: "Stale"}

func markStale(result *AdapterResult) {
	for index := range result.Resources {
		details := result.Resources[index].Details
		if len(details) >= maxDetails {
			details = details[:maxDetails-1]
		}
		result.Resources[index].Details = append(details, staleHealthDetail)
	}
}

func mergeEnrichedResources(snapshot *Snapshot, results []AdapterResult) {
	resources := make(map[string]*Resource, len(snapshot.Resources))
	for i := range snapshot.Resources {
		resources[snapshot.Resources[i].ID] = &snapshot.Resources[i]
	}
	for _, result := range results {
		for _, enriched := range result.Resources {
			if resource, ok := resources[enriched.ID]; ok {
				resource.Details = mergeDetails(resource.Details, enriched.Details)
				resource.Health = higherHealth(resource.Health, enriched.Health)
			}
		}
	}
	sortSnapshot(snapshot)
	recalculateSummary(snapshot)
}

func mergeDetails(core, enriched []Detail) []Detail {
	result := slices.Clone(core)
	if len(result) >= maxDetails {
		return result[:maxDetails]
	}
	stale := -1
	for index, detail := range enriched {
		if detail == staleHealthDetail {
			stale = index
			break
		}
	}
	limit := maxDetails
	if stale >= 0 {
		limit--
	}
	for _, detail := range enriched {
		if detail == staleHealthDetail || len(result) == limit {
			continue
		}
		result = append(result, detail)
	}
	if stale >= 0 && len(result) < maxDetails {
		result = append(result, staleHealthDetail)
	}
	return result
}
