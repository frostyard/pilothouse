package k3s

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func viewFixture() State {
	return State{
		Nodes: []Node{
			{Name: "server-a", Ready: true, Roles: []string{"control-plane", "etcd"}, Version: "v1.34.1+k3s1", Runtime: "containerd://2.1.4-k3s1"},
			{Name: "worker-b", Roles: []string{"worker"}, Version: "v1.34.1+k3s1", Runtime: "containerd://2.1.4-k3s1"},
		},
		Namespaces: []Namespace{
			{Name: "default", Ready: 3, NotReady: 1, Total: 4},
			{Name: "kube-system", Ready: 4, Pending: 1, Failed: 1, Total: 6},
		},
	}
}

func TestSummaryRendersAggregateHealthAndIcon(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Summary(viewFixture()).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "1 of 2 nodes ready")
	assert.Contains(t, html, "7 ready of 10 total")
	assert.Contains(t, html, `href="/k3s"`)
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.Icon")
}

func TestPageRendersNodesAndNamespaceTotalsWithoutPodDetails(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(viewFixture()).Render(context.Background(), &output))
	html := output.String()

	for _, value := range []string{
		"server-a", "worker-b", "control-plane, etcd", "Not ready",
		"default", "kube-system", "Aggregate pod health", "Read only",
	} {
		assert.Contains(t, html, value)
	}
	assert.Contains(t, html, "not individual pod details or cluster mutations")
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersEmptyStates(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(State{}).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "No nodes were reported by k3s.")
	assert.Contains(t, output.String(), "No pods were reported by k3s.")
}
