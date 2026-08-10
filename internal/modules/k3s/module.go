package k3s

import (
	"context"
	"net/http"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module {
	return &Module{}
}

func (*Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: Summary(state), Order: 35, Span: platform.SpanHalf}}, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{
		Description: "Read-only k3s node and namespace health",
		Icon:        "network",
		ID:          "k3s",
		Name:        "Kubernetes",
		Order:       45,
		Path:        "/k3s",
	}
}

func (*Module) RequiredCapabilities() []capability.ID {
	return []capability.ID{capability.K3s}
}

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /k3s", platform.Gate(host, []capability.ID{capability.K3s}, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), stateTimeout)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active: "k3s", Body: Page(state), Eyebrow: "Kubernetes", Title: "k3s",
		})
	}))
}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	if err := host.Query(ctx, broker.QueryK3sState, nil, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func readyNodes(state State) int {
	ready := 0
	for _, node := range state.Nodes {
		if node.Ready {
			ready++
		}
	}
	return ready
}

func totalPods(state State) int {
	total := 0
	for _, namespace := range state.Namespaces {
		total += namespace.Total
	}
	return total
}

func readyPods(state State) int {
	ready := 0
	for _, namespace := range state.Namespaces {
		ready += namespace.Ready
	}
	return ready
}

func issuePods(state State) int {
	issues := 0
	for _, namespace := range state.Namespaces {
		issues += namespace.NotReady + namespace.Pending + namespace.Failed
	}
	return issues
}
