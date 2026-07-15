package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBroker struct {
	healthErr error
	session   broker.SessionResponse
}

func (b *fakeBroker) Action(context.Context, string, string, map[string]string) error { return nil }
func (b *fakeBroker) Health(context.Context) error                                    { return b.healthErr }
func (b *fakeBroker) Login(context.Context, string, string, string) (broker.LoginResponse, error) {
	return broker.LoginResponse{Session: b.session, Token: "token"}, nil
}
func (b *fakeBroker) Logout(context.Context, string) error                                { return nil }
func (b *fakeBroker) Query(context.Context, string, string, map[string]string, any) error { return nil }
func (b *fakeBroker) Session(context.Context, string) (broker.SessionResponse, error) {
	return b.session, nil
}

func newTestServer(t *testing.T) *Server {
	registry, err := platform.NewRegistry()
	require.NoError(t, err)
	server, err := NewServer(registry, &fakeBroker{session: broker.SessionResponse{CSRF: "csrf", Identity: auth.Identity{UID: 1000, Username: "snow"}}}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	return server
}

func TestServerHealthAndSecurityHeaders(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest("GET", "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	assert.Equal(t, 200, response.Code)
	assert.Equal(t, "ok\n", response.Body.String())
	assert.Contains(t, response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
}

func TestServerReadinessRequiresBroker(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest("GET", "/readyz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "ready\n", response.Body.String())

	server.broker = &fakeBroker{healthErr: broker.ErrUnavailable}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestProtectedPageRedirectsToLogin(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, 303, response.Code)
	assert.Equal(t, "/login", response.Header().Get("Location"))
}

func TestLoginRejectsMissingCSRF(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest("POST", "/login", strings.NewReader("username=snow&password=secret"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, 403, response.Code)
}

func TestAuthenticatedPageRendersSystemIdentity(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, 200, response.Code)
	assert.Contains(t, response.Body.String(), "snow")
	assert.Contains(t, response.Body.String(), "Read-only access")
}

func TestValidateActionRejectsMissingTokenAndForeignOrigin(t *testing.T) {
	server := newTestServer(t)
	missingRequest := httptest.NewRequest("POST", "/action", strings.NewReader("csrf=csrf"))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingResponse := httptest.NewRecorder()
	assert.False(t, server.ValidateAction(missingResponse, missingRequest))
	assert.Equal(t, 401, missingResponse.Code)

	foreignRequest := httptest.NewRequest("POST", "/action", strings.NewReader("csrf=csrf"))
	foreignRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	foreignRequest.Header.Set("Origin", "https://evil.example")
	foreignRequest = foreignRequest.WithContext(context.WithValue(foreignRequest.Context(), sessionContextKey{}, requestSession{
		data:  broker.SessionResponse{CSRF: "csrf"},
		token: "token",
	}))
	foreignResponse := httptest.NewRecorder()
	assert.False(t, server.ValidateAction(foreignResponse, foreignRequest))
	assert.Equal(t, 403, foreignResponse.Code)
}

func TestConfiguredPublicOriginAllowsReverseProxyActions(t *testing.T) {
	registry, err := platform.NewRegistry()
	require.NoError(t, err)
	server, err := NewServer(
		registry,
		&fakeBroker{session: broker.SessionResponse{CSRF: "csrf"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		false,
		"https://Admin.Example.test:443/",
	)
	require.NoError(t, err)

	request := httptest.NewRequest("POST", "http://127.0.0.1:8888/action", strings.NewReader("csrf=csrf"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://admin.example.test")
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, requestSession{
		data:  broker.SessionResponse{CSRF: "csrf"},
		token: "token",
	}))
	response := httptest.NewRecorder()

	assert.True(t, server.ValidateAction(response, request))
	assert.True(t, server.secureCookie)
}

func TestBrowserSameOriginMetadataAvoidsHostComparisonFalsePositive(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest("POST", "http://127.0.0.1:8888/action", strings.NewReader("csrf=csrf"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "null")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, requestSession{
		data:  broker.SessionResponse{CSRF: "csrf"},
		token: "token",
	}))
	response := httptest.NewRecorder()

	assert.True(t, server.ValidateAction(response, request))
}

func TestInvalidPublicOriginIsRejectedAtStartup(t *testing.T) {
	registry, err := platform.NewRegistry()
	require.NoError(t, err)
	_, err = NewServer(registry, &fakeBroker{}, slog.New(slog.NewTextHandler(io.Discard, nil)), false, "https://admin.example.test/pilothouse")
	assert.ErrorContains(t, err, "must not contain a path")
}
