package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type moduleHost struct {
	page platform.Page
}

func (*moduleHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return true
}
func (*moduleHost) CSRFToken(*http.Request) string { return "" }
func (*moduleHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (*moduleHost) Identity(*http.Request) auth.Identity { return auth.Identity{} }
func (*moduleHost) Query(context.Context, string, map[string]string, any) error {
	return nil
}
func (host *moduleHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	host.page = page
	return nil
}
func (*moduleHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }

func TestFleetRoutesRenderPreviewPages(t *testing.T) {
	tests := []struct {
		path   string
		title  string
		active string
	}{
		{path: "/fleet", title: "Systems", active: "fleet"},
		{path: "/fleet/enroll", title: "Connect a system", active: "fleet"},
		{path: "/fleet/systems/cayo-01", title: "cayo-01", active: "fleet"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			host := &moduleHost{}
			mux := http.NewServeMux()
			New().Mount(mux, host)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))

			require.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, test.title, host.page.Title)
			assert.Equal(t, test.active, host.page.Active)
			assert.NotNil(t, host.page.Body)
		})
	}
}

func TestUnknownFleetSystemReturnsNotFound(t *testing.T) {
	host := &moduleHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fleet/systems/missing", nil))

	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Nil(t, host.page.Body)
}
