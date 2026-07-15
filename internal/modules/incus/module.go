package incus

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module {
	return &Module{}
}

func (m *Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host, "default")
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
		state, err := queryState(ctx, host, r.URL.Query().Get("project"))
		if err != nil {
			if r.URL.Query().Get("project") != "" && strings.Contains(err.Error(), "project is not available") {
				values := url.Values{"kind": {"error"}, "notice": {"Selected project is no longer available"}}
				http.Redirect(w, r, "/incus?"+values.Encode(), http.StatusSeeOther)
				return
			}
			http.Error(w, "Failed to load Incus state. Please check that the Incus daemon is running.", http.StatusServiceUnavailable)
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
		project := r.FormValue("project")
		err := host.Execute(ctx, r, actionID, map[string]string{"name": name, "project": project})
		m.redirect(w, r, project, fmt.Sprintf("Instance %sd", r.PathValue("action")), err)
	})
	mux.HandleFunc("POST /incus/images/{fingerprint}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		if r.PathValue("action") != "remove" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		project := r.FormValue("project")
		err := host.Execute(ctx, r, broker.ActionIncusRemoveImage, map[string]string{
			"fingerprint": r.PathValue("fingerprint"), "project": project,
		})
		m.redirect(w, r, project, "Image removed", err)
	})
}

func queryState(ctx context.Context, host platform.Host, project string) (State, error) {
	var state State
	if err := host.Query(ctx, broker.QueryIncusState, map[string]string{"project": project}, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (m *Module) redirect(w http.ResponseWriter, r *http.Request, project, success string, err error) {
	values := url.Values{}
	values.Set("project", project)
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", "Action failed. Please try again.")
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
