package backups

import (
	"context"
	"net/http"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: Summary(state), Order: 36, Span: platform.SpanHalf}}, nil
}

func (*Module) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	findings := make([]platform.Finding, 0, len(state.Timers))
	for _, timer := range state.Timers {
		if timer.Health == HealthHealthy {
			continue
		}
		severity := platform.SeverityWarning
		switch timer.Health {
		case HealthCritical:
			severity = platform.SeverityCritical
		case HealthUnknown:
			severity = platform.SeverityUnknown
		}
		findings = append(findings, platform.Finding{
			Detail: timer.Detail, ID: "backups." + timer.Name, Path: "/backups",
			Severity: severity, Source: "Backups", Title: timer.Name + " backup is " + string(timer.Health),
		})
	}
	return findings, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "backups", Name: "Backups", Description: "Monitor configured systemd backup timers", Icon: "disk", Order: 36, Path: "/backups"}
}

// RequiredCapabilities makes the whole module — its nav entry, dashboard
// card, and its route — available only on a host that advertises systemd,
// since backup timers are systemd timer units.
func (*Module) RequiredCapabilities() []capability.ID {
	return []capability.ID{capability.Systemd}
}

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /backups", platform.Gate(host, []capability.ID{capability.Systemd}, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, "Backup status is unavailable.", http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{Active: "backups", Body: Page(state), Eyebrow: "Backup schedules", Title: "Backups"})
	}))
}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	err := host.Query(ctx, broker.QueryBackupsState, nil, &state)
	return state, err
}
