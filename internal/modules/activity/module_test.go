package activity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type activityHost struct {
	admin      bool
	queryID    string
	parameters map[string]string
}

func (*activityHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return true
}
func (*activityHost) CSRFToken(*http.Request) string { return "csrf" }
func (*activityHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (h *activityHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: h.admin} }
func (h *activityHost) Query(_ context.Context, id string, parameters map[string]string, target any) error {
	h.queryID, h.parameters = id, parameters
	records := target.(*[]audit.Record)
	*records = []audit.Record{{ID: 1, Action: broker.ActionServicesStop, Resource: "services/unit/demo.service", Username: "admin", UID: 1000, StartedAt: time.Now(), Outcome: audit.OutcomeSucceeded}}
	return nil
}
func (*activityHost) Render(w http.ResponseWriter, r *http.Request, page platform.Page) error {
	return page.Body.Render(r.Context(), w)
}
func (*activityHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }

func TestActivityPageUsesAdminOnlyFixedQuery(t *testing.T) {
	host := &activityHost{admin: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/activity?outcome=succeeded", nil))
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryActivity, host.queryID)
	assert.Equal(t, map[string]string{"limit": "100", "outcome": "succeeded"}, host.parameters)
	assert.Contains(t, response.Body.String(), "services/unit/demo.service")
	assert.NotContains(t, response.Body.String(), "@web.")
}

func TestActivityPageDoesNotQueryForReadOnlyUser(t *testing.T) {
	host := &activityHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/activity", nil))
	assert.Empty(t, host.queryID)
	assert.Contains(t, response.Body.String(), "Administrator access required")
}
