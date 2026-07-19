package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testHost struct {
	action     string
	parameters map[string]string
	query      string
	page       platform.Page
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
	return nil
}
func (h *testHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
	return nil
}
func (*testHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }

func TestUnitActionDispatch(t *testing.T) {
	host := &testHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/services/backup.timer/restart", nil))
	assert.Equal(t, broker.ActionServicesRestart, host.action)
	assert.Equal(t, map[string]string{"unit": "backup.timer"}, host.parameters)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/services/backup.scope/start", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
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
