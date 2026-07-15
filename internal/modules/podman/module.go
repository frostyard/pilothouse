package podman

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module {
	return &Module{}
}

func (m *Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: Summary(state), Order: 32, Span: platform.SpanHalf}}, nil
}

func (m *Module) Manifest() platform.Manifest {
	return platform.Manifest{
		Description: "Inspect and manage system containers, pods, and images",
		Icon:        "containers",
		ID:          "podman",
		Name:        "Containers",
		Order:       30,
		Path:        "/podman",
	}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /podman", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active: "podman", Body: Page(state, host.CSRFToken(r), host.Identity(r).Admin),
			Eyebrow: "OCI workloads", Title: "Podman",
		})
	})
	mux.HandleFunc("POST /podman/containers/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		id := r.PathValue("id")
		action := r.PathValue("action")
		var actionID string
		switch action {
		case "remove":
			actionID = broker.ActionPodmanRemove
		case "restart":
			actionID = broker.ActionPodmanRestart
		case "start":
			actionID = broker.ActionPodmanStart
		case "stop":
			actionID = broker.ActionPodmanStop
		default:
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, actionID, map[string]string{"id": id})
		m.redirect(w, r, fmt.Sprintf("Container %sd", action), err)
	})
}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	if err := host.Query(ctx, broker.QueryPodmanState, nil, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (m *Module) redirect(w http.ResponseWriter, r *http.Request, success string, err error) {
	values := url.Values{}
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", err.Error())
	} else {
		values.Set("notice", success)
	}
	destination := "/podman?" + values.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
