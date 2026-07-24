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
		_ = host.Render(w, r, platform.Page{Active: "maintenance", Body: Page(inputs.state, inputs.hostImage, inputs.autoUpdate, host.CSRFToken(r), host.Identity(r).Admin), Eyebrow: "Host lifecycle", Title: "Maintenance"})
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
	// hostImage is QueryHostImageStatus's response when the host advertises
	// HasAny(Bootc, RPMOStree), and nil when it advertises neither (see
	// queryHostImage). Page renders the whole "Host image" section — the
	// booted/staged/rollback deployments, the per-source unavailable
	// indicators, and the page's single soft-reboot-eligibility indicator —
	// only when this is non-nil, so a host with no host-image source omits
	// the section entirely rather than showing an empty placeholder.
	//
	// Nil-ness is the availability flag: the page can only render host-image
	// content it actually fetched, so there is no separate boolean that
	// could disagree with the data.
	hostImage *HostImageStatus
	// autoUpdate is QueryAutoUpdateStatus's response when the host advertises
	// HasAny(Bootc, RPMOStree), and nil when it advertises neither (see
	// queryAutoUpdate). Page renders the whole "Automatic updates" section —
	// both updaters' configured detail and their explicit not-configured
	// states — only when this is non-nil, so a host with no image-based
	// updater source omits the section entirely rather than showing an empty
	// placeholder.
	//
	// It follows hostImage's nil-ness-is-availability convention for the same
	// reason and under the same any-of gate; the two are separate fields
	// because they are separate broker queries whose responses are
	// independent.
	autoUpdate *AutoUpdateStatus
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
	hostImage, err := queryHostImage(ctx, host, caps)
	if err != nil {
		return pageInputs{}, err
	}
	autoUpdate, err := queryAutoUpdate(ctx, host, caps)
	if err != nil {
		return pageInputs{}, err
	}
	return pageInputs{state: state, hostImage: hostImage, autoUpdate: autoUpdate}, nil
}

// queryAutoUpdate returns QueryAutoUpdateStatus's response when caps advertises
// at least one image-based updater source, and nil when it advertises neither.
// It mirrors queryHostImage exactly, because the daemon registers both queries
// under the same any-of gate: registerAutoUpdate (cmd/pilothoused/main.go)
// guards QueryAutoUpdateStatus with HasAny(Bootc, RPMOStree), so on a
// systemd-only host the query is absent rather than empty and calling it would
// fail. Returning nil instead is what makes the page omit the
// "Automatic updates" section on such a host rather than 503.
//
// The gate is deliberately Bootc/RPMOStree and *not* AutoupdateBootc /
// AutoupdateRPMOStree. Those two capabilities drive the per-updater
// configured/not-configured split inside the response itself
// (AutoUpdateStatus.BootcConfigured / RPMOStreeConfigured); gating the call on
// them would make the "no updater is configured" report — a valid, reportable
// state on an image host — unreachable, because the query 404s on precisely
// that host.
//
// A non-nil error here means the broker call itself failed, which takes the
// page down as any other failed page query does. It is not how "this updater
// is not configured" arrives: that is an ordinary successful response carrying
// both *Configured flags false and both payload pointers nil.
func queryAutoUpdate(ctx context.Context, host platform.Host, caps capability.Set) (*AutoUpdateStatus, error) {
	if !caps.HasAny(capability.Bootc, capability.RPMOStree) {
		return nil, nil
	}
	var status AutoUpdateStatus
	if err := host.Query(ctx, broker.QueryAutoUpdateStatus, nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// queryHostImage returns QueryHostImageStatus's response when caps advertises
// at least one host-image source, and nil when it advertises neither. The
// daemon registers that query only under HasAny(Bootc, RPMOStree)
// (docs/capabilities.md's one any-of row), so on a systemd-only host it is
// absent rather than empty: calling it there would fail. Returning nil instead
// is what makes the page omit the host-image section on such a host rather
// than 503 or render an error placeholder.
//
// The gate is HasAny(Bootc, RPMOStree) and deliberately says nothing about
// Systemd, so everything the section renders — including soft-reboot
// eligibility, which the page renders from HostImageStatus.SoftRebootCapable
// and not from the Systemd-gated State.SoftRebootCapable — is available on a
// bootc-only host exactly as it is on a bootc-plus-systemd one.
//
// A non-nil error here means the broker call itself failed, which takes the
// page down as any other failed page query does. It is not how a *source*
// failure arrives: a host whose bootc or rpm-ostree is present but did not
// answer still gets a successful response, carrying BootcError/RPMOStreeError,
// which the section renders as a per-source unavailable indicator.
func queryHostImage(ctx context.Context, host platform.Host, caps capability.Set) (*HostImageStatus, error) {
	if !caps.HasAny(capability.Bootc, capability.RPMOStree) {
		return nil, nil
	}
	var status HostImageStatus
	if err := host.Query(ctx, broker.QueryHostImageStatus, nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
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
