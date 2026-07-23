package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBroker struct {
	actionErr              error
	capabilities           capability.Set
	confirmation           string
	healthErr              error
	queryCalls             []string
	queryErr               error
	session                broker.SessionResponse
	sessionErr             error
	streamActionBody       string
	streamActionErr        error
	streamActionID         string
	streamActionParameters map[string]string
	streamActionToken      string
	streamQueryErr         error
	streamQueryID          string
	streamQueryParameters  map[string]string
	streamQueryToken       string
}

func (b *fakeBroker) Action(_ context.Context, _, _ string, _ map[string]string, confirmation string) error {
	b.confirmation = confirmation
	return b.actionErr
}
func (b *fakeBroker) Health(context.Context) error { return b.healthErr }
func (b *fakeBroker) Login(context.Context, string, string, string) (broker.LoginResponse, error) {
	return broker.LoginResponse{Session: b.session, Token: "token"}, nil
}
func (b *fakeBroker) Logout(context.Context, string) error { return nil }
func (b *fakeBroker) Query(_ context.Context, _, id string, _ map[string]string, target any) error {
	b.queryCalls = append(b.queryCalls, id)
	if b.queryErr != nil {
		return b.queryErr
	}
	if id == broker.QueryCapabilities {
		encoded, err := json.Marshal(b.capabilities)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	}
	return nil
}
func (b *fakeBroker) Session(context.Context, string) (broker.SessionResponse, error) {
	if b.sessionErr != nil {
		return broker.SessionResponse{}, b.sessionErr
	}
	return b.session, nil
}
func (b *fakeBroker) StreamAction(_ context.Context, token, id string, parameters map[string]string, body io.Reader) error {
	b.streamActionToken, b.streamActionID, b.streamActionParameters = token, id, parameters
	contents, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	b.streamActionBody = string(contents)
	return b.streamActionErr
}
func (b *fakeBroker) StreamQuery(_ context.Context, token, id string, parameters map[string]string) (broker.StreamResult, error) {
	b.streamQueryToken, b.streamQueryID, b.streamQueryParameters = token, id, parameters
	return broker.StreamResult{}, b.streamQueryErr
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

func TestServerServesEmbeddedFrostyardArtwork(t *testing.T) {
	server := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/frozen-reflection.png", nil))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "image/png", response.Header().Get("Content-Type"))
	assert.NotEmpty(t, response.Body.Bytes())
}

func TestConfirmActionRendersReviewAndAcceptsExactResource(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/services/backup.timer/stop", strings.NewReader("csrf=csrf&project=default"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, requestSession{data: broker.SessionResponse{CSRF: "csrf"}, token: "token"}))
	require.NoError(t, request.ParseForm())
	response := httptest.NewRecorder()

	assert.False(t, server.ConfirmAction(response, request, "Stop backup.timer", "services/unit/backup.timer"))
	assert.Contains(t, response.Body.String(), "Stop backup.timer")
	assert.Contains(t, response.Body.String(), `name="confirmation" value="services/unit/backup.timer"`)
	assert.Contains(t, response.Body.String(), `name="project" value="default"`)
	assert.NotContains(t, response.Body.String(), "@Icon(")

	request.Form.Set("confirmation", "services/unit/backup.timer")
	assert.True(t, server.ConfirmAction(httptest.NewRecorder(), request, "Stop backup.timer", "services/unit/backup.timer"))
}

func TestExecuteForwardsConfirmation(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)
	request := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader("confirmation=resource%2Fone"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, requestSession{token: "token"}))
	require.NoError(t, request.ParseForm())
	require.NoError(t, server.Execute(context.Background(), request, "action", nil))
	assert.Equal(t, "resource/one", fake.confirmation)
}

func TestStreamActionRequiresSessionAndForwardsRequestBody(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)
	body := strings.NewReader("stream body")
	request := httptest.NewRequest(http.MethodPost, "/files/root/upload", nil)

	err := server.StreamAction(context.Background(), request, "files.upload", map[string]string{"path": "/root"}, body)
	require.ErrorIs(t, err, broker.ErrUnauthorized)

	request = withTestSession(request, "csrf", "opaque-token")
	require.NoError(t, server.StreamAction(context.Background(), request, "files.upload", map[string]string{"path": "/root"}, body))
	assert.Equal(t, "opaque-token", fake.streamActionToken)
	assert.Equal(t, "files.upload", fake.streamActionID)
	assert.Equal(t, map[string]string{"path": "/root"}, fake.streamActionParameters)
	assert.Equal(t, "stream body", fake.streamActionBody)
}

func TestStreamQueryRequiresSessionAndForwardsToken(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)

	_, err := server.StreamQuery(context.Background(), "files.download", map[string]string{"path": "/root/file"})
	require.ErrorIs(t, err, broker.ErrUnauthorized)

	ctx := context.WithValue(context.Background(), sessionContextKey{}, requestSession{token: "opaque-token"})
	_, err = server.StreamQuery(ctx, "files.download", map[string]string{"path": "/root/file"})
	require.NoError(t, err)
	assert.Equal(t, "opaque-token", fake.streamQueryToken)
	assert.Equal(t, "files.download", fake.streamQueryID)
	assert.Equal(t, map[string]string{"path": "/root/file"}, fake.streamQueryParameters)
}

func TestValidateActionTokenChecksExplicitCSRFWithoutReadingBody(t *testing.T) {
	server := newTestServer(t)
	body := &countingReader{Reader: strings.NewReader("unread")}
	request := httptest.NewRequest(http.MethodPost, "/files/root/upload", body)
	request = withTestSession(request, "csrf", "token")
	response := httptest.NewRecorder()

	assert.True(t, server.ValidateActionToken(response, request, "csrf"))
	assert.Zero(t, body.reads)
}

func TestValidateActionTokenRejectsMissingSessionWrongCSRFAndForeignOrigin(t *testing.T) {
	server := newTestServer(t)

	missingSession := httptest.NewRequest(http.MethodPost, "/files/root/upload", nil)
	missingResponse := httptest.NewRecorder()
	assert.False(t, server.ValidateActionToken(missingResponse, missingSession, "csrf"))
	assert.Equal(t, http.StatusUnauthorized, missingResponse.Code)

	wrongCSRF := withTestSession(httptest.NewRequest(http.MethodPost, "/files/root/upload", nil), "csrf", "token")
	wrongResponse := httptest.NewRecorder()
	assert.False(t, server.ValidateActionToken(wrongResponse, wrongCSRF, "wrong"))
	assert.Equal(t, http.StatusForbidden, wrongResponse.Code)

	foreignOrigin := withTestSession(httptest.NewRequest(http.MethodPost, "/files/root/upload", nil), "csrf", "token")
	foreignOrigin.Header.Set("Origin", "https://evil.example")
	foreignResponse := httptest.NewRecorder()
	assert.False(t, server.ValidateActionToken(foreignResponse, foreignOrigin, "csrf"))
	assert.Equal(t, http.StatusForbidden, foreignResponse.Code)
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

func TestCapabilitiesIsZeroSetBeforeLogin(t *testing.T) {
	server := newTestServer(t)
	assert.Empty(t, server.Capabilities(context.Background()).List())
}

func TestLoginFetchesAndCachesCapabilities(t *testing.T) {
	registry, err := platform.NewRegistry()
	require.NoError(t, err)
	caps := capability.New(capability.Systemd, capability.Docker)
	fake := &fakeBroker{session: broker.SessionResponse{CSRF: "csrf"}, capabilities: caps}
	server, err := NewServer(registry, fake, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)

	request := httptest.NewRequest("POST", "/login", strings.NewReader("csrf="+server.loginCSRF+"&username=snow&password=secret"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, []string{broker.QueryCapabilities}, fake.queryCalls)
	assert.Equal(t, caps.List(), server.Capabilities(context.Background()).List())
}

func TestAuthenticateRefetchesCapabilitiesAfterOutageRecovery(t *testing.T) {
	registry, err := platform.NewRegistry()
	require.NoError(t, err)
	initialCaps := capability.New(capability.Systemd)
	fake := &fakeBroker{session: broker.SessionResponse{CSRF: "csrf"}, capabilities: initialCaps}
	server, err := NewServer(registry, fake, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)

	server.refreshCapabilities(context.Background(), "token")
	require.Equal(t, []string{broker.QueryCapabilities}, fake.queryCalls)
	require.Equal(t, initialCaps.List(), server.Capabilities(context.Background()).List())
	fake.queryCalls = nil

	// A transport failure from Session marks the cache down but leaves the
	// previously cached Set untouched.
	fake.sessionErr = fmt.Errorf("dial unix: %w", broker.ErrUnavailable)
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.True(t, server.capabilities.staleAfterOutage())
	assert.Equal(t, initialCaps.List(), server.Capabilities(context.Background()).List())
	assert.Empty(t, fake.queryCalls)
	// A transient outage must NOT clear the session cookie: clearing it logs
	// the user out, so the recovery refetch below could never fire with the
	// same session. Only a genuine ErrUnauthorized clears the cookie.
	for _, c := range response.Result().Cookies() {
		if c.Name == sessionCookie {
			assert.Falsef(t, c.MaxAge < 0 || c.Value == "",
				"transient outage cleared the session cookie")
		}
	}

	// The next successful Session() triggers exactly one refetch and clears
	// the down flag.
	fake.sessionErr = nil
	newCaps := capability.New(capability.Docker, capability.Incus)
	fake.capabilities = newCaps
	request = httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, []string{broker.QueryCapabilities}, fake.queryCalls)
	assert.False(t, server.capabilities.staleAfterOutage())
	assert.Equal(t, newCaps.List(), server.Capabilities(context.Background()).List())
}

func TestAuthenticateDoesNotMarkCapabilitiesDownOnUnauthorized(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)
	fake.sessionErr = broker.ErrUnauthorized

	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.False(t, server.capabilities.staleAfterOutage())
}

func TestExecuteMarksCapabilitiesDownOnlyOnUnavailable(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)
	ctx := context.WithValue(context.Background(), sessionContextKey{}, requestSession{token: "token"})
	request := httptest.NewRequest("POST", "/action", nil).WithContext(ctx)

	fake.actionErr = broker.ErrUnauthorized
	err := server.Execute(ctx, request, "action", nil)
	assert.ErrorIs(t, err, broker.ErrUnauthorized)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.actionErr = errors.New("some domain error")
	err = server.Execute(ctx, request, "action", nil)
	assert.Error(t, err)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.actionErr = fmt.Errorf("dial unix: %w", broker.ErrUnavailable)
	err = server.Execute(ctx, request, "action", nil)
	assert.Error(t, err)
	assert.True(t, server.capabilities.staleAfterOutage())

	assert.Empty(t, fake.queryCalls, "Execute must never itself trigger a QueryCapabilities call")
}

func TestQueryMarksCapabilitiesDownOnlyOnUnavailableAndNeverRefetchesItself(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)
	ctx := context.WithValue(context.Background(), sessionContextKey{}, requestSession{token: "token"})
	var target any

	fake.queryErr = broker.ErrUnauthorized
	err := server.Query(ctx, "some.query", nil, &target)
	assert.ErrorIs(t, err, broker.ErrUnauthorized)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.queryErr = errors.New("some domain error")
	err = server.Query(ctx, "some.query", nil, &target)
	assert.Error(t, err)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.queryErr = fmt.Errorf("dial unix: %w", broker.ErrUnavailable)
	err = server.Query(ctx, "some.query", nil, &target)
	assert.Error(t, err)
	assert.True(t, server.capabilities.staleAfterOutage())

	assert.Equal(t, []string{"some.query", "some.query", "some.query"}, fake.queryCalls, "Query must never itself trigger a QueryCapabilities call")
}

func TestStreamActionMarksCapabilitiesDownOnlyOnUnavailable(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)
	request := withTestSession(httptest.NewRequest("POST", "/files/root/upload", nil), "csrf", "token")

	fake.streamActionErr = broker.ErrUnauthorized
	err := server.StreamAction(context.Background(), request, "id", nil, strings.NewReader(""))
	assert.ErrorIs(t, err, broker.ErrUnauthorized)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.streamActionErr = errors.New("some domain error")
	err = server.StreamAction(context.Background(), request, "id", nil, strings.NewReader(""))
	assert.Error(t, err)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.streamActionErr = fmt.Errorf("dial unix: %w", broker.ErrUnavailable)
	err = server.StreamAction(context.Background(), request, "id", nil, strings.NewReader(""))
	assert.Error(t, err)
	assert.True(t, server.capabilities.staleAfterOutage())

	assert.Empty(t, fake.queryCalls, "StreamAction must never itself trigger a QueryCapabilities call")
}

func TestStreamQueryMarksCapabilitiesDownOnlyOnUnavailable(t *testing.T) {
	server := newTestServer(t)
	fake := server.broker.(*fakeBroker)
	ctx := context.WithValue(context.Background(), sessionContextKey{}, requestSession{token: "token"})

	fake.streamQueryErr = broker.ErrUnauthorized
	_, err := server.StreamQuery(ctx, "id", nil)
	assert.ErrorIs(t, err, broker.ErrUnauthorized)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.streamQueryErr = errors.New("some domain error")
	_, err = server.StreamQuery(ctx, "id", nil)
	assert.Error(t, err)
	assert.False(t, server.capabilities.staleAfterOutage())

	fake.streamQueryErr = fmt.Errorf("dial unix: %w", broker.ErrUnavailable)
	_, err = server.StreamQuery(ctx, "id", nil)
	assert.Error(t, err)
	assert.True(t, server.capabilities.staleAfterOutage())

	assert.Empty(t, fake.queryCalls, "StreamQuery must never itself trigger a QueryCapabilities call")
}

// fakeGatedModule is a synthetic platform.Module that also implements
// platform.CapabilityGate, used to prove the nav/dashboard filtering
// mechanism added in this chunk without depending on any real module.
type fakeGatedModule struct {
	dashboardCalls int
	required       []capability.ID
}

func (m *fakeGatedModule) Dashboard(context.Context, platform.Host) ([]platform.DashboardCard, error) {
	m.dashboardCalls++
	return []platform.DashboardCard{{Component: textComponent("gated-card-marker"), Order: 1, Span: platform.SpanFull}}, nil
}

func (m *fakeGatedModule) Manifest() platform.Manifest {
	return platform.Manifest{ID: "gated", Name: "Gated Module", Order: 50, Path: "/gated"}
}

func (m *fakeGatedModule) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /gated", platform.Gate(host, m.required, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "gated-page-marker")
	}))
}

func (m *fakeGatedModule) RequiredCapabilities() []capability.ID { return m.required }

func textComponent(text string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

func newGatedTestServer(t *testing.T, module *fakeGatedModule, initialCaps capability.Set) (*Server, *fakeBroker) {
	t.Helper()
	registry, err := platform.NewRegistry(module)
	require.NoError(t, err)
	fake := &fakeBroker{session: broker.SessionResponse{CSRF: "csrf"}, capabilities: initialCaps}
	server, err := NewServer(registry, fake, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	server.refreshCapabilities(context.Background(), "token")
	return server, fake
}

func getAuthenticated(server *Server) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestDashboardOmitsCardAndSkipsDashboardCallForCapabilityGatedAbsentModule(t *testing.T) {
	module := &fakeGatedModule{required: []capability.ID{capability.Docker}}
	server, fake := newGatedTestServer(t, module, capability.New(capability.Systemd))

	response := getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "gated-card-marker")
	assert.NotContains(t, response.Body.String(), "Module unavailable")
	assert.Zero(t, module.dashboardCalls)

	fake.capabilities = capability.New(capability.Docker)
	server.refreshCapabilities(context.Background(), "token")

	response = getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "gated-card-marker")
	assert.Equal(t, 1, module.dashboardCalls)
}

func TestNavOmitsManifestForCapabilityGatedAbsentModuleAndIncludesItWhenPresent(t *testing.T) {
	module := &fakeGatedModule{required: []capability.ID{capability.Docker}}
	server, fake := newGatedTestServer(t, module, capability.New(capability.Systemd))

	response := getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), `href="/gated"`)
	assert.NotContains(t, response.Body.String(), "Gated Module")

	fake.capabilities = capability.New(capability.Docker)
	server.refreshCapabilities(context.Background(), "token")

	response = getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `href="/gated"`)
	assert.Contains(t, response.Body.String(), "Gated Module")
}

func TestGatedModuleRouteMountedAlways404sUntilCapabilityPresent(t *testing.T) {
	module := &fakeGatedModule{required: []capability.ID{capability.Docker}}
	server, fake := newGatedTestServer(t, module, capability.New(capability.Systemd))

	request := httptest.NewRequest(http.MethodGet, "/gated", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusNotFound, response.Code)

	fake.capabilities = capability.New(capability.Docker)
	server.refreshCapabilities(context.Background(), "token")

	request = httptest.NewRequest(http.MethodGet, "/gated", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "gated-page-marker")
}

// fakeGatedAnyModule is a synthetic platform.Module that implements
// platform.CapabilityGateAny (not CapabilityGate), used to prove the any-of
// nav/dashboard/route filtering mechanism added in this chunk through a
// real registry and real HTTP round trip, mirroring fakeGatedModule above.
type fakeGatedAnyModule struct {
	dashboardCalls int
	requiredAny    []capability.ID
}

func (m *fakeGatedAnyModule) Dashboard(context.Context, platform.Host) ([]platform.DashboardCard, error) {
	m.dashboardCalls++
	return []platform.DashboardCard{{Component: textComponent("gated-any-card-marker"), Order: 1, Span: platform.SpanFull}}, nil
}

func (m *fakeGatedAnyModule) Manifest() platform.Manifest {
	return platform.Manifest{ID: "gated-any", Name: "Gated Any Module", Order: 51, Path: "/gated-any"}
}

func (m *fakeGatedAnyModule) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /gated-any", platform.GateAny(host, m.requiredAny, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "gated-any-page-marker")
	}))
}

func (m *fakeGatedAnyModule) RequiredAnyCapabilities() []capability.ID { return m.requiredAny }

// fakePlainModule is a synthetic platform.Module implementing neither
// platform.CapabilityGate nor platform.CapabilityGateAny: it has no
// capability requirement at all and must always be available, the third
// case moduleAvailable's AND-of-two-defaults composition must handle
// correctly alongside CapabilityGate-only and CapabilityGateAny-only.
type fakePlainModule struct {
	dashboardCalls int
}

func (m *fakePlainModule) Dashboard(context.Context, platform.Host) ([]platform.DashboardCard, error) {
	m.dashboardCalls++
	return []platform.DashboardCard{{Component: textComponent("plain-card-marker"), Order: 1, Span: platform.SpanFull}}, nil
}

func (m *fakePlainModule) Manifest() platform.Manifest {
	return platform.Manifest{ID: "plain", Name: "Plain Module", Order: 1, Path: "/plain"}
}

func (m *fakePlainModule) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /plain", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain-page-marker")
	})
}

func newGatedAnyTestServer(t *testing.T, module *fakeGatedAnyModule, initialCaps capability.Set) (*Server, *fakeBroker) {
	t.Helper()
	registry, err := platform.NewRegistry(module)
	require.NoError(t, err)
	fake := &fakeBroker{session: broker.SessionResponse{CSRF: "csrf"}, capabilities: initialCaps}
	server, err := NewServer(registry, fake, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	server.refreshCapabilities(context.Background(), "token")
	return server, fake
}

func TestDashboardOmitsCardAndSkipsDashboardCallForCapabilityGateAnyAbsentModule(t *testing.T) {
	module := &fakeGatedAnyModule{requiredAny: []capability.ID{capability.Docker, capability.Podman}}
	server, fake := newGatedAnyTestServer(t, module, capability.New(capability.Systemd))

	response := getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "gated-any-card-marker")
	assert.NotContains(t, response.Body.String(), "Module unavailable")
	assert.Zero(t, module.dashboardCalls)

	// One of the required set (Podman) present, not Docker: any-of must be
	// satisfied.
	fake.capabilities = capability.New(capability.Podman)
	server.refreshCapabilities(context.Background(), "token")

	response = getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "gated-any-card-marker")
	assert.Equal(t, 1, module.dashboardCalls)
}

func TestNavOmitsManifestForCapabilityGateAnyAbsentModuleAndIncludesItWhenPresent(t *testing.T) {
	module := &fakeGatedAnyModule{requiredAny: []capability.ID{capability.Docker, capability.Podman}}
	server, fake := newGatedAnyTestServer(t, module, capability.New(capability.Systemd))

	response := getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), `href="/gated-any"`)
	assert.NotContains(t, response.Body.String(), "Gated Any Module")

	fake.capabilities = capability.New(capability.Podman)
	server.refreshCapabilities(context.Background(), "token")

	response = getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `href="/gated-any"`)
	assert.Contains(t, response.Body.String(), "Gated Any Module")
}

func TestGatedAnyModuleRouteMountedAlways404sUntilOneOfRequiredCapabilitiesPresent(t *testing.T) {
	module := &fakeGatedAnyModule{requiredAny: []capability.ID{capability.Docker, capability.Podman}}
	server, fake := newGatedAnyTestServer(t, module, capability.New(capability.Systemd))

	request := httptest.NewRequest(http.MethodGet, "/gated-any", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusNotFound, response.Code)

	fake.capabilities = capability.New(capability.Podman)
	server.refreshCapabilities(context.Background(), "token")

	request = httptest.NewRequest(http.MethodGet, "/gated-any", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "gated-any-page-marker")
}

// TestModuleAvailabilityCoversCapabilityGateCapabilityGateAnyAndNeither
// registers one module of each of the three shapes moduleAvailable's
// AND-of-two-defaults composition must handle (CapabilityGate only,
// CapabilityGateAny only, and neither interface) into one real registry and
// proves, through a real *web.Server and authenticated HTTP round trips,
// that each is correctly included or excluded from both the nav
// (availableManifests) and the dashboard card list as capabilities change.
func TestModuleAvailabilityCoversCapabilityGateCapabilityGateAnyAndNeither(t *testing.T) {
	gateOnly := &fakeGatedModule{required: []capability.ID{capability.Docker}}
	gateAnyOnly := &fakeGatedAnyModule{requiredAny: []capability.ID{capability.Podman, capability.Incus}}
	neither := &fakePlainModule{}

	registry, err := platform.NewRegistry(gateOnly, gateAnyOnly, neither)
	require.NoError(t, err)
	fake := &fakeBroker{session: broker.SessionResponse{CSRF: "csrf"}, capabilities: capability.New(capability.Systemd)}
	server, err := NewServer(registry, fake, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	server.refreshCapabilities(context.Background(), "token")

	// Neither Docker (gateOnly's requirement) nor Podman/Incus (gateAnyOnly's
	// any-of set) is present: only the module implementing neither interface
	// is available.
	response := getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "gated-card-marker")
	assert.NotContains(t, response.Body.String(), "gated-any-card-marker")
	assert.Contains(t, response.Body.String(), "plain-card-marker")
	assert.NotContains(t, response.Body.String(), `href="/gated"`)
	assert.NotContains(t, response.Body.String(), `href="/gated-any"`)
	assert.Contains(t, response.Body.String(), `href="/plain"`)
	assert.Zero(t, gateOnly.dashboardCalls)
	assert.Zero(t, gateAnyOnly.dashboardCalls)
	assert.Equal(t, 1, neither.dashboardCalls)

	// Docker present: the CapabilityGate-only module becomes available; the
	// CapabilityGateAny-only module still isn't (Docker isn't in its any-of
	// set); the plain module remains available.
	fake.capabilities = capability.New(capability.Docker)
	server.refreshCapabilities(context.Background(), "token")

	response = getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "gated-card-marker")
	assert.NotContains(t, response.Body.String(), "gated-any-card-marker")
	assert.Contains(t, response.Body.String(), "plain-card-marker")
	assert.Contains(t, response.Body.String(), `href="/gated"`)
	assert.NotContains(t, response.Body.String(), `href="/gated-any"`)
	assert.Equal(t, 1, gateOnly.dashboardCalls)
	assert.Zero(t, gateAnyOnly.dashboardCalls)

	// Podman present instead: the CapabilityGateAny-only module becomes
	// available (any-of satisfied); the CapabilityGate-only module (still
	// missing Docker) is not.
	fake.capabilities = capability.New(capability.Podman)
	server.refreshCapabilities(context.Background(), "token")

	response = getAuthenticated(server)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "gated-card-marker")
	assert.Contains(t, response.Body.String(), "gated-any-card-marker")
	assert.Contains(t, response.Body.String(), "plain-card-marker")
	assert.NotContains(t, response.Body.String(), `href="/gated"`)
	assert.Contains(t, response.Body.String(), `href="/gated-any"`)
	assert.Equal(t, 1, gateOnly.dashboardCalls)
	assert.Equal(t, 1, gateAnyOnly.dashboardCalls)
}

type countingReader struct {
	io.Reader
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}

func withTestSession(r *http.Request, csrf, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, requestSession{
		data:  broker.SessionResponse{CSRF: csrf},
		token: token,
	}))
}
