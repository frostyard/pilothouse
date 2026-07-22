package storage

import (
	"slices"
	"sync"
	"time"
)

const healthCacheTTL = 5 * time.Minute

type cachedHealth struct {
	collectedAt time.Time
	result      AdapterResult
}

type HealthCache struct {
	entries map[string]cachedHealth
	now     func() time.Time
	mu      sync.RWMutex
}

func NewHealthCache() *HealthCache {
	return newHealthCache(time.Now)
}

func newHealthCache(now func() time.Time) *HealthCache {
	return &HealthCache{entries: make(map[string]cachedHealth), now: now}
}

func (c *HealthCache) Load(key string) (AdapterResult, bool, bool) {
	c.mu.RLock()
	entry, found := c.entries[key]
	c.mu.RUnlock()
	if !found {
		return AdapterResult{}, false, false
	}
	return cloneAdapterResult(entry.result), c.now().Sub(entry.collectedAt) < healthCacheTTL, true
}

func (c *HealthCache) Store(key string, result AdapterResult) {
	c.mu.Lock()
	c.entries[key] = cachedHealth{collectedAt: c.now(), result: cloneAdapterResult(result)}
	c.mu.Unlock()
}

func cloneAdapterResult(result AdapterResult) AdapterResult {
	clone := AdapterResult{
		Findings:  slices.Clone(result.Findings),
		Mounts:    slices.Clone(result.Mounts),
		Relations: slices.Clone(result.Relations),
		Resources: slices.Clone(result.Resources),
		Truncated: result.Truncated,
	}
	for i := range clone.Mounts {
		clone.Mounts[i].Options = slices.Clone(clone.Mounts[i].Options)
	}
	for i := range clone.Resources {
		clone.Resources[i].Details = slices.Clone(clone.Resources[i].Details)
	}
	return clone
}
