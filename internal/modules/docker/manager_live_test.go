package docker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLiveSystemManagerState(t *testing.T) {
	if os.Getenv("PILOTHOUSE_LIVE_DOCKER") != "1" {
		t.Skip("set PILOTHOUSE_LIVE_DOCKER=1 to inspect the local system Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	state, err := NewSystemManager(ExecRunner{}, "docker").State(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, state.Version)
	t.Logf("Docker %s: %d containers, %d images", state.Version, len(state.Containers), len(state.Images))
}
