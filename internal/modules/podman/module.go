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

var actionIDs = map[string]string{
	"remove":  broker.ActionPodmanRemove,
	"restart": broker.ActionPodmanRestart,
	"start":   broker.ActionPodmanStart,
	"stop":    broker.ActionPodmanStop,
}

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
	mux.HandleFunc("GET /podman/containers/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
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
		_ = host.Render(w, r, platform.Page{Active: "podman", Body: LogsView(logs, unavailable), Eyebrow: "container diagnostics", Title: logs.Name + " logs"})
	})
	mux.HandleFunc("POST /podman/containers/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		id := r.PathValue("id")
		actionID, ok := actionIDs[r.PathValue("action")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, actionID, map[string]string{"id": id})
		m.redirect(w, r, fmt.Sprintf("Container %sd", r.PathValue("action")), err)
	})
	mux.HandleFunc("POST /podman/images/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		if r.PathValue("action") != "remove" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, broker.ActionPodmanRemoveImage, map[string]string{"id": r.PathValue("id")})
		m.redirect(w, r, "Image removed", err)
	})
}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	if err := host.Query(ctx, broker.QueryPodmanState, nil, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func queryLogs(ctx context.Context, host platform.Host, id string) (Logs, error) {
	var logs Logs
	if err := host.Query(ctx, broker.QueryPodmanLogs, map[string]string{"id": id}, &logs); err != nil {
		return Logs{}, err
	}
	return logs, nil
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
