package docker

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
	"remove":  broker.ActionDockerRemove,
	"restart": broker.ActionDockerRestart,
	"start":   broker.ActionDockerStart,
	"stop":    broker.ActionDockerStop,
}

func New() *Module {
	return &Module{}
}

func (m *Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: Summary(state), Order: 33, Span: platform.SpanHalf}}, nil
}

func (m *Module) Manifest() platform.Manifest {
	return platform.Manifest{
		Description: "Inspect and manage system Docker containers and images",
		Icon:        "docker",
		ID:          "docker",
		Name:        "Docker",
		Order:       40,
		Path:        "/docker",
	}
}

// RequiredCapabilities makes the whole module — its nav entry, dashboard
// card, and every route mounted below — available only on a host that
// advertises docker.
func (*Module) RequiredCapabilities() []capability.ID {
	return []capability.ID{capability.Docker}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /docker", platform.Gate(host, []capability.ID{capability.Docker}, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active: "docker", Body: Page(state, host.CSRFToken(r), host.Identity(r).Admin),
			Eyebrow: "Moby workloads", Title: "Docker",
		})
	}))
	mux.HandleFunc("GET /docker/containers/{id}/logs", platform.Gate(host, []capability.ID{capability.Docker}, func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validContainerID(id) {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		logs, err := queryLogs(ctx, host, id)
		unavailable := err != nil
		if unavailable {
			logs = Logs{ID: id, Name: id}
		}
		_ = host.Render(w, r, platform.Page{Active: "docker", Body: LogsView(logs, unavailable), Eyebrow: "container diagnostics", Title: logs.Name + " logs"})
	}))
	mux.HandleFunc("POST /docker/containers/{id}/{action}", platform.Gate(host, []capability.ID{capability.Docker}, func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		id := r.PathValue("id")
		action := r.PathValue("action")
		actionID, ok := actionIDs[action]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if (action == "stop" || action == "remove") && !host.ConfirmAction(w, r, strings.ToUpper(action[:1])+action[1:]+" container", "docker/container/"+id) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, actionID, map[string]string{"id": id})
		m.redirect(w, r, fmt.Sprintf("Container %sd", r.PathValue("action")), err)
	}))
	mux.HandleFunc("POST /docker/images/{id}/{action}", platform.Gate(host, []capability.ID{capability.Docker}, func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		if r.PathValue("action") != "remove" {
			http.NotFound(w, r)
			return
		}
		id := r.PathValue("id")
		if !host.ConfirmAction(w, r, "Remove Docker image", "docker/image/"+id) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, broker.ActionDockerRemoveImage, map[string]string{"id": id})
		m.redirect(w, r, "Image removed", err)
	}))
}

func queryLogs(ctx context.Context, host platform.Host, id string) (Logs, error) {
	var logs Logs
	err := host.Query(ctx, broker.QueryDockerLogs, map[string]string{"id": id}, &logs)
	return logs, err
}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	if err := host.Query(ctx, broker.QueryDockerState, nil, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (m *Module) redirect(w http.ResponseWriter, r *http.Request, success string, err error) {
	values := url.Values{}
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", "Action failed. Review Activity for the recorded outcome.")
	} else {
		values.Set("notice", success)
	}
	destination := "/docker?" + values.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
