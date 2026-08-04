package incus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileDetailProjectsAllowlistedShape(t *testing.T) {
	detail, err := NewSystemManager(stateClient()).ProfileDetail(context.Background(), "production", "default")
	require.NoError(t, err)

	assert.Equal(t, "default", detail.Name)
	assert.Equal(t, "production", detail.Project)
	assert.Equal(t, "Default Incus profile", detail.Description)
	assert.Equal(t, []string{"/1.0/instances/api"}, detail.UsedBy)

	devices := map[string]Device{}
	for _, device := range detail.Devices {
		devices[device.Name] = device
	}
	require.Contains(t, devices, "eth0")
	assert.Equal(t, []ConfigEntry{
		{Key: "name", Value: "eth0"},
		{Key: "network", Value: "incusbr0"},
	}, devices["eth0"].Properties)
	require.Contains(t, devices, "root")
	assert.Equal(t, []ConfigEntry{
		{Key: "path", Value: "/"},
		{Key: "pool", Value: "default"},
	}, devices["root"].Properties)
}

// TestProfileDetailExcludesSecretConfiguration proves the instance
// allowlist genuinely generalises to profiles. A profile is arguably the
// more sensitive of the two: its cloud-init payload applies to every
// instance that inherits it, so the same namespaces must be excluded.
func TestProfileDetailExcludesSecretConfiguration(t *testing.T) {
	detail, err := NewSystemManager(stateClient()).ProfileDetail(context.Background(), "production", "default")
	require.NoError(t, err)

	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	serialized := string(encoded)

	for _, forbidden := range []string{
		"user.user-data", "user.vendor-data", "cloud-init.user-data",
		"environment.SECRET", "raw.lxc",
		"volatile.eth0.hwaddr", "volatile.last_state.p",
	} {
		assert.NotContains(t, serialized, forbidden, "config key %q must not cross the broker boundary", forbidden)
	}
	for _, secret := range []string{"hunter2", "ssh-ed25519", "AAAAsecret", "apparmor", "vendor secret"} {
		assert.NotContains(t, serialized, secret, "secret value %q must not cross the broker boundary", secret)
	}

	// The allowlisted keys survive, so the exclusions prove a filter.
	assert.Equal(t, []ConfigEntry{
		{Key: "image.description", Value: "Ubuntu 24.04"},
		{Key: "image.os", Value: "Ubuntu"},
		{Key: "limits.memory", Value: "2GiB"},
		{Key: "security.nesting", Value: "true"},
		{Key: "volatile.base_image", Value: imageFingerprint},
	}, detail.Config)
}

func TestProfileDetailValidatesNameAndProject(t *testing.T) {
	manager := NewSystemManager(stateClient())

	_, err := manager.ProfileDetail(context.Background(), "production", "../instances/api")
	assert.EqualError(t, err, "invalid profile name")

	_, err = manager.ProfileDetail(context.Background(), "missing", "default")
	assert.EqualError(t, err, "project is not available")
}

func TestUsedByLabelExtractsName(t *testing.T) {
	assert.Equal(t, "web-test", usedByLabel("/1.0/instances/web-test"))
	assert.Equal(t, "default", usedByLabel("/1.0/profiles/default"))
	assert.Equal(t, "a b", usedByLabel("/1.0/instances/a%20b"))
	assert.Equal(t, "bare", usedByLabel("bare"))
	assert.Equal(t, "", usedByLabel(""))
}
