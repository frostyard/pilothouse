package maintenance

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
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
func (h *moduleHost) Query(_ context.Context, _ string, _ map[string]string, target any) error {
	*target.(*State) = h.state
	return nil
}
func (*moduleHost) Render(http.ResponseWriter, *http.Request, platform.Page) error { return nil }
func (*moduleHost) ValidateAction(http.ResponseWriter, *http.Request) bool         { return true }
func (*moduleHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (*moduleHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (*moduleHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
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

func TestRequiredCapabilitiesIsSystemdOnly(t *testing.T) {
	assert.Equal(t, []capability.ID{capability.Systemd}, New().RequiredCapabilities())
}

// TestModuleAvailabilityGatedOnSystemd exercises platform.Available (the
// registry's real module-availability predicate, not a reimplementation of
// it) against this module's RequiredCapabilities, proving the whole module
// — nav entry and dashboard card — is excluded whenever systemd is absent,
// regardless of what else is present.
func TestModuleAvailabilityGatedOnSystemd(t *testing.T) {
	module := New()
	assert.True(t, platform.Available(module, capability.New(capability.Systemd)))
	assert.True(t, platform.Available(module, capability.New(capability.Systemd, capability.Journald)))
	assert.False(t, platform.Available(module, capability.New(capability.Journald)))
	assert.False(t, platform.Available(module, capability.Set{}))
}

// TestRoutesGateOnSystemdAbsent proves — via a real ServeMux round trip
// through Mount, not a test-only stand-in — that both GET /maintenance and
// POST /maintenance/reboot 404 once systemd is absent, even when other
// capabilities (journald here) are present.
func TestRoutesGateOnSystemdAbsent(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.Journald), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/maintenance", nil),
		httptest.NewRequest(http.MethodPost, "/maintenance/reboot", nil),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code, "%s %s", request.Method, request.URL.Path)
	}
}

// TestRoutesServeWhenSystemdPresent proves the gate is additive, not a
// regression: with systemd present (fullTestCapabilities, the default),
// both routes behave exactly as they did before this chunk.
func TestRoutesServeWhenSystemdPresent(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
	assert.Equal(t, http.StatusOK, response.Code)

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/maintenance/reboot", nil))
	assert.Equal(t, "maintenance/reboot", host.confirmation)
	assert.Equal(t, broker.ActionMaintenanceReboot, host.action)
}
