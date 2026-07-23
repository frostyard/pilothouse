package logs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
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

type logsHost struct {
	admin      bool
	queryID    string
	parameters map[string]string
	queryErr   error
	queryCalls int
	page       platform.Page
	// caps overrides Capabilities' return value when capsSet is true.
	// Leaving both zero (the default for a bare &logsHost{}) falls back to
	// fullTestCapabilities, so existing tests that never touch capability
	// gating keep exercising the full-capability path unchanged; a gating
	// test sets both explicitly, including to an intentionally narrower
	// capability.Set.
	caps    capability.Set
	capsSet bool
}

func (h *logsHost) Capabilities(context.Context) capability.Set {
	if !h.capsSet {
		return fullTestCapabilities
	}
	return h.caps
}
func (*logsHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }
func (*logsHost) CSRFToken(*http.Request) string                                        { return "" }
func (*logsHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (h *logsHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: h.admin} }
func (h *logsHost) Query(_ context.Context, id string, parameters map[string]string, target any) error {
	h.queryCalls++
	h.queryID, h.parameters = id, parameters
	if h.queryErr != nil {
		return h.queryErr
	}
	state := target.(*State)
	state.Filters = Filters{
		Query: parameters["query"], Priority: parameters["priority"],
		Unit: parameters["unit"], Window: parameters["window"],
	}
	state.Units = []string{"sshd.service"}
	return nil
}
func (h *logsHost) Render(w http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
	return page.Body.Render(context.Background(), w)
}
func (*logsHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }
func (*logsHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (*logsHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (*logsHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

func TestManifestAndDashboard(t *testing.T) {
	module := New()
	assert.Equal(t, platform.Manifest{
		ID: "logs", Name: "Logs", Description: "Inspect the systemd journal",
		Icon: "activity", Order: 37, Path: "/logs",
	}, module.Manifest())
	cards, err := module.Dashboard(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, cards)
	assert.Equal(t, "org.frostyard.pilothouse.logs.list", broker.QueryLogs)
}

func TestNormalizeHTTPFiltersAndPollURL(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{
		Query: strings.Repeat("界", 201), Priority: "verbose",
		Unit: "../bad.service", Window: "7d",
	})
	assert.LessOrEqual(t, utf8.RuneCountInString(filters.Query), queryMaxRunes)
	assert.LessOrEqual(t, len(filters.Query), queryMaxBytes)
	assert.Equal(t, "", filters.Priority)
	assert.Equal(t, "", filters.Unit)
	assert.Equal(t, "1h", filters.Window)
	assert.Equal(t,
		"/logs?priority=&query="+url.QueryEscape(filters.Query)+"&unit=&window=1h",
		string(pollURL(filters)),
	)
}

func TestNormalizeHTTPFiltersASCIIQueryHitsRuneCapFirst(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{Query: strings.Repeat("x", 1_100)})

	assert.Len(t, filters.Query, queryMaxRunes)
	assert.LessOrEqual(t, len(filters.Query), queryMaxBytes)
}

func TestNormalizeHTTPFiltersStripsNUL(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{Query: "panic\x00now"})

	assert.Equal(t, "panicnow", filters.Query)
}

func TestNormalizeHTTPFiltersKeepsValidValues(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{
		Query: "query", Priority: "warning", Unit: "sshd.service", Window: "6h",
	})

	assert.Equal(t, Filters{Query: "query", Priority: "warning", Unit: "sshd.service", Window: "6h"}, filters)
}

func TestLogsPageRequiresAdministratorWithoutBrokerCall(t *testing.T) {
	host := &logsHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

	assert.Zero(t, host.queryCalls)
	assert.Equal(t, "logs", host.page.Active)
	assert.Equal(t, "Logs", host.page.Title)
	assert.Equal(t, "system journal", host.page.Eyebrow)
	assert.Contains(t, response.Body.String(), "Administrator access required")
	assert.Contains(t, response.Body.String(), "<svg")
	assert.NotContains(t, response.Body.String(), "@web.")
}

func TestLogsPageDispatchesOnlyFixedQueryWithExactFilters(t *testing.T) {
	host := &logsHost{admin: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs?query=panic+now&priority=warning&unit=sshd.service&window=6h", nil))

	assert.Equal(t, broker.QueryLogs, host.queryID)
	assert.Equal(t, map[string]string{"query": "panic now", "priority": "warning", "unit": "sshd.service", "window": "6h"}, host.parameters)
	assert.Equal(t, "logs", host.page.Active)
	assert.Equal(t, "Logs", host.page.Title)
}

func TestLogsPageRendersUnavailableStateWithoutReaderErrorDisclosure(t *testing.T) {
	host := &logsHost{admin: true, queryErr: errors.New("secret backend detail")}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "System journal entries are unavailable. Retrying automatically.")
	assert.Contains(t, response.Body.String(), `hx-trigger="every 5s"`)
	assert.NotContains(t, response.Body.String(), "secret backend detail")
}

func TestRequiredCapabilitiesIsSystemdAndJournald(t *testing.T) {
	assert.Equal(t, []capability.ID{capability.Systemd, capability.Journald}, New().RequiredCapabilities())
}

// TestModuleAvailabilityGatedOnSystemdAndJournald exercises platform.Available
// (the registry's real module-availability predicate, which also drives the
// shell's nav filtering per c2 — not a reimplementation of it) against this
// module's RequiredCapabilities, proving the whole module — nav entry
// included — is excluded whenever either Systemd or Journald is absent.
func TestModuleAvailabilityGatedOnSystemdAndJournald(t *testing.T) {
	module := New()
	assert.True(t, platform.Available(module, capability.New(capability.Systemd, capability.Journald)))
	assert.True(t, platform.Available(module, fullTestCapabilities))
	assert.False(t, platform.Available(module, capability.New(capability.Systemd)))
	assert.False(t, platform.Available(module, capability.New(capability.Journald)))
	assert.False(t, platform.Available(module, capability.Set{}))
}

// TestLogsRouteGatesOnSystemdOrJournaldAbsent proves — via a real ServeMux
// round trip through Mount, not a test-only stand-in — that GET /logs 404s
// whenever either Systemd or Journald is absent from the fake host's
// Capabilities(), even when the other is present and the caller is an
// administrator (i.e. the gate applies before the admin/query logic runs).
func TestLogsRouteGatesOnSystemdOrJournaldAbsent(t *testing.T) {
	for name, caps := range map[string]capability.Set{
		"systemd only":  capability.New(capability.Systemd),
		"journald only": capability.New(capability.Journald),
		"neither":       capability.Set{},
	} {
		t.Run(name, func(t *testing.T) {
			host := &logsHost{admin: true, caps: caps, capsSet: true}
			mux := http.NewServeMux()
			New().Mount(mux, host)
			response := httptest.NewRecorder()

			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

			assert.Equal(t, http.StatusNotFound, response.Code)
			assert.Zero(t, host.queryCalls)
		})
	}
}

// TestLogsRouteServesWhenBothPresent proves the gate is additive, not a
// regression: with both Systemd and Journald present (fullTestCapabilities,
// the default), GET /logs behaves exactly as it did before this chunk.
func TestLogsRouteServesWhenBothPresent(t *testing.T) {
	host := &logsHost{admin: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryLogs, host.queryID)
}

// navE2EBroker is a minimal fake satisfying web.BrokerClient, used only to
// drive a real internal/web.Server login + dashboard round trip in
// TestLogsNavEntryFollowsCapabilityGateEndToEnd below. Unlike
// TestModuleAvailabilityGatedOnSystemdAndJournald (which calls
// platform.Available directly), this proves internal/web's own nav
// filtering (c2's availableManifests/moduleAvailable) actually consults
// this real logs.Module's RequiredCapabilities end-to-end — not a
// synthetic fakeGatedModule standing in for it.
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
	if id != broker.QueryCapabilities {
		return nil
	}
	encoded, err := json.Marshal(b.capabilities)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
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

// dashboardHTMLWithCapabilities builds a real internal/web.Server registered
// with only this package's real logs.Module (via New(), through
// platform.NewRegistry — the same constructor cmd/pilothouse wires up),
// drives a real POST /login against it, and returns the authenticated
// dashboard HTML for the given advertised capability set. Every step goes
// through server.Handler(), so the nav HTML asserted on by the caller is
// exactly what a browser would receive.
func dashboardHTMLWithCapabilities(t *testing.T, caps capability.Set) string {
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

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range cookies {
		dashboardRequest.AddCookie(cookie)
	}
	dashboardResponse := httptest.NewRecorder()
	handler.ServeHTTP(dashboardResponse, dashboardRequest)
	require.Equal(t, http.StatusOK, dashboardResponse.Code)
	return dashboardResponse.Body.String()
}

// TestLogsNavEntryFollowsCapabilityGateEndToEnd proves, through a real
// internal/web.Server login-then-dashboard round trip against this actual
// logs.Module, that the logs nav entry (href="/logs", name "Logs") is
// absent whenever either Systemd or Journald is missing from the broker's
// advertised capability set, and present when both are — i.e. c2's real
// nav filtering, exercised end-to-end with the real module rather than a
// synthetic stand-in or a direct platform.Available call.
func TestLogsNavEntryFollowsCapabilityGateEndToEnd(t *testing.T) {
	for name, caps := range map[string]capability.Set{
		"systemd only":  capability.New(capability.Systemd),
		"journald only": capability.New(capability.Journald),
		"neither":       capability.Set{},
	} {
		t.Run(name, func(t *testing.T) {
			html := dashboardHTMLWithCapabilities(t, caps)
			assert.NotContains(t, html, `href="/logs"`)
			assert.NotContains(t, html, ">Logs<")
		})
	}

	t.Run("both present", func(t *testing.T) {
		html := dashboardHTMLWithCapabilities(t, capability.New(capability.Systemd, capability.Journald))
		assert.Contains(t, html, `href="/logs"`)
		assert.Contains(t, html, ">Logs<")
	})
}
