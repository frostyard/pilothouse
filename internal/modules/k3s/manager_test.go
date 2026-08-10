package k3s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/k3sconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nodesFixture = `{
  "apiVersion": "v1",
  "kind": "NodeList",
  "items": [
    {
      "metadata": {
        "name": "worker-b",
        "labels": {}
      },
      "status": {
        "conditions": [{"type": "Ready", "status": "False"}],
        "nodeInfo": {
          "kubeletVersion": "v1.34.1+k3s1",
          "containerRuntimeVersion": "containerd://2.1.4-k3s1"
        }
      }
    },
    {
      "metadata": {
        "name": "server-a",
        "labels": {
          "node-role.kubernetes.io/control-plane": "true",
          "node-role.kubernetes.io/etcd": "true"
        }
      },
      "status": {
        "conditions": [{"type": "Ready", "status": "True"}],
        "nodeInfo": {
          "kubeletVersion": "v1.34.1+k3s1",
          "containerRuntimeVersion": "containerd://2.1.4-k3s1"
        }
      }
    }
  ]
}`

const podsFixture = `{
  "apiVersion": "v1",
  "kind": "PodList",
  "items": [
    {"metadata":{"name":"api","namespace":"default"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},
    {"metadata":{"name":"worker","namespace":"default"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}]}},
    {"metadata":{"name":"job","namespace":"default"},"status":{"phase":"Succeeded"}},
    {"metadata":{"name":"dns","namespace":"kube-system"},"status":{"phase":"Pending"}},
    {"metadata":{"name":"metrics","namespace":"kube-system"},"status":{"phase":"Failed"}},
    {"metadata":{"name":"tunnel","namespace":"kube-system"},"status":{"phase":"Unknown"}}
  ]
}`

type runCall struct {
	ctx  context.Context
	name string
	args []string
}

type fakeRunner struct {
	calls     []runCall
	errors    []error
	responses [][]byte
}

func (runner *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	index := len(runner.calls)
	runner.calls = append(runner.calls, runCall{ctx: ctx, name: name, args: append([]string(nil), args...)})
	var response []byte
	if index < len(runner.responses) {
		response = runner.responses[index]
	}
	var err error
	if index < len(runner.errors) {
		err = runner.errors[index]
	}
	return response, err
}

func TestSystemManagerBuildsAggregateState(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{[]byte(nodesFixture), []byte(podsFixture)}}
	state, err := NewSystemManager(runner, "/usr/local/bin/k3s").State(context.Background())
	require.NoError(t, err)

	require.Len(t, state.Nodes, 2)
	assert.Equal(t, "server-a", state.Nodes[0].Name)
	assert.True(t, state.Nodes[0].Ready)
	assert.Equal(t, []string{"control-plane", "etcd"}, state.Nodes[0].Roles)
	assert.Equal(t, "worker-b", state.Nodes[1].Name)
	assert.False(t, state.Nodes[1].Ready)
	assert.Equal(t, []string{"worker"}, state.Nodes[1].Roles)

	require.Equal(t, []Namespace{
		{Name: "default", Ready: 1, NotReady: 1, Succeeded: 1, Total: 3},
		{Name: "kube-system", NotReady: 1, Pending: 1, Failed: 1, Total: 3},
	}, state.Namespaces)
}

func TestSystemManagerUsesFixedReadOnlyCommands(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{[]byte(nodesFixture), []byte(podsFixture)}}
	_, err := NewSystemManager(runner, "/opt/k3s").State(context.Background())
	require.NoError(t, err)
	require.Len(t, runner.calls, 2)

	assert.Equal(t, "/opt/k3s", runner.calls[0].name)
	assert.Equal(t, []string{"kubectl", "--kubeconfig=" + k3sconfig.KubeconfigPath, "get", "nodes", "-o", "json"}, runner.calls[0].args)
	assert.Equal(t, "/opt/k3s", runner.calls[1].name)
	assert.Equal(t, []string{"kubectl", "--kubeconfig=" + k3sconfig.KubeconfigPath, "get", "pods", "--all-namespaces", "-o", "json"}, runner.calls[1].args)
}

func TestSystemManagerBoundsBothCommands(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{[]byte(nodesFixture), []byte(podsFixture)}}
	start := time.Now()
	_, err := NewSystemManager(runner, "/usr/local/bin/k3s").State(context.Background())
	require.NoError(t, err)

	for _, call := range runner.calls {
		deadline, ok := call.ctx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, deadline.Sub(start), stateTimeout+500*time.Millisecond)
		assert.Greater(t, time.Until(deadline), time.Duration(0))
	}
}

func TestSystemManagerSurfacesCommandFailure(t *testing.T) {
	runner := &fakeRunner{
		responses: [][]byte{[]byte("Unable to connect to the server")},
		errors:    []error{errors.New("exit status 1")},
	}
	_, err := NewSystemManager(runner, "/usr/local/bin/k3s").State(context.Background())
	assert.EqualError(t, err, "k3s kubectl: exit status 1: Unable to connect to the server")
}

func TestSystemManagerRejectsMalformedClusterDocuments(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		nodes     string
		pods      string
		wantError string
	}{
		{name: "wrong node api", nodes: `{"apiVersion":"v2","kind":"NodeList","items":[]}`, wantError: "unexpected k3s node response"},
		{name: "wrong node kind", nodes: `{"apiVersion":"v1","kind":"PodList","items":[]}`, wantError: "unexpected k3s node response"},
		{name: "unnamed node", nodes: `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{}}]}`, wantError: "k3s node response contains an unnamed node"},
		{name: "node missing version", nodes: `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"server"},"status":{"nodeInfo":{"containerRuntimeVersion":"containerd://2.1.4-k3s1"}}}]}`, wantError: `k3s node "server" response omits version or runtime`},
		{name: "node missing runtime", nodes: `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"server"},"status":{"nodeInfo":{"kubeletVersion":"v1.34.1+k3s1"}}}]}`, wantError: `k3s node "server" response omits version or runtime`},
		{name: "wrong pod api", nodes: nodesFixture, pods: `{"apiVersion":"v2","kind":"PodList","items":[]}`, wantError: "unexpected k3s pod response"},
		{name: "wrong pod kind", nodes: nodesFixture, pods: `{"apiVersion":"v1","kind":"NodeList","items":[]}`, wantError: "unexpected k3s pod response"},
		{name: "unnamed pod", nodes: nodesFixture, pods: `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"namespace":"default"},"status":{"phase":"Running"}}]}`, wantError: "k3s pod response contains an unnamed pod or namespace"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			responses := [][]byte{[]byte(testCase.nodes)}
			if testCase.pods != "" {
				responses = append(responses, []byte(testCase.pods))
			}
			_, err := NewSystemManager(&fakeRunner{responses: responses}, "/usr/local/bin/k3s").State(context.Background())
			assert.EqualError(t, err, testCase.wantError)
		})
	}
}

func TestSystemManagerCountsUnrecognizedPodPhaseAsNotReady(t *testing.T) {
	pods := `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api","namespace":"default"},"status":{"phase":"FuturePhase"}}]}`
	runner := &fakeRunner{responses: [][]byte{[]byte(nodesFixture), []byte(pods)}}

	state, err := NewSystemManager(runner, "/usr/local/bin/k3s").State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []Namespace{{Name: "default", NotReady: 1, Total: 1}}, state.Namespaces)
	require.Len(t, state.Nodes, 2, "an unknown pod phase must not erase unrelated node inventory")
}

func TestSystemManagerRejectsInvalidJSON(t *testing.T) {
	_, err := NewSystemManager(&fakeRunner{responses: [][]byte{[]byte(`{"items":`)}}, "/usr/local/bin/k3s").State(context.Background())
	assert.ErrorContains(t, err, "decode k3s kubectl response")
}

func TestBoundedOutputCapsStoredBytes(t *testing.T) {
	output := &boundedOutput{limit: 5}
	count, err := output.Write([]byte("abcdefgh"))
	require.NoError(t, err)
	assert.Equal(t, 8, count)
	assert.Equal(t, "abcde", output.String())
	assert.True(t, output.truncated)
}

func TestExecRunnerDoesNotUseShell(t *testing.T) {
	_, err := (ExecRunner{}).Run(context.Background(), "echo hi; echo bye")
	assert.Error(t, err)
}

func TestExecRunnerReturnsOnlyStdoutOnSuccess(t *testing.T) {
	output, err := (ExecRunner{}).Run(context.Background(), "sh", "-c", `printf '{"kind":"NodeList"}'; printf 'warning' >&2`)
	require.NoError(t, err)
	assert.Equal(t, `{"kind":"NodeList"}`, string(output))
}

func TestExecRunnerReturnsBothStreamsOnFailure(t *testing.T) {
	output, err := (ExecRunner{}).Run(context.Background(), "sh", "-c", `printf 'partial response'; printf 'request failed' >&2; exit 1`)
	require.Error(t, err)
	assert.Equal(t, "partial response\nrequest failed", string(output))
}

func TestNodeRolesIgnoreUnrelatedLabels(t *testing.T) {
	assert.Equal(t, []string{"worker"}, nodeRoles(map[string]string{"kubernetes.io/hostname": "worker"}))
	assert.Equal(t, []string{"agent", "storage"}, nodeRoles(map[string]string{
		"node-role.kubernetes.io/storage": "",
		"node-role.kubernetes.io/agent":   "true",
	}))
	assert.False(t, strings.Contains(strings.Join(nodeRoles(nil), ","), "kubernetes.io"))
}
