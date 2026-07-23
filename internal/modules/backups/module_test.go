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

type moduleHost struct {
	page       platform.Page
	parameters map[string]string
	query      string
	state      State
}

func (*moduleHost) Capabilities(context.Context) capability.Set {
	return capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)
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
