package docker

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
}

func (*moduleHost) CSRFToken(*http.Request) string { return "token" }
func (h *moduleHost) Execute(_ context.Context, _ *http.Request, action string, parameters map[string]string) error {
	h.action, h.parameters = action, parameters
	return nil
}
func (*moduleHost) Identity(*http.Request) auth.Identity                           { return auth.Identity{Admin: true} }
func (*moduleHost) Query(context.Context, string, map[string]string, any) error    { return nil }
func (*moduleHost) Render(http.ResponseWriter, *http.Request, platform.Page) error { return nil }
func (*moduleHost) ValidateAction(http.ResponseWriter, *http.Request) bool         { return true }

func TestImageActionDispatch(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/docker/images/image/remove", nil))
	assert.Equal(t, broker.ActionDockerRemoveImage, host.action)
	assert.Equal(t, map[string]string{"id": "image"}, host.parameters)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/docker/images/image/unknown", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}
