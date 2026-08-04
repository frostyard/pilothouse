package incus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetailProjectsAllowlistedInstanceShape(t *testing.T) {
	value, err := NewSystemManager(stateClient()).Detail(context.Background(), "production", "api")
	require.NoError(t, err)

	assert.Equal(t, "production", value.Project)
	assert.Equal(t, "api", value.Instance.Name)
	assert.Equal(t, "x86_64", value.Architecture)
	assert.Equal(t, []string{"default"}, value.Profiles)
	assert.Equal(t, "2026-07-01T09:00:00Z", value.CreatedAt)
	assert.Equal(t, uint64(2147483648), value.MemoryTotal)

	// Snapshots arrive with the "instance/" prefix trimmed and sorted.
	assert.Equal(t, []Snapshot{
		{CreatedAt: "2026-07-20T03:00:00Z", Name: "before-upgrade", Stateful: true},
		{CreatedAt: "2026-07-30T03:00:00Z", Name: "nightly"},
	}, value.Snapshots)

	// Interfaces keep every address with its netmask, including loopback
	// and link-local, because this is the per-interface view rather than
	// the list's "where do I reach it" summary.
	require.Len(t, value.Networks, 2)
	assert.Equal(t, "eth0", value.Networks[0].Name)
	assert.Equal(t, []string{"10.0.0.5/24", "fd42::5/64", "fe80::1/64"}, value.Networks[0].Addresses)
	assert.Equal(t, "lo", value.Networks[1].Name)
}

// TestDetailExcludesSecretConfiguration is the behavioral guarantee behind
// the allowlist: it drives the real manager over a fixture whose
// configuration carries cloud-init payloads, process environment, raw
// passthrough and non-base_image volatile keys, then asserts none of it
// survives into the model that crosses the broker boundary — checked
// against the serialized JSON, so a key reaching any field of any nested
// struct is caught rather than only the ones this test thought to name.
func TestDetailExcludesSecretConfiguration(t *testing.T) {
	value, err := NewSystemManager(stateClient()).Detail(context.Background(), "production", "api")
	require.NoError(t, err)

	encoded, err := json.Marshal(value)
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

	// The allowlisted keys really are present, so the assertions above
	// are proving exclusion rather than an empty model.
	assert.Equal(t, []ConfigEntry{
		{Key: "image.description", Value: "Ubuntu 24.04"},
		{Key: "image.os", Value: "Ubuntu"},
		{Key: "limits.memory", Value: "2GiB"},
		{Key: "security.nesting", Value: "true"},
		{Key: "volatile.base_image", Value: imageFingerprint},
	}, value.Config)
}

// TestDetailDeviceAllowlistIsPerType proves device properties are filtered
// by the device's own type: a gpu's reviewed properties survive while its
// unreviewed "uid" does not, and a device type absent from the allowlist
// contributes its name and type only.
func TestDetailDeviceAllowlistIsPerType(t *testing.T) {
	value, err := NewSystemManager(stateClient()).Detail(context.Background(), "production", "api")
	require.NoError(t, err)

	devices := map[string]Device{}
	for _, device := range value.Devices {
		devices[device.Name] = device
	}

	require.Contains(t, devices, "gpu0")
	assert.Equal(t, "gpu", devices["gpu0"].Type)
	assert.Equal(t, []ConfigEntry{
		{Key: "gputype", Value: "physical"},
		{Key: "pci", Value: "0000:01:00.0"},
	}, devices["gpu0"].Properties, "an unreviewed property such as uid is dropped")

	require.Contains(t, devices, "eth0")
	assert.Equal(t, []ConfigEntry{
		{Key: "hwaddr", Value: "00:16:3e:aa:bb:cc"},
		{Key: "network", Value: "incusbr0"},
		{Key: "nictype", Value: "bridged"},
	}, devices["eth0"].Properties)

	// A device type with no allowlist entry exposes nothing beyond its
	// identity, so a device kind added by a future Incus release cannot
	// leak properties before it is reviewed.
	client := stateClient()
	client.instances[1].Devices = api.DevicesMap{
		"future": {"type": "brand-new-kind", "secret": "should-not-appear", "path": "/etc/shadow"},
	}
	value, err = NewSystemManager(client).Detail(context.Background(), "production", "api")
	require.NoError(t, err)
	require.Len(t, value.Devices, 1)
	assert.Equal(t, "future", value.Devices[0].Name)
	assert.Equal(t, "brand-new-kind", value.Devices[0].Type)
	assert.Empty(t, value.Devices[0].Properties)

	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "should-not-appear")
	assert.NotContains(t, string(encoded), "/etc/shadow")
}

func TestDetailValidatesNameAndProject(t *testing.T) {
	manager := NewSystemManager(stateClient())

	_, err := manager.Detail(context.Background(), "production", "../default/api")
	assert.EqualError(t, err, "invalid instance name")

	_, err = manager.Detail(context.Background(), "missing", "api")
	assert.EqualError(t, err, "project is not available")
}

// TestDetailRejectsNameBeforeReachingTheAPI proves the name validation is a
// gate, not a label: a rejected name produces no client call at all.
func TestDetailRejectsNameBeforeReachingTheAPI(t *testing.T) {
	client := stateClient()
	_, err := NewSystemManager(client).Detail(context.Background(), "production", "../default/api")
	require.Error(t, err)
	for _, action := range client.actions {
		assert.False(t, strings.HasPrefix(action, "instance "), "no instance read may happen for a rejected name, got %q", action)
	}
}
