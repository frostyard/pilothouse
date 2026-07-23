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
	host.queryParameters = parameters
	if host.queryError != nil {
		return host.queryError
	}
	if id == broker.QueryIncusState {
		*target.(*State) = State{Project: parameters["project"], Projects: []Project{{Name: parameters["project"]}}}
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
