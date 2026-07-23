package backups

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
	"github.com/stretchr/testify/require"
)

// fullTestCapabilities matches the default fake Host capability set used
// across this repo's module tests: every capability present, so existing
// tests that don't care about gating keep exercising the full-capability
// path unchanged.
var fullTestCapabilities = capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)

type moduleHost struct {
	page       platform.Page
	parameters map[string]string
	query      string
	state      State
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
func (*moduleHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return true
}
func (*moduleHost) CSRFToken(*http.Request) string { return "" }
func (*moduleHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (*moduleHost) Identity(*http.Request) auth.Identity { return auth.Identity{} }
func (host *moduleHost) Query(_ context.Context, query string, parameters map[string]string, target any) error {
	host.query = query
	host.parameters = parameters
	*target.(*State) = host.state
	return nil
}
func (host *moduleHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	host.page = page
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

func TestModulePageUsesFixedBrokerQuery(t *testing.T) {
	host := &moduleHost{state: State{Configured: true, Timers: []Timer{{Name: "nightly.timer", Health: HealthHealthy}}}}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backups", nil))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryBackupsState, host.query)
	assert.Nil(t, host.parameters)
	assert.Equal(t, "backups", host.page.Active)
	assert.Equal(t, "Backups", host.page.Title)
}

func TestModuleDashboardAndHealth(t *testing.T) {
	host := &moduleHost{state: State{Configured: true, Timers: []Timer{
		{Name: "healthy.timer", Health: HealthHealthy},
		{Name: "late.timer", Health: HealthWarning, Detail: "Backup has never run."},
		{Name: "failed.timer", Health: HealthCritical, Detail: "Last backup failed."},
		{Name: "unknown.timer", Health: HealthUnknown, Detail: "Status unavailable."},
	}}}
	module := New()

	cards, err := module.Dashboard(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, platform.SpanHalf, cards[0].Span)

	findings, err := module.Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 3)
	assert.Equal(t, []platform.Severity{platform.SeverityWarning, platform.SeverityCritical, platform.SeverityUnknown}, []platform.Severity{findings[0].Severity, findings[1].Severity, findings[2].Severity})
	for _, finding := range findings {
		assert.Equal(t, "/backups", finding.Path)
		assert.Equal(t, "Backups", finding.Source)
	}
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

// TestBackupsRouteGatesOnSystemdAbsent proves — via a real ServeMux round
// trip through Mount, not a test-only stand-in — that GET /backups 404s
// once systemd is absent, even when other capabilities are present.
func TestBackupsRouteGatesOnSystemdAbsent(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.Journald), capsSet: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backups", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

// TestBackupsRouteServesWhenSystemdPresent proves the gate is additive,
// not a regression: with systemd present (fullTestCapabilities, the
// default), GET /backups behaves exactly as it did before this chunk.
func TestBackupsRouteServesWhenSystemdPresent(t *testing.T) {
	host := &moduleHost{state: State{Configured: true}}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backups", nil))
	assert.Equal(t, http.StatusOK, response.Code)
}
