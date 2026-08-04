package incus

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/stretchr/testify/require"
)

func TestLiveSystemManagerState(t *testing.T) {
	if os.Getenv("PILOTHOUSE_LIVE_INCUS") != "1" {
		t.Skip("set PILOTHOUSE_LIVE_INCUS=1 to inspect the local Incus daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := NewLocalClient()
	state, err := NewSystemManager(client).State(ctx, "default")
	require.NoError(t, err)
	require.NotEmpty(t, state.Version)
	require.NotEmpty(t, state.Projects)
	t.Logf("Incus %s: project %s of %d, %d instances, %d images", state.Version, state.Project, len(state.Projects), len(state.Instances), len(state.Images))
	t.Logf("networks=%d profiles=%d pools=%d warnings=%v", len(state.Networks), len(state.Profiles), len(state.Pools), state.Warnings)

	// Every instance's live fields must be either a real measurement or
	// the documented "not reported" zero — never a sentinel such as
	// Incus's -1 process count.
	for _, instance := range state.Instances {
		require.GreaterOrEqual(t, instance.Processes, int64(0), "instance %s", instance.Name)
		require.GreaterOrEqual(t, instance.CPUTime, int64(0), "instance %s", instance.Name)
		t.Logf("instance %s: running=%t addresses=%v memory=%d processes=%d snapshots=%d",
			instance.Name, instance.Running, instance.Addresses, instance.Memory, instance.Processes, instance.Snapshots)
	}

	manager := NewSystemManager(client)
	for _, network := range state.Networks {
		detail, err := manager.NetworkDetail(ctx, "default", network.Name)
		require.NoError(t, err, "network %s", network.Name)
		require.Equal(t, network.Managed, detail.Managed, "network %s", network.Name)
		if !detail.Managed {
			require.False(t, detail.LeasesAvailable, "an unmanaged network reports no leases: %s", network.Name)
		}
		for _, entry := range detail.Config {
			require.True(t, allowedNetworkKey(entry.Key), "network %s leaked config key %q", network.Name, entry.Key)
		}
		t.Logf("network %s: type=%s managed=%t leases=%d(available=%t) config=%d addresses=%v",
			detail.Name, detail.Type, detail.Managed, len(detail.Leases), detail.LeasesAvailable, len(detail.Config), detail.Addresses)
	}

	for _, profile := range state.Profiles {
		detail, err := manager.ProfileDetail(ctx, "default", profile.Name)
		require.NoError(t, err, "profile %s", profile.Name)
		for _, entry := range detail.Config {
			require.True(t, allowedConfigKey(entry.Key), "profile %s leaked config key %q", profile.Name, entry.Key)
		}
		t.Logf("profile %s: devices=%d config=%d usedBy=%d", detail.Name, len(detail.Devices), len(detail.Config), len(detail.UsedBy))
	}
}

// TestLiveImageRemoteResolvesAlias exercises the one outbound network path
// creation depends on, without creating anything: connect to the fixed
// image remote, resolve a well-known alias for both instance types, and
// confirm a nonexistent alias fails rather than silently succeeding.
func TestLiveImageRemoteResolvesAlias(t *testing.T) {
	if os.Getenv("PILOTHOUSE_LIVE_INCUS") != "1" {
		t.Skip("set PILOTHOUSE_LIVE_INCUS=1 to contact the public image server")
	}
	images, err := incusclient.ConnectSimpleStreams(imageRemote, &incusclient.ConnectionArgs{
		HTTPClient: &http.Client{Timeout: imageRemoteTimeout},
	})
	require.NoError(t, err)

	for _, instanceType := range []string{TypeContainer, TypeVirtualMachine} {
		entry, _, err := images.GetImageAliasType(instanceType, "debian/13")
		require.NoError(t, err, "resolving debian/13 for %s", instanceType)
		require.NotEmpty(t, entry.Target)

		image, _, err := images.GetImage(entry.Target)
		require.NoError(t, err)
		require.Equal(t, instanceType, image.Type, "the resolved image must match the requested type")
		t.Logf("%s debian/13 -> %s (%s, %d bytes)", instanceType, entry.Target[:12], image.Properties["description"], image.Size)
	}

	_, _, err = images.GetImageAliasType(TypeContainer, "debian/does-not-exist")
	require.Error(t, err, "a nonexistent alias must fail rather than resolve")
}

// TestLiveCreateInstance is the only live test that mutates the host, so it
// is gated behind its own variable rather than PILOTHOUSE_LIVE_INCUS: it
// downloads an image and creates a real instance. It cleans up after
// itself, and refuses to run if its fixed name is already taken so it can
// never delete an instance it did not create.
func TestLiveCreateInstance(t *testing.T) {
	if os.Getenv("PILOTHOUSE_LIVE_INCUS_CREATE") != "1" {
		t.Skip("set PILOTHOUSE_LIVE_INCUS_CREATE=1 to create and delete a real instance")
	}
	const name = "pilothouse-live-check"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	manager := NewSystemManager(NewLocalClient())
	state, err := manager.State(ctx, "default")
	require.NoError(t, err)
	for _, instance := range state.Instances {
		require.NotEqual(t, name, instance.Name, "refusing to run: %s already exists", name)
	}

	require.NoError(t, manager.CreateInstance(ctx, "default", name, "debian/13", TypeContainer, ""))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := manager.Remove(cleanupCtx, "default", name); err != nil {
			t.Logf("cleanup failed, remove %s by hand: %v", name, err)
		}
	})

	detail, err := manager.Detail(ctx, "default", name)
	require.NoError(t, err)
	require.Equal(t, name, detail.Instance.Name)
	require.Equal(t, "Container", detail.Instance.Type)
	require.False(t, detail.Instance.Running, "a created instance is not started")
	t.Logf("created %s: architecture=%s profiles=%v config=%d devices=%d",
		detail.Instance.Name, detail.Architecture, detail.Profiles, len(detail.Config), len(detail.Devices))
}
