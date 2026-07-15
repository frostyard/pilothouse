package docker

import (
	"context"
	"fmt"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
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

func (fake *fakeClient) ContainerList(_ context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
	if !options.All {
		return client.ContainerListResult{}, fmt.Errorf("expected all containers")
	}
	summaries := make([]containertypes.Summary, 0, len(fake.containers))
	for _, item := range fake.containers {
		summaries = append(summaries, containertypes.Summary{ID: item.ID})
	}
	return client.ContainerListResult{Items: summaries}, nil
}

func (fake *fakeClient) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if fake.inspectErr != nil {
		return client.ContainerInspectResult{}, fake.inspectErr
	}
	for _, item := range fake.containers {
		if item.ID == id {
			return client.ContainerInspectResult{Container: item}, nil
		}
	}
	return client.ContainerInspectResult{}, fmt.Errorf("unexpected container %s", id)
}

func (fake *fakeClient) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	fake.actions = append(fake.actions, "remove "+id)
	return client.ContainerRemoveResult{}, nil
}

func (fake *fakeClient) ContainerRestart(_ context.Context, id string, options client.ContainerRestartOptions) (client.ContainerRestartResult, error) {
	fake.actions = append(fake.actions, fmt.Sprintf("restart %d %s", *options.Timeout, id))
	return client.ContainerRestartResult{}, nil
}

func (fake *fakeClient) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	fake.actions = append(fake.actions, "start "+id)
	return client.ContainerStartResult{}, nil
}

func (fake *fakeClient) ContainerStop(_ context.Context, id string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
	fake.actions = append(fake.actions, fmt.Sprintf("stop %d %s", *options.Timeout, id))
	return client.ContainerStopResult{}, nil
}

func (fake *fakeClient) ImageList(_ context.Context, options client.ImageListOptions) (client.ImageListResult, error) {
	if options.All {
		return client.ImageListResult{}, fmt.Errorf("did not expect intermediate images")
	}
	return client.ImageListResult{Items: fake.images}, nil
}

func (fake *fakeClient) ImageRemove(_ context.Context, id string, _ client.ImageRemoveOptions) (client.ImageRemoveResult, error) {
	fake.actions = append(fake.actions, "remove image "+id)
	return client.ImageRemoveResult{}, nil
}

func (fake *fakeClient) ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error) {
	return client.ServerVersionResult{Version: fake.version}, nil
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

func TestRemoveImageValidatesUsage(t *testing.T) {
	used := stateClient()
	err := NewSystemManager(used).RemoveImage(context.Background(), imageID)
	assert.EqualError(t, err, "remove containers using this image before deleting it")
	assert.NotContains(t, used.actions, "remove image "+imageID)

	unused := &fakeClient{images: []imagetypes.Summary{{ID: imageID}}}
	require.NoError(t, NewSystemManager(unused).RemoveImage(context.Background(), imageID))
	assert.Contains(t, unused.actions, "remove image "+imageID)
	assert.EqualError(t, NewSystemManager(unused).RemoveImage(context.Background(), ""), "invalid image identifier")
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
		containers: []containertypes.InspectResponse{{ID: runningID}},
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
		ID: id, Image: imageID, Name: name,
		State:  &containertypes.State{Running: running, Status: status, ExitCode: exitCode},
		Config: &containertypes.Config{Image: "registry.example/api:latest"},
	}
}
