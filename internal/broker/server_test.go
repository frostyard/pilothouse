package broker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAuthenticator struct {
	err error
}

func (a fakeAuthenticator) Authenticate(_, _ string) error { return a.err }

type fakeResolver struct {
	identity auth.Identity
}

func (r fakeResolver) Resolve(_ string) (auth.Identity, error) { return r.identity, nil }

type handlerTransport struct {
	handler http.Handler
}

func (t handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func TestBrokerClientLoginSessionAndAuthorizedAction(t *testing.T) {
	actions := NewActionRegistry()
	queries := NewQueryRegistry()
	called := false
	require.NoError(t, actions.RegisterDefinition(ActionDefinition{
		ID: "test.manage", Admin: true, Parameters: []string{"value"},
		Resource: func(parameters map[string]string) (string, error) { return "test/" + parameters["value"], nil },
		Handler: func(_ context.Context, identity auth.Identity, parameters map[string]string) error {
			called = identity.Username == "snow" && parameters["value"] == "yes"
			return nil
		},
	}))
	require.NoError(t, queries.Register("test.read", false, func(_ context.Context, identity auth.Identity, parameters map[string]string) (any, error) {
		return map[string]string{"user": identity.Username, "value": parameters["value"]}, nil
	}))
	handler := NewServer(
		fakeAuthenticator{},
		fakeResolver{identity: auth.Identity{Admin: true, UID: 1000, Username: "snow"}},
		NewSessionStore(time.Minute, time.Hour),
		actions,
		queries,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	client := &Client{baseURL: "http://broker", http: &http.Client{Transport: handlerTransport{handler: handler.Handler()}}, socket: "test"}
	login, err := client.Login(context.Background(), "snow", "secret", "127.0.0.1")
	require.NoError(t, err)
	assert.NotEmpty(t, login.Token)
	assert.NotEmpty(t, login.Session.CSRF)

	session, err := client.Session(context.Background(), login.Token)
	require.NoError(t, err)
	assert.Equal(t, "snow", session.Identity.Username)
	require.NoError(t, client.Action(context.Background(), login.Token, "test.manage", map[string]string{"value": "yes"}, ""))
	assert.True(t, called)
	var queryResult map[string]string
	require.NoError(t, client.Query(context.Background(), login.Token, "test.read", map[string]string{"value": "visible"}, &queryResult))
	assert.Equal(t, map[string]string{"user": "snow", "value": "visible"}, queryResult)
	require.NoError(t, client.Logout(context.Background(), login.Token))
	_, err = client.Session(context.Background(), login.Token)
	assert.ErrorIs(t, err, ErrUnauthorized)
}
