package incus

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

var actionIDs = map[string]string{
	"remove":  broker.ActionIncusRemove,
	"restart": broker.ActionIncusRestart,
	"start":   broker.ActionIncusStart,
	"stop":    broker.ActionIncusStop,
}

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

// RequiredCapabilities makes the whole module — its nav entry, dashboard
// card, and every route mounted below — available only on a host that
// advertises incus.
func (*Module) RequiredCapabilities() []capability.ID {
	return []capability.ID{capability.Incus}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /incus", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		state, err := queryState(ctx, host, r.URL.Query().Get("project"))
		if err != nil {
			if r.URL.Query().Get("project") != "" && projectUnavailable(err) {
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
	}))
	mux.HandleFunc("POST /incus/instances/{name}/{action}", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		name := r.PathValue("name")
		action := r.PathValue("action")
		actionID, ok := actionIDs[action]
		if !ok {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		project := r.FormValue("project")
		if (action == "stop" || action == "remove") && !host.ConfirmAction(w, r, strings.ToUpper(action[:1])+action[1:]+" Incus instance", "incus/instance/"+project+"/"+name) {
			return
		}
		err := host.Execute(ctx, r, actionID, map[string]string{"name": name, "project": project})
		m.redirect(w, r, project, fmt.Sprintf("Instance %sd", r.PathValue("action")), err)
	}))
	mux.HandleFunc("POST /incus/images/{fingerprint}/{action}", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
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
		fingerprint := r.PathValue("fingerprint")
		if !host.ConfirmAction(w, r, "Remove Incus image", "incus/image/"+project+"/"+fingerprint) {
			return
		}
		err := host.Execute(ctx, r, broker.ActionIncusRemoveImage, map[string]string{
			"fingerprint": fingerprint, "project": project,
		})
		m.redirect(w, r, project, "Image removed", err)
	}))
}

func queryState(ctx context.Context, host platform.Host, project string) (State, error) {
	var state State
	if err := host.Query(ctx, broker.QueryIncusState, map[string]string{"project": project}, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func projectUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "project is not available")
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
