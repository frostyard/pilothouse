package podman

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const runningID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const stoppedID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type fakeRunner struct {
	calls     []string
	responses map[string][]byte
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	runner.calls = append(runner.calls, key)
	output, ok := runner.responses[key]
	if !ok {
		return nil, fmt.Errorf("unexpected command %s", key)
	}
	return output, nil
}

func TestSystemManagerBuildsCanonicalState(t *testing.T) {
	runner := stateRunner()
	state, err := NewSystemManager(runner, "podman").State(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Containers, 2)
	assert.Equal(t, "api", state.Containers[0].Name)
	assert.True(t, state.Containers[0].Running)
	assert.Equal(t, "worker", state.Containers[1].Name)
	assert.False(t, state.Containers[1].Running)
	assert.Equal(t, "5.4.2", state.Version)
	require.Len(t, state.Pods, 1)
	assert.Equal(t, 2, state.Pods[0].Containers)
	require.Len(t, state.Images, 1)
	assert.Equal(t, "quay.io/example/api:latest", state.Images[0].Name)
}

func TestContainerActionsValidateStateAndIdentifier(t *testing.T) {
	runner := stateRunner()
	manager := NewSystemManager(runner, "podman")

	require.NoError(t, manager.Stop(context.Background(), runningID))
	assert.Equal(t, "podman stop --time 10 "+runningID, runner.calls[len(runner.calls)-1])
	require.NoError(t, manager.Start(context.Background(), stoppedID))
	assert.Equal(t, "podman start "+stoppedID, runner.calls[len(runner.calls)-1])

	err := manager.Remove(context.Background(), runningID)
	assert.EqualError(t, err, "stop the container before removing it")
	err = manager.Start(context.Background(), "--all")
	assert.EqualError(t, err, "invalid container identifier")
}

func stateRunner() *fakeRunner {
	containers := fmt.Sprintf(`[
		{"Id":%q,"Image":"quay.io/example/api:latest","Names":["api"],"Pod":"web","State":"running","Status":"Up 3 minutes"},
		{"Id":%q,"Image":"quay.io/example/worker:latest","Names":"worker","State":"exited","Status":"Exited (0)"}
	]`, runningID, stoppedID)
	return &fakeRunner{responses: map[string][]byte{
		"podman version --format json":       []byte(`{"Client":{"Version":"5.4.2"}}`),
		"podman ps --all --format json":      []byte(containers),
		"podman pod ps --format json":        []byte(`[{"Id":"cccccccccccc","Name":"web","NumContainers":2,"Status":"Running"}]`),
		"podman images --format json":        []byte(`[{"Id":"dddddddddddd","Names":["quay.io/example/api:latest"],"Size":1048576,"Containers":1}]`),
		"podman stop --time 10 " + runningID: nil,
		"podman start " + stoppedID:          nil,
	}}
}
