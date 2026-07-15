package incus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHost struct {
	actionID         string
	actionParameters map[string]string
	queryError       error
	queryParameters  map[string]string
}

func (host *fakeHost) CSRFToken(*http.Request) string { return "token" }

func (host *fakeHost) Execute(_ context.Context, _ *http.Request, action string, parameters map[string]string) error {
	host.actionID = action
	host.actionParameters = parameters
	return nil
}

func TestImageActionDispatchAndUnknown(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	form := url.Values{"project": {"production"}}
	request := httptest.NewRequest(http.MethodPost, "/incus/images/fingerprint/remove", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, broker.ActionIncusRemoveImage, host.actionID)
	assert.Equal(t, map[string]string{"fingerprint": "fingerprint", "project": "production"}, host.actionParameters)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/incus/images/fingerprint/unknown", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func (host *fakeHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: true} }

func (host *fakeHost) Query(_ context.Context, id string, parameters map[string]string, target any) error {
	host.queryParameters = parameters
	if host.queryError != nil {
		return host.queryError
	}
	if id == broker.QueryIncusState {
		*target.(*State) = State{Project: parameters["project"], Projects: []Project{{Name: parameters["project"]}}}
	}
	return nil
}

func TestModuleRedirectsUnavailableProject(t *testing.T) {
	host := &fakeHost{queryError: errors.New("broker: project is not available")}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	request := httptest.NewRequest(http.MethodGet, "/incus?project=removed", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.NotContains(t, response.Header().Get("Location"), "project=removed")
}

func (host *fakeHost) Render(w http.ResponseWriter, _ *http.Request, page platform.Page) error {
	return page.Body.Render(context.Background(), w)
}

func (host *fakeHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }

func TestModulePropagatesSelectedProject(t *testing.T) {
	host := &fakeHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	request := httptest.NewRequest(http.MethodGet, "/incus?project=production", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "production", host.queryParameters["project"])

	form := url.Values{"project": {"production"}}
	request = httptest.NewRequest(http.MethodPost, "/incus/instances/api/start", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	require.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, map[string]string{"name": "api", "project": "production"}, host.actionParameters)
	assert.Contains(t, response.Header().Get("Location"), "project=production")
}
