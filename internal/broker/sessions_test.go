package broker

import (
	"context"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionStoreEnforcesIdleAndAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(15*time.Minute, time.Hour)
	store.now = func() time.Time { return now }
	token, created, err := store.Create(auth.Identity{UID: 1000, Username: "snow"})
	require.NoError(t, err)
	assert.Equal(t, "snow", created.Identity.Username)

	now = now.Add(14 * time.Minute)
	_, ok := store.Get(token)
	assert.True(t, ok)
	now = now.Add(16 * time.Minute)
	_, ok = store.Get(token)
	assert.False(t, ok)

	token, _, err = store.Create(auth.Identity{UID: 1000, Username: "snow"})
	require.NoError(t, err)
	now = now.Add(61 * time.Minute)
	_, ok = store.Get(token)
	assert.False(t, ok)
}

func TestActionRegistryRequiresAdmin(t *testing.T) {
	registry := NewActionRegistry()
	called := false
	require.NoError(t, registry.Register("manage", true, func(_ context.Context, _ auth.Identity, _ map[string]string) error {
		called = true
		return nil
	}))

	err := registry.Execute(context.Background(), auth.Identity{Username: "viewer"}, "manage", nil, "")
	assert.Error(t, err)
	assert.False(t, called)
	require.NoError(t, registry.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, "manage", nil, ""))
	assert.True(t, called)
}
