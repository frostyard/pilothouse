package web

import (
	"errors"
	"sync"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
)

// capabilityCache holds the web process's own view of the broker's
// advertised capability.Set, independent of any module gating. It is
// refreshed opportunistically (see refreshCapabilities in server.go) and is
// safe for concurrent use.
type capabilityCache struct {
	mu   sync.Mutex
	caps capability.Set
	down bool
}

// get returns the currently cached Set. Before any successful login or
// capability fetch, this is the zero (all-absent) Set.
func (c *capabilityCache) get() capability.Set {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// set replaces the cached Set and clears the down flag.
func (c *capabilityCache) set(caps capability.Set) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caps = caps
	c.down = false
}

// noteResult records the outcome of a broker call that was not itself a
// capability fetch. It marks the cache down iff err wraps
// broker.ErrUnavailable; it never clears the down flag and it ignores nil
// and any other error (authorization failures, request-validation errors,
// and domain errors do not trigger a refetch).
func (c *capabilityCache) noteResult(err error) {
	if !errors.Is(err, broker.ErrUnavailable) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = true
}

// staleAfterOutage reports whether the cache was marked down by a prior
// transport/unavailable failure, meaning the next successful authenticated
// broker request should trigger a QueryCapabilities refetch.
func (c *capabilityCache) staleAfterOutage() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.down
}
