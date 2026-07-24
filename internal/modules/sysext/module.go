package sysext

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
)

// Module is the web-side Extensions surface. It holds no state and no
// extension manager: every read goes through the broker's fixed
// QueryExtensionsState aggregate, and every mutation through a fixed
// broker action. The web process performs no local updex or
// systemd-sysext reads of any kind, and nothing in this package imports
// os/exec or calls a CommandRunner — the exec-backed implementation of
// Manager and ExtensionsSource lives in the extctl subpackage, which only
// cmd/pilothoused links.
type Module struct{}

func New() *Module { return &Module{} }

// RequiredAnyCapabilities makes Extensions a whole-module any-of gate,
// mirroring the daemon's registerExtensions guard
// (caps.HasAny(Updex, Sysext)) exactly: either tool alone yields a useful
// inventory, since the aggregate is a union of updex definitions and
// systemd-sysext installed/merged state. A host advertising neither
// exposes no nav entry, no dashboard card, and no /sysext route, and the
// web side never calls QueryExtensionsState there.
//
// Extensions deliberately does not also implement platform.CapabilityGate:
// the two whole-module gates are alternatives, not layers. The narrower
// per-route and per-action guards in Mount below carry the cases where the
// daemon requires more than "either tool" — enable/disable need both, and
// the two global actions each need one specific tool.
func (*Module) RequiredAnyCapabilities() []capability.ID {
	return []capability.ID{capability.Updex, capability.Sysext}
}

func (m *Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	// No capability check here: internal/web/server.go's moduleAvailable
	// filter already skips Dashboard entirely for a module whose any-of
	// gate is unsatisfied, and this module's gate is identical to
	// QueryExtensionsState's registration guard. A host with neither tool
	// therefore never reaches this call.
	state, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{
		Component: Summary(state),
		Order:     31,
		Span:      platform.SpanHalf,
	}}, nil
}

func (m *Module) Manifest() platform.Manifest {
	return platform.Manifest{
		Description: "Install, remove, update, and refresh Snosi system extensions",
		Icon:        "sysext",
		ID:          "sysext",
		Name:        "Extensions",
		Order:       20,
		Path:        "/sysext",
	}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	// GET /sysext follows the whole-module any-of gate. The two POST route
	// families below carry narrower guards, matching
	// cmd/pilothoused/main.go's registerSysextActions split: enable and
	// disable are registered only under HasAll(Updex, Sysext), refresh only
	// under Sysext, and update only under Updex. Gating the web side on
	// anything looser would render controls the broker refuses.
	mux.HandleFunc("GET /sysext", platform.GateAny(host, m.RequiredAnyCapabilities(), func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		state, err := queryState(ctx, host)
		if err != nil {
			http.Error(w, "Extension state is unavailable.", http.StatusServiceUnavailable)
			return
		}
		// Computed once here, from one Capabilities read, and threaded
		// into the view so every control that targets a gated route is
		// collapsed by the same flag that gates the route itself.
		caps := host.Capabilities(r.Context())
		_ = host.Render(w, r, platform.Page{
			Active: "sysext",
			Body: Page(
				state,
				host.CSRFToken(r),
				host.Identity(r).Admin,
				caps.HasAll(capability.Updex, capability.Sysext),
				caps.Has(capability.Sysext),
				caps.Has(capability.Updex),
			),
			Eyebrow: "Immutable add-ons",
			Title:   "System extensions",
		})
	}))
	// Enable and disable share the daemon's HasAll(Updex, Sysext)
	// requirement, so the whole route family takes one all-of gate.
	mux.HandleFunc("POST /sysext/{name}/{action}", platform.Gate(host, []capability.ID{capability.Updex, capability.Sysext}, func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		name := r.PathValue("name")
		action := r.PathValue("action")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		var err error
		switch action {
		case "disable":
			if !host.ConfirmAction(w, r, "Disable system extension "+name, "sysext/feature/"+name) {
				return
			}
			err = host.Execute(ctx, r, broker.ActionSysextDisable, map[string]string{"name": name})
		case "enable":
			err = host.Execute(ctx, r, broker.ActionSysextEnable, map[string]string{"name": name})
		default:
			http.NotFound(w, r)
			return
		}
		m.redirect(w, r, fmt.Sprintf("%s %sd", name, action), err)
	}))
	// The two global actions have different requirements from each other,
	// so the route-level gate is the module's any-of condition and the
	// per-action requirement is checked in the handler: refresh merges
	// systemd-sysext images (Sysext), update invokes updex (Updex). An
	// action whose tool is absent 404s, indistinguishable from an unknown
	// action, so a request can never reach a broker ID the daemon did not
	// register.
	mux.HandleFunc("POST /sysext/actions/{action}", platform.GateAny(host, m.RequiredAnyCapabilities(), func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
		defer cancel()
		action := r.PathValue("action")
		caps := host.Capabilities(r.Context())
		var err error
		switch action {
		case "refresh":
			if !caps.Has(capability.Sysext) {
				http.NotFound(w, r)
				return
			}
			if !host.ConfirmAction(w, r, "Refresh system extensions", "sysext/global") {
				return
			}
			err = host.Execute(ctx, r, broker.ActionSysextRefresh, nil)
		case "update":
			if !caps.Has(capability.Updex) {
				http.NotFound(w, r)
				return
			}
			if !host.ConfirmAction(w, r, "Update system extensions", "sysext/global") {
				return
			}
			err = host.Execute(ctx, r, broker.ActionSysextUpdate, nil)
		default:
			http.NotFound(w, r)
			return
		}
		m.redirect(w, r, fmt.Sprintf("Extension %s queued", action), err)
	}))
}

// queryState is the module's single read path: the fixed
// QueryExtensionsState aggregate, never a local tool invocation.
func queryState(ctx context.Context, host platform.Host) (ExtensionsState, error) {
	var state ExtensionsState
	err := host.Query(ctx, broker.QueryExtensionsState, nil, &state)
	return state, err
}

func (m *Module) redirect(w http.ResponseWriter, r *http.Request, success string, err error) {
	values := url.Values{}
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", "Action failed. Review Activity for the recorded outcome.")
	} else {
		values.Set("notice", success)
	}
	destination := "/sysext?" + values.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
