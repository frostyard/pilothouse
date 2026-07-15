package docker

import (
	"context"
	"fmt"
	"testing"

	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const runningID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const stoppedID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const imageID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

type fakeClient struct {
	actions    []string
	containers []containertypes.InspectResponse
	inspectErr error
	images     []imagetypes.Summary
	version    string
}

func (client *fakeClient) ContainerList(_ context.Context, options containertypes.ListOptions) ([]containertypes.Summary, error) {
	if !options.All {
		return nil, fmt.Errorf("expected all containers")
	}
	summaries := make([]containertypes.Summary, 0, len(client.containers))
	for _, item := range client.containers {
		summaries = append(summaries, containertypes.Summary{ID: item.ID})
	}
	return summaries, nil
}

func (client *fakeClient) ContainerInspect(_ context.Context, id string) (containertypes.InspectResponse, error) {
	if client.inspectErr != nil {
		return containertypes.InspectResponse{}, client.inspectErr
	}
	for _, item := range client.containers {
		if item.ID == id {
			return item, nil
		}
	}
	return containertypes.InspectResponse{}, fmt.Errorf("unexpected container %s", id)
}

func (client *fakeClient) ContainerRemove(_ context.Context, id string, _ containertypes.RemoveOptions) error {
	client.actions = append(client.actions, "remove "+id)
	return nil
}

func (client *fakeClient) ContainerRestart(_ context.Context, id string, options containertypes.StopOptions) error {
	client.actions = append(client.actions, fmt.Sprintf("restart %d %s", *options.Timeout, id))
	return nil
}

func (client *fakeClient) ContainerStart(_ context.Context, id string, _ containertypes.StartOptions) error {
	client.actions = append(client.actions, "start "+id)
	return nil
}

func (client *fakeClient) ContainerStop(_ context.Context, id string, options containertypes.StopOptions) error {
	client.actions = append(client.actions, fmt.Sprintf("stop %d %s", *options.Timeout, id))
	return nil
}

func (client *fakeClient) ImageList(_ context.Context, options imagetypes.ListOptions) ([]imagetypes.Summary, error) {
	if options.All {
		return nil, fmt.Errorf("did not expect intermediate images")
	}
	return client.images, nil
}

func (client *fakeClient) ServerVersion(context.Context) (types.Version, error) {
	return types.Version{Version: client.version}, nil
}

func TestSystemManagerBuildsCanonicalStateFromAPI(t *testing.T) {
	client := stateClient()
	state, err := NewSystemManager(client).State(context.Background())
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
}

func TestContainerActionsValidateStateAndIdentifier(t *testing.T) {
	client := stateClient()
	manager := NewSystemManager(client)

	require.NoError(t, manager.Stop(context.Background(), runningID))
	assert.Equal(t, "stop 10 "+runningID, client.actions[len(client.actions)-1])
	require.NoError(t, manager.Start(context.Background(), stoppedID))
	assert.Equal(t, "start "+stoppedID, client.actions[len(client.actions)-1])
	require.NoError(t, manager.Restart(context.Background(), runningID))
	assert.Equal(t, "restart 10 "+runningID, client.actions[len(client.actions)-1])
	require.NoError(t, manager.Remove(context.Background(), stoppedID))
	assert.Equal(t, "remove "+stoppedID, client.actions[len(client.actions)-1])

	err := manager.Remove(context.Background(), runningID)
	assert.EqualError(t, err, "stop the container before removing it")
	err = manager.Start(context.Background(), "--all")
	assert.EqualError(t, err, "invalid container identifier")
}

func TestEmptyDaemonDoesNotInspectContainers(t *testing.T) {
	state, err := NewSystemManager(&fakeClient{version: "29.6.1"}).State(context.Background())
	require.NoError(t, err)
	assert.Empty(t, state.Containers)
	assert.Empty(t, state.Images)
}

func TestStateRejectsIncompleteContainerInspectResponse(t *testing.T) {
	client := &fakeClient{
		version:    "29.6.1",
		containers: []containertypes.InspectResponse{{ContainerJSONBase: &containertypes.ContainerJSONBase{ID: runningID}}},
	}

	_, err := NewSystemManager(client).State(context.Background())
	assert.EqualError(t, err, "docker returned incomplete inspect data for container "+runningID)
}

func TestStatePropagatesContainerInspectError(t *testing.T) {
	client := stateClient()
	client.inspectErr = fmt.Errorf("container disappeared")

	_, err := NewSystemManager(client).State(context.Background())
	assert.EqualError(t, err, "container disappeared")
}

func stateClient() *fakeClient {
	return &fakeClient{
		version: "29.6.1",
		containers: []containertypes.InspectResponse{
			inspectResponse(runningID, "/api", true, containertypes.StateRunning, 0),
			inspectResponse(stoppedID, "/worker", false, containertypes.StateExited, 17),
		},
		images: []imagetypes.Summary{{ID: imageID, RepoTags: []string{"registry.example/api:latest"}, Size: 1048576}},
	}
}

func inspectResponse(id, name string, running bool, status containertypes.ContainerState, exitCode int) containertypes.InspectResponse {
	return containertypes.InspectResponse{
		ContainerJSONBase: &containertypes.ContainerJSONBase{
			ID: id, Image: imageID, Name: name,
			State: &containertypes.State{Running: running, Status: status, ExitCode: exitCode},
		},
		Config: &containertypes.Config{Image: "registry.example/api:latest"},
	}
}
