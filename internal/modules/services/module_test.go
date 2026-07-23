package services

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullTestCapabilities matches c1's default: every capability present, so
// existing tests that don't care about gating keep exercising the
// full-capability path unchanged.
var fullTestCapabilities = capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)

type testHost struct {
	action     string
	parameters map[string]string
	query      string
	page       platform.Page
	// caps overrides Capabilities' return value when capsSet is true.
	// Leaving both zero (the default for a bare &testHost{}) falls back to
	// fullTestCapabilities, so existing tests that never touch capability
	// gating keep exercising the full-capability path unchanged; tests
	// that need to exercise gating set both caps and capsSet explicitly,
	// including to an intentionally empty capability.Set{}.
	caps    capability.Set
	capsSet bool
	// state overrides the State returned to a *State Query, so gating
	// tests can populate units and inspect the rendered page.
	state State
}

func (h *testHost) Capabilities(context.Context) capability.Set {
	if !h.capsSet {
		return fullTestCapabilities
	}
	return h.caps
}

func (*testHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }

func (*testHost) CSRFToken(*http.Request) string { return "token" }
func (h *testHost) Execute(_ context.Context, _ *http.Request, action string, parameters map[string]string) error {
	h.action, h.parameters = action, parameters
	return nil
}
func (*testHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: true} }
func (h *testHost) Query(_ context.Context, query string, parameters map[string]string, result any) error {
	h.query, h.parameters = query, parameters
	if journal, ok := result.(*Journal); ok {
		*journal = Journal{Unit: parameters["unit"], Description: "Backup"}
	}
	if state, ok := result.(*State); ok {
		*state = h.state
	}
	return nil
}
func (h *testHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
	return nil
}
func (*testHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }
func (*testHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (*testHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (*testHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

func TestUnitActionDispatch(t *testing.T) {
	host := &testHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/services/backup.timer/restart", nil))
	assert.Equal(t, broker.ActionServicesRestart, host.action)
	assert.Equal(t, map[string]string{"unit": "backup.timer"}, host.parameters)
	destination, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "backup.timer restarted", destination.Query().Get("notice"))

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/services/backup.scope/start", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestUnitActionRedirectPreservesFilters(t *testing.T) {
	host := &testHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	form := url.Values{
		"query":     {"backup job"},
		"status":    {"failed"},
		"type":      {"timer"},
		"unit-file": {"enabled"},
	}
	request := httptest.NewRequest(http.MethodPost, "/services/backup.timer/reset-failed", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	require.Equal(t, http.StatusSeeOther, response.Code)
	destination, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "backup job", destination.Query().Get("query"))
	assert.Equal(t, "failed", destination.Query().Get("status"))
	assert.Equal(t, "timer", destination.Query().Get("type"))
	assert.Equal(t, "enabled", destination.Query().Get("unit-file"))
	assert.Equal(t, "backup.timer failure reset", destination.Query().Get("notice"))
}

func TestLogsQueryUsesFixedBrokerQueryAndUnitParameter(t *testing.T) {
	host := &testHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/services/backup.timer/logs", nil))
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryServicesJournal, host.query)
	assert.Equal(t, map[string]string{"unit": "backup.timer"}, host.parameters)
	assert.Equal(t, "backup.timer logs", host.page.Title)
}

func TestFilterStateCombinesServiceFilters(t *testing.T) {
	state := State{
		Summary: Summary{Total: 4, Active: 2, Failed: 1},
		Units: []Unit{
			{Name: "backup.timer", Description: "Nightly archive", ActiveState: "active", UnitFileState: "enabled"},
			{Name: "dbus.socket", Description: "Message bus", ActiveState: "active", UnitFileState: "static"},
			{Name: "broken.service", Description: "Broken worker", ActiveState: "failed", UnitFileState: "disabled"},
			{Name: "idle.service", Description: "Idle worker", ActiveState: "inactive", UnitFileState: "disabled"},
		},
	}

	tests := []struct {
		name    string
		filters Filters
		want    []string
	}{
		{name: "status", filters: Filters{Status: "failed"}, want: []string{"broken.service"}},
		{name: "type", filters: Filters{Type: "socket"}, want: []string{"dbus.socket"}},
		{name: "unit file", filters: Filters{UnitFileState: "disabled"}, want: []string{"broken.service", "idle.service"}},
		{name: "case insensitive description", filters: Filters{Query: "ARCHIVE"}, want: []string{"backup.timer"}},
		{name: "combined", filters: Filters{Query: "worker", Status: "inactive", Type: "service", UnitFileState: "disabled"}, want: []string{"idle.service"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filtered := filterState(state, test.filters)
			names := make([]string, 0, len(filtered.Units))
			for _, unit := range filtered.Units {
				names = append(names, unit.Name)
			}
			assert.Equal(t, test.want, names)
			assert.Equal(t, state.Summary, filtered.Summary)
		})
	}
}

func TestFilterOptionsIncludeStandardAndObservedStates(t *testing.T) {
	state := State{Units: []Unit{
		{ActiveState: "active", UnitFileState: "enabled"},
		{ActiveState: "activating", UnitFileState: "static"},
		{ActiveState: "maintenance", UnitFileState: "enabled"},
	}}
	options := filterOptions(state)
	assert.Equal(t, []string{"active", "inactive", "failed", "activating", "maintenance"}, options.Statuses)
	assert.Equal(t, []string{"enabled", "static"}, options.UnitFileStates)

	normalized := normalizeFilters(Filters{Status: "invented", Type: "scope", UnitFileState: "masked"}, options)
	assert.Equal(t, Filters{}, normalized)
}

func TestRequiredCapabilitiesIsSystemdOnly(t *testing.T) {
	assert.Equal(t, []capability.ID{capability.Systemd}, New().RequiredCapabilities())
}

// TestModuleAvailabilityGatedOnSystemd exercises platform.Available (c2's
// real production gating predicate, not a reimplementation of it) against
// this module's RequiredCapabilities, proving the whole module — nav entry
// and dashboard card, per c2's mechanism — is excluded whenever systemd is
// absent, regardless of what else is present.
func TestModuleAvailabilityGatedOnSystemd(t *testing.T) {
	module := New()
	assert.True(t, platform.Available(module, capability.New(capability.Systemd)))
	assert.True(t, platform.Available(module, capability.New(capability.Systemd, capability.Journald)))
	assert.False(t, platform.Available(module, capability.New(capability.Journald)))
	assert.False(t, platform.Available(module, capability.Set{}))
}

// TestRoutesGateOnSystemdAbsent proves — via a real ServeMux round trip
// through Mount, not a test-only stand-in — that every route this module
// registers 404s once systemd is absent, even when other capabilities
// (journald here) are present.
func TestRoutesGateOnSystemdAbsent(t *testing.T) {
	host := &testHost{caps: capability.New(capability.Journald), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/services", nil),
		httptest.NewRequest(http.MethodGet, "/services/backup.timer/logs", nil),
		httptest.NewRequest(http.MethodPost, "/services/backup.timer/restart", nil),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code, "%s %s", request.Method, request.URL.Path)
	}
}

// TestLogsRouteGatesOnJournaldWhileServicesAndActionsStillWork proves the
// journal sub-gate is additive: with systemd present but journald absent,
// GET /services/{unit}/logs 404s but GET /services and the unit-action
// POST route keep working exactly as when fully capable.
func TestLogsRouteGatesOnJournaldWhileServicesAndActionsStillWork(t *testing.T) {
	host := &testHost{caps: capability.New(capability.Systemd), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/services", nil))
	assert.Equal(t, http.StatusOK, response.Code)

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/services/backup.timer/logs", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/services/backup.timer/restart", nil))
	assert.Equal(t, broker.ActionServicesRestart, host.action)
	assert.Equal(t, map[string]string{"unit": "backup.timer"}, host.parameters)
	assert.Equal(t, http.StatusSeeOther, response.Code)
}

// TestServicesPageLogsLinkFollowsJournaldCapability drives the real GET
// /services handler end-to-end and inspects the rendered page body,
// proving module.go actually threads host.Capabilities' journald bit into
// Page's journalAvailable argument rather than hardcoding it.
func TestServicesPageLogsLinkFollowsJournaldCapability(t *testing.T) {
	unit := Unit{Name: "backup.timer", ActiveState: "active"}
	for _, test := range []struct {
		name     string
		caps     capability.Set
		wantLogs bool
	}{
		{name: "journald present", caps: capability.New(capability.Systemd, capability.Journald), wantLogs: true},
		{name: "journald absent", caps: capability.New(capability.Systemd), wantLogs: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &testHost{caps: test.caps, capsSet: true, state: State{Units: []Unit{unit}}}
			mux := http.NewServeMux()
			New().Mount(mux, host)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/services", nil))
			require.Equal(t, http.StatusOK, response.Code)

			var output strings.Builder
			require.NoError(t, host.page.Body.Render(context.Background(), &output))
			html := output.String()
			if test.wantLogs {
				assert.Contains(t, html, "/services/backup.timer/logs")
			} else {
				assert.NotContains(t, html, "/services/backup.timer/logs")
				assert.NotContains(t, html, ">Logs<")
			}
		})
	}
}
