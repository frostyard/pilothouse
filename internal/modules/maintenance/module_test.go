package maintenance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
)

type moduleHost struct {
	action       string
	confirmation string
	state        State
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
