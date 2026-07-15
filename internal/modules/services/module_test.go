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
)

type testHost struct {
	action     string
	parameters map[string]string
}

func (*testHost) CSRFToken(*http.Request) string { return "token" }
func (h *testHost) Execute(_ context.Context, _ *http.Request, action string, parameters map[string]string) error {
	h.action, h.parameters = action, parameters
	return nil
}
func (*testHost) Identity(*http.Request) auth.Identity                           { return auth.Identity{Admin: true} }
func (*testHost) Query(context.Context, string, map[string]string, any) error    { return nil }
func (*testHost) Render(http.ResponseWriter, *http.Request, platform.Page) error { return nil }
func (*testHost) ValidateAction(http.ResponseWriter, *http.Request) bool         { return true }

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
