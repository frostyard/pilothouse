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
	state, err := NewSystemManager(client).State(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, state.Version)
	t.Logf("Incus %s: %d instances, %d images", state.Version, len(state.Instances), len(state.Images))
}
