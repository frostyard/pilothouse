package maintenance

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/frostyard/pilothouse/internal/web"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullTestCapabilities matches the default fake Host capability set used
// across this repo's module tests: every capability present, so existing
// tests that don't care about gating keep exercising the full-capability
// path unchanged.
var fullTestCapabilities = capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)

type moduleHost struct {
	action       string
	confirmation string
	state        State
	// queries records every broker query ID this host was asked for, in
	// order, so a test can prove a capability-gated call (notably
	// QueryMaintenanceState, which the daemon registers only under Systemd)
	// is never made at all on a host lacking its capability — not merely
	// that its result went unused.
	queries []string
	// page records the last platform.Page handed to Render, so a test can
	// assert on the exact body component a handler rendered.
	page     platform.Page
	rendered bool
	// caps overrides Capabilities' return value when capsSet is true.
	// Leaving both zero (the default for a bare &moduleHost{}) falls back
	// to fullTestCapabilities, so existing tests that never touch
	// capability gating keep exercising the full-capability path
	// unchanged; a gating test sets both explicitly, including to an
	// intentionally empty capability.Set{}.
	caps    capability.Set
	capsSet bool
}

func (h *moduleHost) Capabilities(context.Context) capability.Set {
	if !h.capsSet {
		return fullTestCapabilities
	}
	return h.caps
}
func (h *moduleHost) ConfirmAction(_ http.ResponseWriter, _ *http.Request, _ string, resource string) bool {
	h.confirmation = resource
	return true
}
func (*moduleHost) CSRFToken(*http.Request) string { return "csrf" }
func (h *moduleHost) Execute(_ context.Context, _ *http.Request, action string, _ map[string]string) error {
	h.action = action
	return nil
}
func (*moduleHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: true} }
func (h *moduleHost) Query(_ context.Context, id string, _ map[string]string, target any) error {
	h.queries = append(h.queries, id)
	if id == broker.QueryMaintenanceState {
		*target.(*State) = h.state
	}
	return nil
}
func (h *moduleHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
	h.rendered = true
	return nil
}
func (*moduleHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }
func (*moduleHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (*moduleHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (*moduleHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

// renderComponent renders a templ component to HTML so two components can be
// compared for byte-for-byte equality.
func renderComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var output strings.Builder
	require.NoError(t, component.Render(context.Background(), &output))
	return output.String()
}

func TestRebootRouteRequiresCanonicalConfirmation(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/maintenance/reboot", nil))
	assert.Equal(t, "maintenance/reboot", host.confirmation)
	assert.Equal(t, broker.ActionMaintenanceReboot, host.action)
}

func TestHealthReportsUpdatesRebootAndLatestFailedJob(t *testing.T) {
	host := &moduleHost{state: State{Updates: []sysext.AvailableUpdate{{Feature: "docker"}}, RebootRequired: true, RebootReasons: []string{"update activation"}, Jobs: []Job{{Action: "update", Status: jobs.StatusFailed}}}}
	findings, err := New().Health(context.Background(), host)
	assert.NoError(t, err)
	assert.Len(t, findings, 3)
}

// TestModuleGateTypeIsAnyOfNotAllOf pins the interface switch this module
// made: it declares an any-of whole-module gate over systemd, bootc, and
// rpm-ostree and no longer declares the all-of gate, so
// internal/web's moduleAvailable (platform.Available && platform.AvailableAny)
// evaluates it purely through the HasAny test.
func TestModuleGateTypeIsAnyOfNotAllOf(t *testing.T) {
	module := New()

	gateAny, isGateAny := any(module).(platform.CapabilityGateAny)
	require.True(t, isGateAny, "maintenance.Module must implement platform.CapabilityGateAny")
	assert.Equal(t, []capability.ID{capability.Systemd, capability.Bootc, capability.RPMOStree}, gateAny.RequiredAnyCapabilities())

	_, isGate := any(module).(platform.CapabilityGate)
	assert.False(t, isGate, "maintenance.Module must no longer implement platform.CapabilityGate")
}

// capabilityFixtures is the hand-written expectation matrix for this
// module's whole-module gate, derived from the spec (any one of systemd,
// bootc, or rpm-ostree makes the module present; the reboot action stays
// systemd-only) rather than from any call into the gating code under test.
// Every subset of the three gate capabilities appears, plus a fixture whose
// only capability is unrelated.
var capabilityFixtures = []struct {
	name string
	caps capability.Set
	// available is whether the whole module (nav entry, dashboard card,
	// GET /maintenance) is expected to be present.
	available bool
	// reboot is whether POST /maintenance/reboot is expected to be served.
	reboot bool
}{
	{name: "none of the three", caps: capability.New(capability.Journald), available: false, reboot: false},
	{name: "empty set", caps: capability.Set{}, available: false, reboot: false},
	{name: "systemd only", caps: capability.New(capability.Systemd), available: true, reboot: true},
	{name: "bootc only", caps: capability.New(capability.Bootc), available: true, reboot: false},
	{name: "rpm-ostree only", caps: capability.New(capability.RPMOStree), available: true, reboot: false},
	{name: "bootc and rpm-ostree", caps: capability.New(capability.Bootc, capability.RPMOStree), available: true, reboot: false},
	{name: "systemd and bootc", caps: capability.New(capability.Systemd, capability.Bootc), available: true, reboot: true},
	{name: "systemd and rpm-ostree", caps: capability.New(capability.Systemd, capability.RPMOStree), available: true, reboot: true},
	{name: "all three", caps: capability.New(capability.Systemd, capability.Bootc, capability.RPMOStree), available: true, reboot: true},
}

// TestModuleAvailabilityFollowsHasAnyOfSystemdBootcRPMOStree exercises
// platform.AvailableAny (the real any-of availability predicate the web
// shell's moduleAvailable composes, not a reimplementation of it) against
// this module, generalizing the previous HasAll-based availability test to
// HasAny: the module is available whenever at least one of systemd, bootc,
// or rpm-ostree is present, and only unavailable when none of the three
// are. platform.Available is asserted alongside it because moduleAvailable
// ANDs the two, and this module now relies on Available's
// no-CapabilityGate default of true.
func TestModuleAvailabilityFollowsHasAnyOfSystemdBootcRPMOStree(t *testing.T) {
	module := New()
	for _, fixture := range capabilityFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			assert.Equal(t, fixture.available, platform.AvailableAny(module, fixture.caps))
			assert.True(t, platform.Available(module, fixture.caps), "module declares no all-of gate, so Available must default to true")
		})
	}
}

// TestRoutesFollowCapabilityMatrix proves — via a real ServeMux round trip
// through Mount, not a test-only stand-in — that GET /maintenance is served
// whenever any one of systemd, bootc, or rpm-ostree is present and 404s only
// when none are, while POST /maintenance/reboot keeps its narrower
// systemd-only gate regardless of bootc/rpm-ostree.
func TestRoutesFollowCapabilityMatrix(t *testing.T) {
	for _, fixture := range capabilityFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			host := &moduleHost{caps: fixture.caps, capsSet: true}
			mux := http.NewServeMux()
			New().Mount(mux, host)

			page := httptest.NewRecorder()
			mux.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
			if fixture.available {
				assert.Equal(t, http.StatusOK, page.Code)
				assert.True(t, host.rendered, "GET /maintenance must render a page")
			} else {
				assert.Equal(t, http.StatusNotFound, page.Code)
				assert.False(t, host.rendered)
			}

			reboot := httptest.NewRecorder()
			mux.ServeHTTP(reboot, httptest.NewRequest(http.MethodPost, "/maintenance/reboot", nil))
			if fixture.reboot {
				assert.Equal(t, http.StatusSeeOther, reboot.Code)
				assert.Equal(t, broker.ActionMaintenanceReboot, host.action)
			} else {
				assert.Equal(t, http.StatusNotFound, reboot.Code)
				assert.Empty(t, host.action, "the reboot action must not be executed without systemd")
			}
		})
	}
}

// TestPageRendersWithoutSystemdQueryOnHostImageOnlyHosts covers the failure
// mode the any-of gate creates: with the module now present on a host that
// has bootc (or rpm-ostree) but no systemd, QueryMaintenanceState — which
// the daemon registers only under systemd — must not be called at all, and
// the page must still render rather than 503. The fake host counts every
// query it is asked for, so this proves the call is skipped, not merely
// that its result was discarded.
func TestPageRendersWithoutSystemdQueryOnHostImageOnlyHosts(t *testing.T) {
	for _, fixture := range []struct {
		name string
		caps capability.Set
	}{
		{name: "bootc only", caps: capability.New(capability.Bootc)},
		{name: "rpm-ostree only", caps: capability.New(capability.RPMOStree)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			host := &moduleHost{caps: fixture.caps, capsSet: true, state: State{OSVersion: "never read", RebootRequired: true}}
			mux := http.NewServeMux()
			New().Mount(mux, host)

			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/maintenance", nil))

			assert.Equal(t, http.StatusOK, response.Code)
			assert.NotContains(t, host.queries, broker.QueryMaintenanceState)
			require.True(t, host.rendered)
			assert.Equal(t, renderComponent(t, Page(State{}, "csrf", true)), renderComponent(t, host.page.Body), "the page must render from the zero State when systemd is absent")
			assert.NotContains(t, renderComponent(t, host.page.Body), "/maintenance/reboot", "the page must not render a control targeting the systemd-only reboot route on a host where that route 404s")
		})
	}
}

// TestPageRendersNoRebootControlWithoutSystemd is the dead-control audit the
// any-of gate makes necessary: the module is now present on hosts where
// POST /maintenance/reboot 404s, so every view element targeting that route
// must be absent there. views.templ has exactly one such element — the
// admin "Reboot host" form inside `if state.RebootRequired` — and the zero
// State substituted when Systemd is absent makes that condition false, so
// the control cannot render on a host that cannot serve it. The systemd
// fixtures below pin the other end: the same form does render (for an admin,
// when a reboot is required) exactly where the route is served.
func TestPageRendersNoRebootControlWithoutSystemd(t *testing.T) {
	for _, fixture := range capabilityFixtures {
		if !fixture.available {
			continue
		}
		t.Run(fixture.name, func(t *testing.T) {
			host := &moduleHost{caps: fixture.caps, capsSet: true, state: State{RebootRequired: true, RebootReasons: []string{"update activation"}}}
			mux := http.NewServeMux()
			New().Mount(mux, host)

			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
			require.Equal(t, http.StatusOK, response.Code)
			require.True(t, host.rendered)
			body := renderComponent(t, host.page.Body)

			if fixture.reboot {
				assert.Contains(t, body, `action="/maintenance/reboot"`, "the reboot form belongs on hosts whose reboot route is served")
			} else {
				assert.NotContains(t, body, "/maintenance/reboot", "no control may target the reboot route on a host without systemd")
			}
		})
	}
}

// TestDashboardAndHealthDegradeWithoutSystemd proves the other two call
// paths into the systemd-gated query behave the same way: on a bootc-only
// host both report "nothing to report" instead of erroring or panicking,
// and neither reaches QueryMaintenanceState.
func TestDashboardAndHealthDegradeWithoutSystemd(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.Bootc), capsSet: true, state: State{RebootRequired: true, Updates: []sysext.AvailableUpdate{{Feature: "docker"}}}}

	cards, err := New().Dashboard(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, renderComponent(t, Summary(State{})), renderComponent(t, cards[0].Component))

	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	assert.Empty(t, findings)

	assert.NotContains(t, host.queries, broker.QueryMaintenanceState)
}

// TestRoutesServeWhenSystemdPresent proves the reworked gate is additive,
// not a regression: with systemd present (fullTestCapabilities, the
// default) both routes behave exactly as they did before this chunk,
// including rendering the same page body from the same queried State.
func TestRoutesServeWhenSystemdPresent(t *testing.T) {
	state := State{OSVersion: "42.20260101", Updates: []sysext.AvailableUpdate{{Feature: "docker", Component: "docker", Current: "1", Newest: "2"}}, RebootRequired: true, RebootReasons: []string{"update activation"}, Jobs: []Job{{ID: 7, Action: "update", Resource: "docker", Status: jobs.StatusSucceeded}}}
	host := &moduleHost{state: state}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, host.queries, broker.QueryMaintenanceState)
	require.True(t, host.rendered)
	assert.Equal(t, "maintenance", host.page.Active)
	assert.Equal(t, "Host lifecycle", host.page.Eyebrow)
	assert.Equal(t, "Maintenance", host.page.Title)
	assert.Equal(t, renderComponent(t, Page(state, "csrf", true)), renderComponent(t, host.page.Body))

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/maintenance/reboot", nil))
	assert.Equal(t, "maintenance/reboot", host.confirmation)
	assert.Equal(t, broker.ActionMaintenanceReboot, host.action)
}

// TestDashboardAndHealthUnchangedWhenSystemdPresent pins the full-capability
// behavior of the two non-route call paths against the same expectations
// they had before the gate rework.
func TestDashboardAndHealthUnchangedWhenSystemdPresent(t *testing.T) {
	state := State{Updates: []sysext.AvailableUpdate{{Feature: "docker"}}, RebootRequired: true, RebootReasons: []string{"update activation"}}
	host := &moduleHost{state: state}

	cards, err := New().Dashboard(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, 34, cards[0].Order)
	assert.Equal(t, platform.SpanHalf, cards[0].Span)
	assert.Equal(t, renderComponent(t, Summary(state)), renderComponent(t, cards[0].Component))

	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 2)
	assert.Equal(t, "maintenance.updates", findings[0].ID)
	assert.Equal(t, "maintenance.reboot", findings[1].ID)
}

// TestCollectPageRecordsHostImageAvailability pins the second half of the
// page handler's capability decision: alongside skipping the systemd-only
// query, it records whether the host has any host-image source at all
// (HasAny(Bootc, RPMOStree)) — the flag the page's host-image section will
// attempt or skip its fetch on. Nothing renders from it yet.
func TestCollectPageRecordsHostImageAvailability(t *testing.T) {
	for _, fixture := range []struct {
		name string
		caps capability.Set
		want bool
	}{
		{name: "systemd only", caps: capability.New(capability.Systemd), want: false},
		{name: "bootc only", caps: capability.New(capability.Bootc), want: true},
		{name: "rpm-ostree only", caps: capability.New(capability.RPMOStree), want: true},
		{name: "systemd and bootc", caps: capability.New(capability.Systemd, capability.Bootc), want: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			inputs, err := collectPage(context.Background(), &moduleHost{caps: fixture.caps, capsSet: true})
			require.NoError(t, err)
			assert.Equal(t, fixture.want, inputs.hostImageAvailable)
		})
	}
}

// navE2EBroker is a minimal fake satisfying web.BrokerClient, used only to
// drive a real internal/web.Server login + dashboard round trip in the
// end-to-end tests below. Unlike the platform.AvailableAny assertions above,
// these prove internal/web's own nav and dashboard filtering (c2's
// availableManifests/moduleAvailable) actually consults this real
// maintenance.Module's RequiredAnyCapabilities end-to-end — not a synthetic
// stand-in module.
type navE2EBroker struct {
	capabilities capability.Set
}

func (b *navE2EBroker) Action(context.Context, string, string, map[string]string, string) error {
	return nil
}
func (b *navE2EBroker) Health(context.Context) error { return nil }
func (b *navE2EBroker) Login(context.Context, string, string, string) (broker.LoginResponse, error) {
	return broker.LoginResponse{
		Session: broker.SessionResponse{CSRF: "csrf", Identity: auth.Identity{Admin: true, Username: "snow"}},
		Token:   "token",
	}, nil
}
func (b *navE2EBroker) Logout(context.Context, string) error { return nil }
func (b *navE2EBroker) Query(_ context.Context, _, id string, _ map[string]string, target any) error {
	switch id {
	case broker.QueryCapabilities:
		encoded, err := json.Marshal(b.capabilities)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	case broker.QueryMaintenanceState:
		encoded, err := json.Marshal(State{OSVersion: "42.20260101"})
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	default:
		return nil
	}
}
func (b *navE2EBroker) Session(context.Context, string) (broker.SessionResponse, error) {
	return broker.SessionResponse{CSRF: "csrf", Identity: auth.Identity{Admin: true, Username: "snow"}}, nil
}
func (b *navE2EBroker) StreamAction(context.Context, string, string, map[string]string, io.Reader) error {
	return nil
}
func (b *navE2EBroker) StreamQuery(context.Context, string, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

var loginCSRFPattern = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

// authenticatedHandler builds a real internal/web.Server registered with
// only this package's real maintenance.Module (via New(), through
// platform.NewRegistry — the same constructor cmd/pilothouse wires up),
// drives a real POST /login against it, and returns the handler plus the
// session cookies, so every later request goes through server.Handler()
// exactly as a browser's would.
func authenticatedHandler(t *testing.T, caps capability.Set) (http.Handler, []*http.Cookie) {
	t.Helper()
	registry, err := platform.NewRegistry(New())
	require.NoError(t, err)
	server, err := web.NewServer(registry, &navE2EBroker{capabilities: caps}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	handler := server.Handler()

	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/login", nil))
	match := loginCSRFPattern.FindStringSubmatch(loginPage.Body.String())
	require.Len(t, match, 2, "login page must contain a csrf hidden field")

	form := url.Values{"csrf": {match[1]}, "username": {"snow"}, "password": {"secret"}}
	loginRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	require.Equal(t, http.StatusSeeOther, loginResponse.Code)
	cookies := loginResponse.Result().Cookies()
	require.NotEmpty(t, cookies, "login must set a session cookie")
	return handler, cookies
}

// authenticatedGet performs an authenticated GET against a real
// internal/web.Server handler and returns the response recorder.
func authenticatedGet(t *testing.T, handler http.Handler, cookies []*http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// fragment extracts the region of html between the first occurrence of open
// and the next occurrence of close, so nav assertions and dashboard-card
// assertions can be made independently even though both regions mention the
// module by name and link to its path.
func fragment(t *testing.T, html, openTag, closeTag string) string {
	t.Helper()
	start := strings.Index(html, openTag)
	require.GreaterOrEqual(t, start, 0, "page must contain %q", openTag)
	rest := html[start:]
	end := strings.Index(rest, closeTag)
	require.GreaterOrEqual(t, end, 0, "page must contain %q after %q", closeTag, openTag)
	return rest[:end]
}

// TestModuleSurfacesFollowHasAnyGateEndToEnd proves, through a real
// internal/web.Server login-then-request round trip against this actual
// maintenance.Module, that all three web-side surfaces the whole-module gate
// controls — the sidebar nav entry, the dashboard card, and GET /maintenance
// — appear together whenever any one of systemd, bootc, or rpm-ostree is
// advertised and disappear together when none is. The nav and dashboard
// regions are asserted separately (both mention "Maintenance" and link to
// /maintenance) so a regression in either registry alone cannot hide behind
// the other.
func TestModuleSurfacesFollowHasAnyGateEndToEnd(t *testing.T) {
	for _, fixture := range capabilityFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			handler, cookies := authenticatedHandler(t, fixture.caps)

			dashboard := authenticatedGet(t, handler, cookies, "/")
			require.Equal(t, http.StatusOK, dashboard.Code)
			html := dashboard.Body.String()
			nav := fragment(t, html, `<nav class="nav"`, "</nav>")
			cards := fragment(t, html, `<section id="dashboard"`, "</section>")

			page := authenticatedGet(t, handler, cookies, "/maintenance")

			if fixture.available {
				assert.Contains(t, nav, `href="/maintenance"`)
				assert.Contains(t, nav, "<span>Maintenance</span>")
				assert.Contains(t, cards, "<h2>Maintenance</h2>")
				assert.NotContains(t, cards, "Module unavailable", "the dashboard card must render, not degrade to an error card")
				assert.Equal(t, http.StatusOK, page.Code)
				assert.Contains(t, page.Body.String(), "Update availability, durable maintenance jobs, and host reboot posture.")
			} else {
				assert.NotContains(t, nav, `href="/maintenance"`)
				assert.NotContains(t, nav, "<span>Maintenance</span>")
				assert.NotContains(t, cards, "Maintenance")
				assert.Equal(t, http.StatusNotFound, page.Code)
			}
		})
	}
}

// TestRebootRouteStaysSystemdOnlyEndToEnd is the reboot half of the same
// end-to-end round trip: the action route is reachable only when systemd
// specifically is advertised, regardless of bootc/rpm-ostree, even on
// fixtures where the module itself is present.
func TestRebootRouteStaysSystemdOnlyEndToEnd(t *testing.T) {
	for _, fixture := range capabilityFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			handler, cookies := authenticatedHandler(t, fixture.caps)

			request := httptest.NewRequest(http.MethodPost, "/maintenance/reboot", strings.NewReader(url.Values{"csrf": {"csrf"}}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			for _, cookie := range cookies {
				request.AddCookie(cookie)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if fixture.reboot {
				assert.NotEqual(t, http.StatusNotFound, response.Code)
				assert.Contains(t, response.Body.String(), "Reboot the host", "the route must reach the module's confirmation step, not merely avoid a 404")
			} else {
				assert.Equal(t, http.StatusNotFound, response.Code)
				assert.NotContains(t, response.Body.String(), "Reboot the host")
			}
		})
	}
}
