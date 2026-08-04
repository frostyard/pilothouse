package incus

import (
	"context"
	"errors"
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

// fullCapabilities matches c1's default: every capability present, so
// existing tests that don't care about gating keep exercising the
// full-capability path unchanged.
var fullCapabilities = capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)

type fakeHost struct {
	actionID         string
	actionParameters map[string]string
	queryError       error
	queryID          string
	queryParameters  map[string]string
	// caps overrides Capabilities' return value when capsSet is true.
	// Leaving both zero (the default for a bare &fakeHost{}) falls back to
	// fullCapabilities, so existing tests that never touch capability
	// gating keep exercising the full-capability path unchanged; tests
	// that need to exercise gating set both caps and capsSet explicitly,
	// including to an intentionally empty capability.Set{}.
	caps    capability.Set
	capsSet bool
}

func (h *fakeHost) Capabilities(context.Context) capability.Set {
	if !h.capsSet {
		return fullCapabilities
	}
	return h.caps
}

func (*fakeHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }

func (host *fakeHost) CSRFToken(*http.Request) string { return "token" }

func (host *fakeHost) Execute(_ context.Context, _ *http.Request, action string, parameters map[string]string) error {
	host.actionID = action
	host.actionParameters = parameters
	return nil
}

func TestImageActionDispatchAndUnknown(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	form := url.Values{"project": {"production"}}
	request := httptest.NewRequest(http.MethodPost, "/incus/images/fingerprint/remove", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, broker.ActionIncusRemoveImage, host.actionID)
	assert.Equal(t, map[string]string{"fingerprint": "fingerprint", "project": "production"}, host.actionParameters)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/incus/images/fingerprint/unknown", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func (host *fakeHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: true} }

func (host *fakeHost) Query(_ context.Context, id string, parameters map[string]string, target any) error {
	host.queryID = id
	host.queryParameters = parameters
	if host.queryError != nil {
		return host.queryError
	}
	switch id {
	case broker.QueryIncusState:
		*target.(*State) = State{Project: parameters["project"], Projects: []Project{{Name: parameters["project"]}}}
	case broker.QueryIncusInstance:
		*target.(*Detail) = Detail{
			Instance: Instance{Name: parameters["name"], Running: true, Type: "Container"},
			Project:  parameters["project"],
		}
	case broker.QueryIncusLogs:
		*target.(*Logs) = Logs{
			Lines: []LogLine{{Message: "canned log line"}}, Name: parameters["name"],
			Project: parameters["project"], Source: parameters["source"],
		}
	}
	return nil
}

func TestModuleRedirectsUnavailableProject(t *testing.T) {
	host := &fakeHost{queryError: errors.New("broker: project is not available")}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	request := httptest.NewRequest(http.MethodGet, "/incus?project=removed", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.NotContains(t, response.Header().Get("Location"), "project=removed")
}

func (host *fakeHost) Render(w http.ResponseWriter, _ *http.Request, page platform.Page) error {
	return page.Body.Render(context.Background(), w)
}

func (host *fakeHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }
func (*fakeHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (*fakeHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (*fakeHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

func TestModulePropagatesSelectedProject(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	request := httptest.NewRequest(http.MethodGet, "/incus?project=production", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "production", host.queryParameters["project"])

	form := url.Values{"project": {"production"}}
	request = httptest.NewRequest(http.MethodPost, "/incus/instances/api/start", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	require.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, map[string]string{"name": "api", "project": "production"}, host.actionParameters)
	assert.Contains(t, response.Header().Get("Location"), "project=production")
}

func TestRequiredCapabilitiesIsIncusOnly(t *testing.T) {
	assert.Equal(t, []capability.ID{capability.Incus}, New().RequiredCapabilities())
}

// TestModuleAvailabilityGatedOnIncus exercises platform.Available (c2's
// real production gating predicate, not a reimplementation of it) against
// this module's RequiredCapabilities, proving the whole module — nav entry
// and dashboard card, per c2's mechanism — is excluded whenever incus is
// absent, regardless of what else is present.
func TestModuleAvailabilityGatedOnIncus(t *testing.T) {
	module := New()
	assert.True(t, platform.Available(module, capability.New(capability.Incus)))
	assert.True(t, platform.Available(module, capability.New(capability.Incus, capability.Docker)))
	assert.False(t, platform.Available(module, capability.New(capability.Docker)))
	assert.False(t, platform.Available(module, capability.Set{}))
}

// TestRoutesGateOnIncusAbsent proves — via a real ServeMux round trip
// through Mount, not a test-only stand-in — that every route this module
// registers 404s once incus is absent, even when other engines are
// present.
func TestRoutesGateOnIncusAbsent(t *testing.T) {
	host := &fakeHost{caps: capability.New(capability.Docker, capability.Podman), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/incus", nil),
		httptest.NewRequest(http.MethodGet, "/incus/instances/api", nil),
		httptest.NewRequest(http.MethodGet, "/incus/instances/api/logs", nil),
		httptest.NewRequest(http.MethodPost, "/incus/instances/api/start", nil),
		httptest.NewRequest(http.MethodPost, "/incus/images/fingerprint/remove", nil),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code, "%s %s", request.Method, request.URL.Path)
	}
}

// TestUnrelatedRoutesUnaffectedWhenIncusAbsent proves gating incus does not
// disturb the rest of the app: with incus missing, other routes (mounted on
// the same mux) keep working.
func TestUnrelatedRoutesUnaffectedWhenIncusAbsent(t *testing.T) {
	host := &fakeHost{caps: capability.New(capability.Docker), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	mux.HandleFunc("GET /unrelated", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/unrelated", nil))
	assert.Equal(t, http.StatusOK, response.Code)
}

// TestRoutesWorkWhenIncusPresent proves behavior is unchanged from before
// this chunk when incus is present: routes still succeed and dispatch as
// before, exercised through the real ServeMux.
func TestRoutesWorkWhenIncusPresent(t *testing.T) {
	host := &fakeHost{caps: capability.New(capability.Incus), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incus", nil))
	assert.Equal(t, http.StatusOK, response.Code)

	form := url.Values{"project": {"production"}}
	request := httptest.NewRequest(http.MethodPost, "/incus/instances/api/start", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, map[string]string{"name": "api", "project": "production"}, host.actionParameters)
}

// TestDetailRouteQueriesInstance proves the detail route reaches the broker
// through the fixed QueryIncusInstance ID carrying both the path's instance
// name and the request's selected project, and renders the returned model.
func TestDetailRouteQueriesInstance(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incus/instances/api?project=production", nil))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryIncusInstance, host.queryID)
	assert.Equal(t, map[string]string{"name": "api", "project": "production"}, host.queryParameters)
	assert.Contains(t, response.Body.String(), "api")
}

func TestDetailRouteReportsBrokerFailure(t *testing.T) {
	host := &fakeHost{queryError: errors.New("broker: unavailable")}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incus/instances/api?project=production", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

// TestLogsRouteDefaultsToConsole pins the default source: arriving with no
// source parameter reads the console log, which is what an operator wants
// to see first.
func TestLogsRouteDefaultsToConsole(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incus/instances/api/logs?project=production", nil))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryIncusLogs, host.queryID)
	assert.Equal(t, map[string]string{"name": "api", "project": "production", "source": SourceConsole}, host.queryParameters)
	assert.Contains(t, response.Body.String(), "canned log line")
}

func TestLogsRoutePassesSupervisorSource(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incus/instances/api/logs?project=production&source=log", nil))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, SourceLog, host.queryParameters["source"])
}

// TestLogsRouteRejectsUnknownSource proves the web side refuses an
// unsupported source itself, without issuing a broker query at all, so a
// crafted source never reaches the daemon.
func TestLogsRouteRejectsUnknownSource(t *testing.T) {
	for _, source := range []string{"lxc.log", "../../etc/passwd", "default", "LOG"} {
		host := &fakeHost{}
		mux := http.NewServeMux()
		New().Mount(mux, host)

		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incus/instances/api/logs?source="+url.QueryEscape(source), nil))

		assert.Equal(t, http.StatusNotFound, response.Code, "source %q", source)
		assert.Empty(t, host.queryID, "source %q must be rejected before any broker query", source)
	}
}

// TestLogsRouteDegradesOnBrokerFailure proves an unreadable log renders the
// page in its unavailable state rather than failing the request, matching
// how the other engine modules treat container diagnostics.
func TestLogsRouteDegradesOnBrokerFailure(t *testing.T) {
	host := &fakeHost{queryError: errors.New("broker: unavailable")}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incus/instances/api/logs?project=production", nil))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "could not be read")
}

// TestSnapshotCreateRouteSubmitsFormName proves the create route passes the
// form's snapshot name through as the fixed action's parameter and lands
// back on the instance's own page.
func TestSnapshotCreateRouteSubmitsFormName(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	form := url.Values{"project": {"production"}, "snapshot": {"  before-patch  "}}
	request := httptest.NewRequest(http.MethodPost, "/incus/instances/api/snapshots", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	require.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, broker.ActionIncusSnapshotCreate, host.actionID)
	assert.Equal(t, map[string]string{"instance": "api", "project": "production", "snapshot": "before-patch"},
		host.actionParameters, "surrounding whitespace is trimmed before submission")
	assert.Contains(t, response.Header().Get("Location"), "/incus/instances/api?")
	assert.Contains(t, response.Header().Get("Location"), "project=production")
}

func TestSnapshotActionRouteDispatchAndUnknown(t *testing.T) {
	for action, id := range map[string]string{
		"restore": broker.ActionIncusSnapshotRestore,
		"delete":  broker.ActionIncusSnapshotDelete,
	} {
		host := &fakeHost{}
		mux := http.NewServeMux()
		New().Mount(mux, host)

		form := url.Values{"project": {"production"}}
		request := httptest.NewRequest(http.MethodPost, "/incus/instances/api/snapshots/nightly/"+action, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)

		require.Equal(t, http.StatusSeeOther, response.Code, action)
		assert.Equal(t, id, host.actionID, action)
		assert.Equal(t, map[string]string{"instance": "api", "project": "production", "snapshot": "nightly"}, host.actionParameters, action)
		assert.Contains(t, response.Header().Get("Location"), "/incus/instances/api?", action)
	}

	// An unknown snapshot action is a 404 that dispatches nothing.
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/incus/instances/api/snapshots/nightly/purge", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Empty(t, host.actionID)
}

// TestStopForceRouteDispatchesItsOwnAction proves force stop is a distinct
// broker ID rather than a parameter on the graceful stop.
func TestStopForceRouteDispatchesItsOwnAction(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	form := url.Values{"project": {"production"}}
	request := httptest.NewRequest(http.MethodPost, "/incus/instances/api/stop-force", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	require.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, broker.ActionIncusStopForce, host.actionID)
	assert.NotEqual(t, broker.ActionIncusStop, host.actionID)
}

// TestInstanceActionMessagesReadAsEnglish pins the per-action wording,
// which used to be derived from the action word and produced "Instance
// startd" / "Instance stopd".
func TestInstanceActionMessagesReadAsEnglish(t *testing.T) {
	for action, want := range map[string]string{
		"start":      "Instance+started",
		"stop":       "Instance+stopped",
		"restart":    "Instance+restarted",
		"remove":     "Instance+removed",
		"stop-force": "Instance+force+stopped",
	} {
		host := &fakeHost{}
		mux := http.NewServeMux()
		New().Mount(mux, host)

		form := url.Values{"project": {"production"}}
		request := httptest.NewRequest(http.MethodPost, "/incus/instances/api/"+action, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)

		assert.Contains(t, response.Header().Get("Location"), want, action)
	}
}
