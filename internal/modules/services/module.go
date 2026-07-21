package services

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

type Filters struct {
	Query         string
	Status        string
	Type          string
	UnitFileState string
}

type FilterOptions struct {
	Statuses       []string
	UnitFileStates []string
}

func New() *Module { return &Module{} }

func (m *Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: SummaryCard(state), Order: 35, Span: platform.SpanHalf}}, nil
}

func (m *Module) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	findings := make([]platform.Finding, 0, state.Summary.Failed)
	for _, unit := range state.Units {
		if unit.ActiveState == "failed" {
			findings = append(findings, platform.Finding{ID: "services." + unit.Name, Source: "Services", Severity: platform.SeverityCritical, Title: "Systemd unit failed", Detail: unit.Name, Path: "/services"})
		}
	}
	return findings, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "services", Name: "Services", Description: "Inspect and manage systemd services, sockets, and timers", Icon: "server", Order: 35, Path: "/services"}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /services", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		filters := Filters{
			Query:         strings.TrimSpace(r.URL.Query().Get("query")),
			Status:        r.URL.Query().Get("status"),
			Type:          r.URL.Query().Get("type"),
			UnitFileState: r.URL.Query().Get("unit-file"),
		}
		options := filterOptions(state)
		filters = normalizeFilters(filters, options)
		state = filterState(state, filters)
		_ = host.Render(w, r, platform.Page{Active: "services", Body: Page(state, filters, options, host.CSRFToken(r), host.Identity(r).Admin), Eyebrow: "systemd control plane", Title: "Services"})
	})
	mux.HandleFunc("GET /services/{unit}/logs", func(w http.ResponseWriter, r *http.Request) {
		unit := r.PathValue("unit")
		if !validUnitName(unit) {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		journal, err := queryJournal(ctx, host, unit)
		unavailable := err != nil
		if unavailable {
			journal = Journal{Unit: unit}
		}
		_ = host.Render(w, r, platform.Page{Active: "services", Body: Logs(journal, unavailable), Eyebrow: "systemd diagnostics", Title: unit + " logs"})
	})
	mux.HandleFunc("POST /services/{unit}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		unit, action := r.PathValue("unit"), r.PathValue("action")
		if !validUnitName(unit) {
			http.NotFound(w, r)
			return
		}
		actionID, ok := actionIDs[action]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if (action == "stop" || action == "disable") && !host.ConfirmAction(w, r, fmt.Sprintf("%s %s", action, unit), "services/unit/"+unit) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, actionID, map[string]string{"unit": unit})
		m.redirect(w, r, fmt.Sprintf("%s %s", unit, actionNotices[action]), err)
	})
}

func normalizeFilters(filters Filters, options FilterOptions) Filters {
	if !slices.Contains(options.Statuses, filters.Status) {
		filters.Status = ""
	}
	if filters.Type != "service" && filters.Type != "socket" && filters.Type != "timer" {
		filters.Type = ""
	}
	if !slices.Contains(options.UnitFileStates, filters.UnitFileState) {
		filters.UnitFileState = ""
	}
	return filters
}

func filterState(state State, filters Filters) State {
	query := strings.ToLower(filters.Query)
	units := make([]Unit, 0, len(state.Units))
	for _, unit := range state.Units {
		if query != "" && !strings.Contains(strings.ToLower(unit.Name), query) && !strings.Contains(strings.ToLower(unit.Description), query) {
			continue
		}
		if filters.Status != "" && unit.ActiveState != filters.Status {
			continue
		}
		if filters.Type != "" && !strings.HasSuffix(unit.Name, "."+filters.Type) {
			continue
		}
		if filters.UnitFileState != "" && unit.UnitFileState != filters.UnitFileState {
			continue
		}
		units = append(units, unit)
	}
	state.Units = units
	return state
}

func filterOptions(state State) FilterOptions {
	statuses := []string{"active", "inactive", "failed"}
	knownStatuses := map[string]bool{"active": true, "inactive": true, "failed": true}
	unitFileStates := map[string]bool{}
	var additionalStatuses []string
	for _, unit := range state.Units {
		if unit.ActiveState != "" && !knownStatuses[unit.ActiveState] {
			knownStatuses[unit.ActiveState] = true
			additionalStatuses = append(additionalStatuses, unit.ActiveState)
		}
		if unit.UnitFileState != "" {
			unitFileStates[unit.UnitFileState] = true
		}
	}
	slices.Sort(additionalStatuses)
	statuses = append(statuses, additionalStatuses...)
	fileStates := make([]string, 0, len(unitFileStates))
	for state := range unitFileStates {
		fileStates = append(fileStates, state)
	}
	slices.Sort(fileStates)
	return FilterOptions{Statuses: statuses, UnitFileStates: fileStates}
}

func (f Filters) Active() bool {
	return f.Query != "" || f.Status != "" || f.Type != "" || f.UnitFileState != ""
}

func filterLabel(value string) string {
	value = strings.ReplaceAll(value, "-", " ")
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

var actionIDs = map[string]string{"start": broker.ActionServicesStart, "stop": broker.ActionServicesStop, "restart": broker.ActionServicesRestart, "enable": broker.ActionServicesEnable, "disable": broker.ActionServicesDisable, "reset-failed": broker.ActionServicesResetFailed}

var actionNotices = map[string]string{"start": "started", "stop": "stopped", "restart": "restarted", "enable": "enabled", "disable": "disabled", "reset-failed": "failure reset"}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	err := host.Query(ctx, broker.QueryServicesState, nil, &state)
	return state, err
}

func queryJournal(ctx context.Context, host platform.Host, unit string) (Journal, error) {
	var journal Journal
	err := host.Query(ctx, broker.QueryServicesJournal, map[string]string{"unit": unit}, &journal)
	return journal, err
}

func (*Module) redirect(w http.ResponseWriter, r *http.Request, success string, err error) {
	values := url.Values{}
	for _, name := range []string{"query", "status", "type", "unit-file"} {
		if value := r.FormValue(name); value != "" {
			values.Set(name, value)
		}
	}
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", "Action failed. Review Activity for the recorded outcome.")
	} else {
		values.Set("notice", success)
	}
	destination := "/services?" + values.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
