package incus

import (
	"context"
	"fmt"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const imageFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeClient struct {
	actions   []string
	images    []api.Image
	instances []api.Instance
	version   string
}

func (client *fakeClient) Images(context.Context) ([]api.Image, error) {
	return client.images, nil
}

func (client *fakeClient) Instances(context.Context) ([]api.Instance, error) {
	return client.instances, nil
}

func (client *fakeClient) Remove(_ context.Context, name string) error {
	client.actions = append(client.actions, "remove "+name)
	return nil
}

func (client *fakeClient) Restart(_ context.Context, name string, timeout int) error {
	client.actions = append(client.actions, fmt.Sprintf("restart %d %s", timeout, name))
	return nil
}

func (client *fakeClient) Server(context.Context) (*api.Server, error) {
	return &api.Server{Environment: api.ServerEnvironment{ServerVersion: client.version}}, nil
}

func (client *fakeClient) Start(_ context.Context, name string) error {
	client.actions = append(client.actions, "start "+name)
	return nil
}

func (client *fakeClient) Stop(_ context.Context, name string, timeout int) error {
	client.actions = append(client.actions, fmt.Sprintf("stop %d %s", timeout, name))
	return nil
}

func TestSystemManagerBuildsCanonicalState(t *testing.T) {
	client := stateClient()
	state, err := NewSystemManager(client).State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "6.11", state.Version)
	require.Len(t, state.Instances, 2)
	assert.Equal(t, "api", state.Instances[0].Name)
	assert.True(t, state.Instances[0].Running)
	assert.Equal(t, "Ubuntu 24.04", state.Instances[0].Image)
	assert.Equal(t, "Virtual machine", state.Instances[1].Type)
	require.Len(t, state.Images, 1)
	assert.Equal(t, "ubuntu/24.04", state.Images[0].Name)
	assert.Equal(t, 2, state.Images[0].Instances)
	assert.Equal(t, uint64(1048576), state.Images[0].Size)
}

func TestInstanceActionsValidateStateAndName(t *testing.T) {
	client := stateClient()
	manager := NewSystemManager(client)

	require.NoError(t, manager.Stop(context.Background(), "api"))
	assert.Equal(t, "stop 30 api", client.actions[len(client.actions)-1])
	require.NoError(t, manager.Start(context.Background(), "worker-vm"))
	assert.Equal(t, "start worker-vm", client.actions[len(client.actions)-1])
	require.NoError(t, manager.Restart(context.Background(), "api"))
	assert.Equal(t, "restart 30 api", client.actions[len(client.actions)-1])
	require.NoError(t, manager.Remove(context.Background(), "worker-vm"))
	assert.Equal(t, "remove worker-vm", client.actions[len(client.actions)-1])

	err := manager.Remove(context.Background(), "api")
	assert.EqualError(t, err, "stop the instance before removing it")
	err = manager.Start(context.Background(), "../default/api")
	assert.EqualError(t, err, "invalid instance name")
}

func TestEmptyServerUsesInstalledVersionFallback(t *testing.T) {
	state, err := NewSystemManager(&fakeClient{}).State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "installed", state.Version)
	assert.Empty(t, state.Instances)
	assert.Empty(t, state.Images)
}

func TestValidInstanceName(t *testing.T) {
	for _, name := range []string{"api", "worker-01", "a"} {
		assert.True(t, validInstanceName(name), name)
	}
	for _, name := range []string{"", "-api", "api-", "API", "api/default", "api.local", "../api"} {
		assert.False(t, validInstanceName(name), name)
	}
}

func stateClient() *fakeClient {
	containerConfig := api.ConfigMap{"volatile.base_image": imageFingerprint, "image.description": "Ubuntu 24.04"}
	return &fakeClient{
		version: "6.11",
		instances: []api.Instance{
			{Name: "worker-vm", Status: "Stopped", StatusCode: api.Stopped, Type: "virtual-machine", ExpandedConfig: containerConfig},
			{Name: "api", Status: "Running", StatusCode: api.Running, Type: "container", ExpandedConfig: containerConfig},
		},
		images: []api.Image{{Fingerprint: imageFingerprint, Size: 1048576, Type: "container", Aliases: []api.ImageAlias{{Name: "ubuntu/24.04"}}}},
	}
}
