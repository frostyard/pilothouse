package docker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
)

// fullCapabilities matches c1's default: every capability present, so
// existing tests that don't care about gating keep exercising the
// full-capability path unchanged.
var fullCapabilities = capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)

type moduleHost struct {
	action          string
	parameters      map[string]string
	query           string
	queryParameters map[string]string
	page            platform.Page
	// caps overrides Capabilities' return value when capsSet is true.
	// Leaving both zero (the default for a bare &moduleHost{}) falls back
	// to fullCapabilities, so existing tests that never touch capability
	// gating keep exercising the full-capability path unchanged; tests
	// that need to exercise gating set both caps and capsSet explicitly,
	// including to an intentionally empty capability.Set{}.
	caps    capability.Set
	capsSet bool
}

func (h *moduleHost) Capabilities(context.Context) capability.Set {
	if !h.capsSet {
		return fullCapabilities
	}
	return h.caps
}

func (*moduleHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return true
}

func (*moduleHost) CSRFToken(*http.Request) string { return "token" }
func (h *moduleHost) Execute(_ context.Context, _ *http.Request, action string, parameters map[string]string) error {
	h.action, h.parameters = action, parameters
	return nil
}
func (*moduleHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: true} }
func (h *moduleHost) Query(_ context.Context, query string, parameters map[string]string, result any) error {
	h.query, h.queryParameters = query, parameters
	if logs, ok := result.(*Logs); ok {
		*logs = Logs{ID: parameters["id"], Name: "api"}
	}
	return nil
}
func (h *moduleHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
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

func TestImageActionDispatch(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/docker/images/image/remove", nil))
	assert.Equal(t, broker.ActionDockerRemoveImage, host.action)
	assert.Equal(t, map[string]string{"id": "image"}, host.parameters)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/docker/images/image/unknown", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestLogsRouteQueriesBroker(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/docker/containers/"+runningID+"/logs", nil))
	assert.Equal(t, broker.QueryDockerLogs, host.query)
	assert.Equal(t, map[string]string{"id": runningID}, host.queryParameters)
	assert.Equal(t, "api logs", host.page.Title)

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/docker/containers/invalid/logs", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestRequiredCapabilitiesIsDockerOnly(t *testing.T) {
	assert.Equal(t, []capability.ID{capability.Docker}, New().RequiredCapabilities())
}

// TestModuleAvailabilityGatedOnDocker exercises platform.Available (c2's
// real production gating predicate, not a reimplementation of it) against
// this module's RequiredCapabilities, proving the whole module — nav entry
// and dashboard card, per c2's mechanism — is excluded whenever docker is
// absent, regardless of what else is present.
func TestModuleAvailabilityGatedOnDocker(t *testing.T) {
	module := New()
	assert.True(t, platform.Available(module, capability.New(capability.Docker)))
	assert.True(t, platform.Available(module, capability.New(capability.Docker, capability.Podman)))
	assert.False(t, platform.Available(module, capability.New(capability.Podman)))
	assert.False(t, platform.Available(module, capability.Set{}))
}

// TestRoutesGateOnDockerAbsent proves — via a real ServeMux round trip
// through Mount, not a test-only stand-in — that every route this module
// registers 404s once docker is absent, even when the other engine
// (podman) is present.
func TestRoutesGateOnDockerAbsent(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.Podman), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/docker", nil),
		httptest.NewRequest(http.MethodGet, "/docker/containers/"+runningID+"/logs", nil),
		httptest.NewRequest(http.MethodPost, "/docker/containers/"+runningID+"/restart", nil),
		httptest.NewRequest(http.MethodPost, "/docker/images/image/remove", nil),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code, "%s %s", request.Method, request.URL.Path)
	}
}

// TestPodmanRoutesUnaffectedWhenDockerAbsent proves gating docker does not
// disturb the sibling podman module or the rest of the app: with only
// docker missing, other routes (mounted on the same mux) keep working.
func TestPodmanRoutesUnaffectedWhenDockerAbsent(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.Podman), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	mux.HandleFunc("GET /unrelated", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/unrelated", nil))
	assert.Equal(t, http.StatusOK, response.Code)
}

// TestRoutesWorkWhenDockerPresent proves behavior is unchanged from before
// this chunk when docker is present: routes still succeed and dispatch as
// before, exercised through the real ServeMux.
func TestRoutesWorkWhenDockerPresent(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.Docker), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/docker/containers/"+runningID+"/logs", nil))
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryDockerLogs, host.query)

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/docker/containers/"+runningID+"/restart", nil))
	assert.Equal(t, broker.ActionDockerRestart, host.action)
}
