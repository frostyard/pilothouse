package incus

import (
	"context"
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
	"remove":     broker.ActionIncusRemove,
	"restart":    broker.ActionIncusRestart,
	"start":      broker.ActionIncusStart,
	"stop":       broker.ActionIncusStop,
	"stop-force": broker.ActionIncusStopForce,
}

// snapshotActionIDs covers only the two actions that address an existing
// snapshot. Creation names a new one and takes its name from the form, so
// it has its own route.
var snapshotActionIDs = map[string]string{
	"delete":  broker.ActionIncusSnapshotDelete,
	"restore": broker.ActionIncusSnapshotRestore,
}

// destructiveActions require an explicit confirmation round trip before the
// broker will run them.
var destructiveActions = map[string]bool{"remove": true, "stop": true, "stop-force": true}

// confirmTitles and successMessages are written out per action rather than
// derived from the action word. Deriving them produced wrong English for
// every action but "remove" ("Instance startd", "Instance stopd"), and
// "stop-force" has no correct derived form at all.
var confirmTitles = map[string]string{
	"delete":     "Delete Incus snapshot",
	"remove":     "Remove Incus instance",
	"restore":    "Restore Incus snapshot",
	"stop":       "Stop Incus instance",
	"stop-force": "Force stop Incus instance",
}

var successMessages = map[string]string{
	"delete":     "Snapshot deleted",
	"remove":     "Instance removed",
	"restart":    "Instance restarted",
	"restore":    "Snapshot restored",
	"start":      "Instance started",
	"stop":       "Instance stopped",
	"stop-force": "Instance force stopped",
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
	mux.HandleFunc("GET /incus/instances/{name}", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		project := r.URL.Query().Get("project")
		value, err := queryDetail(ctx, host, project, r.PathValue("name"))
		if err != nil {
			http.Error(w, "Failed to load the instance. Please check that the Incus daemon is running.", http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active: "incus", Body: DetailPage(value, host.CSRFToken(r), host.Identity(r).Admin),
			Eyebrow: "Instance detail", Title: value.Instance.Name,
		})
	}))
	mux.HandleFunc("GET /incus/instances/{name}/logs", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		source := r.URL.Query().Get("source")
		if source == "" {
			source = SourceConsole
		}
		if !validSource(source) {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		name := r.PathValue("name")
		project := r.URL.Query().Get("project")
		logs, err := queryLogs(ctx, host, project, name, source)
		unavailable := err != nil
		if unavailable {
			logs = Logs{Name: name, Project: project, Source: source}
		}
		_ = host.Render(w, r, platform.Page{
			Active: "incus", Body: LogsView(logs, unavailable),
			Eyebrow: "Instance diagnostics", Title: name + " logs",
		})
	}))
	mux.HandleFunc("GET /incus/networks/{name}", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		project := r.URL.Query().Get("project")
		value, err := queryNetwork(ctx, host, project, r.PathValue("name"))
		if err != nil {
			http.Error(w, "Failed to load the network. Please check that the Incus daemon is running.", http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active: "incus", Body: NetworkPage(value),
			Eyebrow: "Network detail", Title: value.Name,
		})
	}))
	mux.HandleFunc("GET /incus/profiles/{name}", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		project := r.URL.Query().Get("project")
		value, err := queryProfile(ctx, host, project, r.PathValue("name"))
		if err != nil {
			http.Error(w, "Failed to load the profile. Please check that the Incus daemon is running.", http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active: "incus", Body: ProfilePage(value),
			Eyebrow: "Profile detail", Title: value.Name,
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
		if destructiveActions[action] && !host.ConfirmAction(w, r, confirmTitles[action], "incus/instance/"+project+"/"+name) {
			return
		}
		err := host.Execute(ctx, r, actionID, map[string]string{"name": name, "project": project})
		m.redirect(w, r, project, successMessages[action], err)
	}))
	// Snapshot creation names a new snapshot, so the name arrives in the
	// form rather than the path; the broker validates it again before use.
	mux.HandleFunc("POST /incus/instances/{name}/snapshots", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		name := r.PathValue("name")
		project := r.FormValue("project")
		snapshot := strings.TrimSpace(r.FormValue("snapshot"))
		err := host.Execute(ctx, r, broker.ActionIncusSnapshotCreate, map[string]string{
			"instance": name, "project": project, "snapshot": snapshot,
		})
		m.redirectInstance(w, r, project, name, "Snapshot created", err)
	}))
	mux.HandleFunc("POST /incus/instances/{name}/snapshots/{snapshot}/{action}", platform.Gate(host, []capability.ID{capability.Incus}, func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		action := r.PathValue("action")
		actionID, ok := snapshotActionIDs[action]
		if !ok {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		name := r.PathValue("name")
		snapshot := r.PathValue("snapshot")
		project := r.FormValue("project")
		// Both snapshot actions are destructive: restore discards
		// everything written since the snapshot, delete discards the
		// snapshot itself.
		if !host.ConfirmAction(w, r, confirmTitles[action], "incus/snapshot/"+project+"/"+name+"/"+snapshot) {
			return
		}
		err := host.Execute(ctx, r, actionID, map[string]string{
			"instance": name, "project": project, "snapshot": snapshot,
		})
		m.redirectInstance(w, r, project, name, successMessages[action], err)
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

func queryDetail(ctx context.Context, host platform.Host, project, name string) (Detail, error) {
	var value Detail
	if err := host.Query(ctx, broker.QueryIncusInstance, map[string]string{"name": name, "project": project}, &value); err != nil {
		return Detail{}, err
	}
	return value, nil
}

func queryLogs(ctx context.Context, host platform.Host, project, name, source string) (Logs, error) {
	var logs Logs
	parameters := map[string]string{"name": name, "project": project, "source": source}
	if err := host.Query(ctx, broker.QueryIncusLogs, parameters, &logs); err != nil {
		return Logs{}, err
	}
	return logs, nil
}

func queryNetwork(ctx context.Context, host platform.Host, project, name string) (NetworkDetail, error) {
	var value NetworkDetail
	if err := host.Query(ctx, broker.QueryIncusNetwork, map[string]string{"name": name, "project": project}, &value); err != nil {
		return NetworkDetail{}, err
	}
	return value, nil
}

func queryProfile(ctx context.Context, host platform.Host, project, name string) (ProfileDetail, error) {
	var value ProfileDetail
	if err := host.Query(ctx, broker.QueryIncusProfile, map[string]string{"name": name, "project": project}, &value); err != nil {
		return ProfileDetail{}, err
	}
	return value, nil
}

func projectUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "project is not available")
}

// redirectInstance returns to the instance's own detail page, so a snapshot
// action lands back where its result is visible rather than on the list.
func (m *Module) redirectInstance(w http.ResponseWriter, r *http.Request, project, name, success string, err error) {
	values := url.Values{}
	values.Set("project", project)
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", "Action failed. Review Activity for the recorded outcome.")
	} else {
		values.Set("notice", success)
	}
	m.finish(w, r, "/incus/instances/"+url.PathEscape(name)+"?"+values.Encode())
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
	m.finish(w, r, "/incus?"+values.Encode())
}

// finish emits the HTMX redirect header for a boosted request and an
// ordinary 303 otherwise, per docs/modules.md's action conventions.
func (m *Module) finish(w http.ResponseWriter, r *http.Request, destination string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
