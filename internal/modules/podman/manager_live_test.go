package podman

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLiveSystemManagerState(t *testing.T) {
	if os.Getenv("PILOTHOUSE_LIVE_PODMAN") != "1" {
		t.Skip("set PILOTHOUSE_LIVE_PODMAN=1 to inspect the local system Podman store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := NewAPIClient("/run/podman/podman.sock")
	defer client.Close()
	state, err := NewSystemManager(client).State(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, state.Version)
	t.Logf("Podman %s: %d containers, %d pods, %d images", state.Version, len(state.Containers), len(state.Pods), len(state.Images))
}
