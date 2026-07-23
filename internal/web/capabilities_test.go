package web

import (
	"errors"
	"fmt"
	"testing"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/stretchr/testify/assert"
)

func TestCapabilityCacheZeroValueIsEmptyAndNotDown(t *testing.T) {
	var cache capabilityCache
	assert.Empty(t, cache.get().List())
	assert.False(t, cache.staleAfterOutage())
}

func TestCapabilityCacheSetStoresAndClearsDown(t *testing.T) {
	var cache capabilityCache
	cache.noteResult(fmt.Errorf("dial: %w", broker.ErrUnavailable))
	assert.True(t, cache.staleAfterOutage())

	caps := capability.New(capability.Systemd, capability.Podman)
	cache.set(caps)
	assert.Equal(t, caps.List(), cache.get().List())
	assert.False(t, cache.staleAfterOutage())
}

func TestCapabilityCacheNoteResultOnlyMarksDownOnUnavailable(t *testing.T) {
	var cache capabilityCache
	cache.noteResult(nil)
	assert.False(t, cache.staleAfterOutage())

	cache.noteResult(broker.ErrUnauthorized)
	assert.False(t, cache.staleAfterOutage())

	cache.noteResult(errors.New("some domain error"))
	assert.False(t, cache.staleAfterOutage())

	cache.noteResult(fmt.Errorf("dial unix: %w", broker.ErrUnavailable))
	assert.True(t, cache.staleAfterOutage())
}

func TestCapabilityCacheNoteResultNeverClearsDown(t *testing.T) {
	var cache capabilityCache
	cache.noteResult(fmt.Errorf("dial unix: %w", broker.ErrUnavailable))
	assert.True(t, cache.staleAfterOutage())

	cache.noteResult(nil)
	assert.True(t, cache.staleAfterOutage())

	cache.noteResult(broker.ErrUnauthorized)
	assert.True(t, cache.staleAfterOutage())
}
