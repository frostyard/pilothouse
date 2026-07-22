package storage

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthCacheReturnsFreshValueBeforeFiveMinutes(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newHealthCache(func() time.Time { return now })
	cache.Store("disk:one", AdapterResult{Resources: []Resource{{ID: "disk:one", Health: HealthHealthy}}})
	now = now.Add(4*time.Minute + 59*time.Second)

	_, fresh, found := cache.Load("disk:one")

	assert.True(t, found)
	assert.True(t, fresh)
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

func TestHealthCacheClonesOnStoreAndLoad(t *testing.T) {
	cache := newHealthCache(time.Now)
	stored := AdapterResult{Resources: []Resource{{
		ID:      "disk:one",
		Details: []Detail{{Label: "status", Value: "healthy"}},
	}}}
	cache.Store("disk:one", stored)
	stored.Resources[0].Details[0].Value = "changed"

	loaded, _, found := cache.Load("disk:one")
	require.True(t, found)
	loaded.Resources[0].Details[0].Value = "changed again"
	loaded, _, found = cache.Load("disk:one")

	require.True(t, found)
	assert.Equal(t, "healthy", loaded.Resources[0].Details[0].Value)
}

func TestHealthCacheSupportsConcurrentLoadAndStore(t *testing.T) {
	cache := newHealthCache(time.Now)
	const workers = 16
	const operations = 100
	var group sync.WaitGroup
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for operation := range operations {
				cache.Store("disk:one", AdapterResult{Resources: []Resource{{ID: "disk:one", Name: string(rune(worker + operation))}}})
				cache.Load("disk:one")
			}
		}()
	}
	group.Wait()
}
