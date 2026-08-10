package k3s

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

type moduleHost struct {
	caps  capability.Set
	page  platform.Page
	query string
	state State
}

func (host *moduleHost) Capabilities(context.Context) capability.Set { return host.caps }
func (*moduleHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return true
}
func (*moduleHost) CSRFToken(*http.Request) string { return "token" }
func (*moduleHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (*moduleHost) Identity(*http.Request) auth.Identity { return auth.Identity{} }
func (host *moduleHost) Query(_ context.Context, query string, _ map[string]string, target any) error {
	host.query = query
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

func TestRequiredCapabilitiesIsK3sOnly(t *testing.T) {
	assert.Equal(t, []capability.ID{capability.K3s}, New().RequiredCapabilities())
}

func TestModuleAvailabilityGatedOnK3s(t *testing.T) {
	module := New()
	assert.True(t, platform.Available(module, capability.New(capability.K3s)))
	assert.False(t, platform.Available(module, capability.New(capability.Systemd)))
	assert.False(t, platform.Available(module, capability.Set{}))
}

func TestRouteQueriesFixedBrokerID(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.K3s)}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/k3s", nil))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryK3sState, host.query)
	assert.Equal(t, "k3s", host.page.Active)
}

func TestRouteGatedWhenK3sAbsent(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.Systemd)}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/k3s", nil))

	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Empty(t, host.query)
}

func TestDashboardQueriesFixedBrokerID(t *testing.T) {
	host := &moduleHost{caps: capability.New(capability.K3s)}
	cards, err := New().Dashboard(context.Background(), host)
	assert.NoError(t, err)
	assert.Equal(t, broker.QueryK3sState, host.query)
	assert.Len(t, cards, 1)
}
