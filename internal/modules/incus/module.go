package incus

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
	return []platform.DashboardCard{{Component: Summary(state), Order: 34, Span: platform.SpanHalf}}, nil
}

func (m *Module) Manifest() platform.Manifest {
	return platform.Manifest{
		Description: "Inspect and manage local Incus instances and images",
		Icon:        "incus",
		ID:          "incus",
		Name:        "Incus",
		Order:       50,
		Path:        "/incus",
	}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /incus", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active: "incus", Body: Page(state, host.CSRFToken(r), host.Identity(r).Admin),
			Eyebrow: "Local system instances", Title: "Incus",
		})
	})
	mux.HandleFunc("POST /incus/instances/{name}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		name := r.PathValue("name")
		var actionID string
		switch r.PathValue("action") {
		case "remove":
			actionID = broker.ActionIncusRemove
		case "restart":
			actionID = broker.ActionIncusRestart
		case "start":
			actionID = broker.ActionIncusStart
		case "stop":
			actionID = broker.ActionIncusStop
		default:
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, actionID, map[string]string{"name": name})
		m.redirect(w, r, fmt.Sprintf("Instance %sd", r.PathValue("action")), err)
	})
}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	if err := host.Query(ctx, broker.QueryIncusState, nil, &state); err != nil {
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
	destination := "/incus?" + values.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
