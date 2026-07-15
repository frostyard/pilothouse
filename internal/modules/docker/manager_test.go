package docker

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
const imageID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

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

func TestSystemManagerBuildsCanonicalStateFromInspect(t *testing.T) {
	runner := stateRunner()
	state, err := NewSystemManager(runner, "docker").State(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Containers, 2)
	assert.Equal(t, "api", state.Containers[0].Name)
	assert.True(t, state.Containers[0].Running)
	assert.Equal(t, "worker", state.Containers[1].Name)
	assert.Equal(t, "exited (17)", state.Containers[1].Status)
	assert.Equal(t, "29.6.1", state.Version)
	require.Len(t, state.Images, 1)
	assert.Equal(t, "registry.example/api:latest", state.Images[0].Name)
	assert.Equal(t, 2, state.Images[0].Containers)
	assert.Equal(t, 1, strings.Count(strings.Join(runner.calls, "\n"), "docker image inspect "))
}

func TestContainerActionsValidateStateAndIdentifier(t *testing.T) {
	runner := stateRunner()
	manager := NewSystemManager(runner, "docker")

	require.NoError(t, manager.Stop(context.Background(), runningID))
	assert.Equal(t, "docker container stop --timeout 10 "+runningID, runner.calls[len(runner.calls)-1])
	require.NoError(t, manager.Start(context.Background(), stoppedID))
	assert.Equal(t, "docker container start "+stoppedID, runner.calls[len(runner.calls)-1])

	err := manager.Remove(context.Background(), runningID)
	assert.EqualError(t, err, "stop the container before removing it")
	err = manager.Start(context.Background(), "--all")
	assert.EqualError(t, err, "invalid container identifier")
}

func TestEmptyDaemonDoesNotIssueInspectCommands(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"docker version --format {{json .}}":           []byte(`{"Server":{"Version":"29.6.1"}}`),
		"docker container ls --all --quiet --no-trunc": nil,
		"docker image ls --quiet --no-trunc":           nil,
	}}
	state, err := NewSystemManager(runner, "docker").State(context.Background())
	require.NoError(t, err)
	assert.Empty(t, state.Containers)
	assert.Empty(t, state.Images)
	assert.Len(t, runner.calls, 3)
}

func stateRunner() *fakeRunner {
	containers := fmt.Sprintf(`[
		{"Id":%q,"Name":"/api","Image":%q,"Config":{"Image":"registry.example/api:latest"},"State":{"Running":true,"Status":"running","ExitCode":0}},
		{"Id":%q,"Name":"/worker","Image":%q,"Config":{"Image":"registry.example/api:latest"},"State":{"Running":false,"Status":"exited","ExitCode":17}}
	]`, runningID, imageID, stoppedID, imageID)
	return &fakeRunner{responses: map[string][]byte{
		"docker version --format {{json .}}":                      []byte(`{"Client":{"Version":"29.6.1"},"Server":{"Version":"29.6.1"}}`),
		"docker container ls --all --quiet --no-trunc":            []byte(runningID + "\n" + stoppedID + "\n"),
		"docker container inspect " + runningID + " " + stoppedID: []byte(containers),
		"docker image ls --quiet --no-trunc":                      []byte(imageID + "\n" + imageID + "\n"),
		"docker image inspect " + imageID:                         []byte(`[{"Id":"` + imageID + `","RepoTags":["registry.example/api:latest"],"Size":1048576}]`),
		"docker container stop --timeout 10 " + runningID:         nil,
		"docker container start " + stoppedID:                     nil,
	}}
}
