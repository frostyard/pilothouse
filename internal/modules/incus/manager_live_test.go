package incus

import (
	"context"
	"os"
	"testing"
	"time"

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
