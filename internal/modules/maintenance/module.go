package maintenance

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: Summary(state), Order: 34, Span: platform.SpanHalf}}, nil
}

func (*Module) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	findings := make([]platform.Finding, 0)
	if len(state.Updates) > 0 {
		findings = append(findings, platform.Finding{ID: "maintenance.updates", Source: "Maintenance", Severity: platform.SeverityWarning, Title: "Extension updates are available", Detail: plural(len(state.Updates), "update"), Path: "/maintenance"})
	}
	if state.RebootRequired {
		detail := "Installed changes require activation."
		if len(state.RebootReasons) > 0 {
			detail = state.RebootReasons[0]
		}
		findings = append(findings, platform.Finding{ID: "maintenance.reboot", Source: "Maintenance", Severity: platform.SeverityWarning, Title: "A reboot is required", Detail: detail, Path: "/maintenance"})
	}
	seen := map[string]bool{}
	for _, job := range state.Jobs {
		if seen[job.Action] {
			continue
		}
		seen[job.Action] = true
		if job.Status == jobs.StatusFailed || job.Status == jobs.StatusUnknown {
			severity := platform.SeverityCritical
			if job.Status == jobs.StatusUnknown {
				severity = platform.SeverityUnknown
			}
			findings = append(findings, platform.Finding{ID: "maintenance.job." + job.Action, Source: "Maintenance", Severity: severity, Title: "Maintenance job " + job.Status, Detail: job.Resource, Path: "/activity"})
		}
	}
	return findings, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "maintenance", Name: "Maintenance", Description: "Updates, jobs, and reboot posture", Icon: "refresh", Order: 34, Path: "/maintenance"}
}

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /maintenance", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, "Maintenance status is unavailable.", http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{Active: "maintenance", Body: Page(state, host.CSRFToken(r), host.Identity(r).Admin), Eyebrow: "Host lifecycle", Title: "Maintenance"})
	})
	mux.HandleFunc("POST /maintenance/reboot", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) || !host.ConfirmAction(w, r, "Reboot the host", "maintenance/reboot") {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := host.Execute(ctx, r, broker.ActionMaintenanceReboot, nil)
		values := url.Values{}
		if err != nil {
			values.Set("kind", "error")
			values.Set("notice", "Reboot could not be scheduled. Review Activity for the recorded outcome.")
		} else {
			values.Set("notice", "Host reboot requested.")
		}
		http.Redirect(w, r, "/maintenance?"+values.Encode(), http.StatusSeeOther)
	})
}

func queryState(ctx context.Context, host platform.Host) (State, error) {
	var state State
	err := host.Query(ctx, broker.QueryMaintenanceState, nil, &state)
	return state, err
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
