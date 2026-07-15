package services

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
		_ = host.Render(w, r, platform.Page{Active: "services", Body: Page(state, host.CSRFToken(r), host.Identity(r).Admin), Eyebrow: "systemd control plane", Title: "Services"})
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
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		err := host.Execute(ctx, r, actionID, map[string]string{"unit": unit})
		m.redirect(w, r, fmt.Sprintf("%s %sd", unit, action), err)
	})
}

var actionIDs = map[string]string{"start": broker.ActionServicesStart, "stop": broker.ActionServicesStop, "restart": broker.ActionServicesRestart, "enable": broker.ActionServicesEnable, "disable": broker.ActionServicesDisable, "reset-failed": broker.ActionServicesResetFailed}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	err := host.Query(ctx, broker.QueryServicesState, nil, &state)
	return state, err
}

func (*Module) redirect(w http.ResponseWriter, r *http.Request, success string, err error) {
	values := url.Values{}
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", err.Error())
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
