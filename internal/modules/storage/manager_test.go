package storage

import (
	"context"
	"errors"
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
