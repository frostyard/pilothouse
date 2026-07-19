package podman

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

type moduleHost struct {
	action     string
	parameters map[string]string
	query      string
	page       platform.Page
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
	h.query, h.parameters = query, parameters
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

func TestImageActionDispatch(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/podman/images/image/remove", nil))
	assert.Equal(t, broker.ActionPodmanRemoveImage, host.action)
	assert.Equal(t, map[string]string{"id": "image"}, host.parameters)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/podman/images/image/unknown", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestLogsRouteQueriesBroker(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/podman/containers/"+runningID+"/logs", nil))
	assert.Equal(t, broker.QueryPodmanLogs, host.query)
	assert.Equal(t, map[string]string{"id": runningID}, host.parameters)
	assert.Equal(t, "api logs", host.page.Title)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/podman/containers/invalid/logs", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}
