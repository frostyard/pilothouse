package maintenance

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	state, err := queryState(ctx, host, host.Capabilities(ctx))
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: Summary(state), Order: 34, Span: platform.SpanHalf}}, nil
}

func (*Module) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	state, err := queryState(ctx, host, host.Capabilities(ctx))
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

// RequiredAnyCapabilities makes the whole module — its nav entry, dashboard
// card, and GET /maintenance below — available on a host that advertises
// *any* of systemd, bootc, or rpm-ostree (platform.CapabilityGateAny's
// HasAny semantics), rather than requiring systemd specifically. Maintenance
// reports on two independent sources: reboot posture, update availability,
// and maintenance jobs come from the systemd-gated QueryMaintenanceState,
// while host-image status comes from the separately-gated
// QueryHostImageStatus (bootc OR rpm-ostree, per docs/capabilities.md). A
// bootc-only host with no systemd therefore still has something to show, so
// the module must not disappear there. The module deliberately does not
// implement platform.CapabilityGate as well: the two whole-module gates are
// alternatives, and each individual broker call is capability-gated inside
// the handlers below instead.
func (*Module) RequiredAnyCapabilities() []capability.ID {
	return []capability.ID{capability.Systemd, capability.Bootc, capability.RPMOStree}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	// GET /maintenance follows the whole-module any-of gate: it is served
	// whenever at least one of the module's capabilities is present, and
	// 404s (indistinguishable from a route that does not exist) when none
	// are. POST /maintenance/reboot below keeps its own, narrower,
	// systemd-only platform.Gate: rebooting is a systemd operation and the
	// broker's ActionMaintenanceReboot is registered only under Systemd.
	mux.HandleFunc("GET /maintenance", platform.GateAny(host, m.RequiredAnyCapabilities(), func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		inputs, err := collectPage(ctx, host)
		if err != nil {
			http.Error(w, "Maintenance status is unavailable.", http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{Active: "maintenance", Body: Page(inputs.state, host.CSRFToken(r), host.Identity(r).Admin), Eyebrow: "Host lifecycle", Title: "Maintenance"})
	}))
	mux.HandleFunc("POST /maintenance/reboot", platform.Gate(host, []capability.ID{capability.Systemd}, func(w http.ResponseWriter, r *http.Request) {
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
	}))
}

// pageInputs holds the capability-conditional inputs GET /maintenance
// renders from. Every field is populated only when the capability the
// underlying broker call needs is advertised, so a host that satisfies the
// whole-module any-of gate through only one of the three capabilities still
// gets a page rather than an error.
type pageInputs struct {
	// state is QueryMaintenanceState's response when the host advertises
	// Systemd, and the zero State otherwise (see queryState).
	state State
	// hostImageAvailable records HasAny(Bootc, RPMOStree): whether the host
	// has a source QueryHostImageStatus could be read from at all. Nothing
	// renders from it yet — views.templ has no capability-conditional
	// content as of this commit, and no web-side code calls
	// QueryHostImageStatus — it is the flag the host-image page section
	// will attempt (or skip) its fetch on.
	hostImageAvailable bool
}

// collectPage gathers GET /maintenance's inputs, reading the host's
// capability set exactly once and gating each broker call on it
// independently.
func collectPage(ctx context.Context, host platform.Host) (pageInputs, error) {
	caps := host.Capabilities(ctx)
	state, err := queryState(ctx, host, caps)
	if err != nil {
		return pageInputs{}, err
	}
	return pageInputs{state: state, hostImageAvailable: caps.HasAny(capability.Bootc, capability.RPMOStree)}, nil
}

// queryState returns QueryMaintenanceState's response, or the zero State
// when caps lacks Systemd. The query's daemon-side handler is registered
// only under Systemd (docs/capabilities.md), so on a bootc-only host it is
// not merely empty but absent: calling it would fail, and failing it would
// take the whole page, dashboard card, or health finding set down with it.
// Omitting the call instead lets Page/Summary/Health render what the host
// does have — "nothing to report" — which is the honest answer for a host
// with no systemd rather than a 503.
func queryState(ctx context.Context, host platform.Host, caps capability.Set) (State, error) {
	var state State
	if !caps.Has(capability.Systemd) {
		return state, nil
	}
	err := host.Query(ctx, broker.QueryMaintenanceState, nil, &state)
	return state, err
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
