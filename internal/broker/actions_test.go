package broker

import (
	"context"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryRegistryAuthorizationAndDuplicateProtection(t *testing.T) {
	registry := NewQueryRegistry()
	require.NoError(t, registry.Register("system.read", false, func(_ context.Context, identity auth.Identity, _ map[string]string) (any, error) {
		return identity.Username, nil
	}))
	result, err := registry.Execute(context.Background(), auth.Identity{Username: "snow"}, "system.read", nil)
	require.NoError(t, err)
	assert.Equal(t, "snow", result)
	assert.Error(t, registry.Register("system.read", false, func(context.Context, auth.Identity, map[string]string) (any, error) { return nil, nil }))

	require.NoError(t, registry.Register("system.private", true, func(context.Context, auth.Identity, map[string]string) (any, error) { return "secret", nil }))
	_, err = registry.Execute(context.Background(), auth.Identity{Username: "snow"}, "system.private", nil)
	assert.ErrorContains(t, err, "not authorized")
}

func TestQueryRegistryRegistered(t *testing.T) {
	registry := NewQueryRegistry()
	assert.False(t, registry.Registered("system.read"))
	require.NoError(t, registry.Register("system.read", false, func(context.Context, auth.Identity, map[string]string) (any, error) { return nil, nil }))
	assert.True(t, registry.Registered("system.read"))
	assert.False(t, registry.Registered("system.other"))
}

func TestActionRegistryRegistered(t *testing.T) {
	registry := NewActionRegistry()
	assert.False(t, registry.Registered("system.act"))
	require.NoError(t, registry.Register("system.act", false, func(context.Context, auth.Identity, map[string]string) error { return nil }))
	assert.True(t, registry.Registered("system.act"))
	assert.False(t, registry.Registered("system.other"))
}
