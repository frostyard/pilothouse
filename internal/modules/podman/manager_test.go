package podman

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const runningID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const stoppedID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type fakeClient struct {
	actions    []string
	containers []apiContainer
	images     []apiImage
	pods       []apiPod
	version    string
}

func (client *fakeClient) Containers(context.Context) ([]apiContainer, error) {
	return client.containers, nil
}

func (client *fakeClient) Images(context.Context) ([]apiImage, error) {
	return client.images, nil
}

func (client *fakeClient) Pods(context.Context) ([]apiPod, error) {
	return client.pods, nil
}

func (client *fakeClient) Remove(_ context.Context, id string) error {
	client.actions = append(client.actions, "remove "+id)
	return nil
}

func (client *fakeClient) Restart(_ context.Context, id string, timeout int) error {
	client.actions = append(client.actions, fmt.Sprintf("restart %d %s", timeout, id))
	return nil
}

func (client *fakeClient) Start(_ context.Context, id string) error {
	client.actions = append(client.actions, "start "+id)
	return nil
}

func (client *fakeClient) Stop(_ context.Context, id string, timeout int) error {
	client.actions = append(client.actions, fmt.Sprintf("stop %d %s", timeout, id))
	return nil
}

func (client *fakeClient) Version(context.Context) (string, error) {
	return client.version, nil
}

func TestSystemManagerBuildsCanonicalState(t *testing.T) {
	client := stateClient()
	state, err := NewSystemManager(client).State(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Containers, 2)
	assert.Equal(t, "api", state.Containers[0].Name)
	assert.True(t, state.Containers[0].Running)
	assert.Equal(t, "web", state.Containers[0].Pod)
	assert.Equal(t, "worker", state.Containers[1].Name)
	assert.False(t, state.Containers[1].Running)
	assert.Equal(t, "6.0.1", state.Version)
	require.Len(t, state.Pods, 1)
	assert.Equal(t, 2, state.Pods[0].Containers)
	require.Len(t, state.Images, 1)
	assert.Equal(t, "quay.io/example/api:latest", state.Images[0].Name)
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

func TestAPIClientUsesFixedLibpodEndpoints(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case apiPrefix + "/version":
			_, _ = w.Write([]byte(`{"Version":"6.0.1"}`))
		case apiPrefix + "/containers/json":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	client := &APIClient{http: server.Client()}
	client.http.Transport = rewriteTransport{base: server.URL, next: client.http.Transport}

	version, err := client.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "6.0.1", version)
	_, err = client.Containers(context.Background())
	require.NoError(t, err)
	require.NoError(t, client.Restart(context.Background(), runningID, 10))
	require.NoError(t, client.Stop(context.Background(), runningID, 10))

	assert.Equal(t, []string{
		"GET " + apiPrefix + "/version",
		"GET " + apiPrefix + "/containers/json?all=true",
		"POST " + apiPrefix + "/containers/" + runningID + "/restart?t=10",
		"POST " + apiPrefix + "/containers/" + runningID + "/stop?t=10",
	}, requests)
}

func TestAPIClientDecodesLibpodState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPrefix + "/version":
			_, _ = w.Write([]byte(`{"Version":"5.4.2"}`))
		case apiPrefix + "/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"` + runningID + `","Image":"quay.io/example/api:latest","Names":["api"],"PodName":"web","State":"running","Status":"Up 3 minutes"}]`))
		case apiPrefix + "/pods/json":
			_, _ = w.Write([]byte(`[{"Id":"cccccccccccc","Name":"web","Status":"Running","Containers":[{"Id":"infra"},{"Id":"` + runningID + `"}]}]`))
		case apiPrefix + "/images/json":
			_, _ = w.Write([]byte(`[{"Id":"dddddddddddd","RepoTags":["quay.io/example/api:latest"],"Size":6243868121,"Containers":1}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &APIClient{http: server.Client()}
	client.http.Transport = rewriteTransport{base: server.URL, next: client.http.Transport}

	state, err := NewSystemManager(client).State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "5.4.2", state.Version)
	require.Len(t, state.Containers, 1)
	assert.Equal(t, "web", state.Containers[0].Pod)
	require.Len(t, state.Pods, 1)
	assert.Equal(t, 2, state.Pods[0].Containers)
	require.Len(t, state.Images, 1)
	assert.Equal(t, "quay.io/example/api:latest", state.Images[0].Name)
	assert.Equal(t, uint64(6243868121), state.Images[0].Size)
}

func TestImagesFallBackToVirtualSize(t *testing.T) {
	values := images([]apiImage{{ID: "dddddddddddd", VirtualSize: 1048576}})
	require.Len(t, values, 1)
	assert.Equal(t, uint64(1048576), values[0].Size)
}

func TestAPIClientReturnsPodmanError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"cause":"running","message":"container is running","response":409}`))
	}))
	defer server.Close()
	client := &APIClient{http: server.Client()}
	client.http.Transport = rewriteTransport{base: server.URL, next: client.http.Transport}

	err := client.Remove(context.Background(), stoppedID)
	assert.EqualError(t, err, `podman API 409 Conflict: {"cause":"running","message":"container is running","response":409}`)
}

type rewriteTransport struct {
	base string
	next http.RoundTripper
}

func (transport rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	base, err := copy.URL.Parse(transport.base)
	if err != nil {
		return nil, err
	}
	copy.URL.Scheme = base.Scheme
	copy.URL.Host = base.Host
	return transport.next.RoundTrip(copy)
}

func stateClient() *fakeClient {
	pod := apiPod{ID: "cccccccccccc", Name: "web", Status: "Running"}
	pod.Containers = append(pod.Containers, struct {
		ID string `json:"Id"`
	}{ID: runningID}, struct {
		ID string `json:"Id"`
	}{ID: stoppedID})
	return &fakeClient{
		version: "6.0.1",
		containers: []apiContainer{
			{ID: runningID, Image: "quay.io/example/api:latest", Names: []string{"api"}, PodName: "web", State: "running", Status: "Up 3 minutes"},
			{ID: stoppedID, Image: "quay.io/example/worker:latest", Names: []string{"worker"}, State: "exited", Status: "Exited (0)"},
		},
		pods:   []apiPod{pod},
		images: []apiImage{{ID: "dddddddddddd", Names: []string{"quay.io/example/api:latest"}, Size: 1048576, Containers: 1}},
	}
}
