package broker

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

type switchResolver struct {
	identity auth.Identity
}

func (r *switchResolver) Resolve(_ string) (auth.Identity, error) { return r.identity, nil }

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
		NewStreamActionRegistry(),
		NewStreamQueryRegistry(),
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

func TestBrokerClientStreamsQueryAndChunkedAction(t *testing.T) {
	var uploaded strings.Builder
	queryBody := &trackingBody{Reader: strings.NewReader("data")}
	streamQueries := NewStreamQueryRegistry()
	streamActions := NewStreamActionRegistry()
	require.NoError(t, streamQueries.Register(StreamQueryDefinition{
		ID: "test.download", Admin: true, Parameters: []string{"path"}, Limit: 8,
		Handler: func(context.Context, auth.Identity, map[string]string) (StreamResult, error) {
			return StreamResult{Body: queryBody, Filename: "a b.txt", MediaType: "application/octet-stream", Size: 4}, nil
		},
	}))
	require.NoError(t, streamActions.Register(StreamActionDefinition{
		ID: "test.upload", Admin: true, Parameters: []string{"name"}, Limit: 8,
		Resource: func(p map[string]string) (string, error) { return "test/" + p["name"], nil },
		Handler: func(_ context.Context, _ auth.Identity, _ map[string]string, body io.Reader) error {
			_, err := io.Copy(&uploaded, body)
			return err
		},
	}))

	client := streamTestClient(t, streamActions, streamQueries, &switchResolver{identity: auth.Identity{Admin: true, UID: 1000, Username: "snow"}})
	login, err := client.Login(context.Background(), "snow", "secret", "local")
	require.NoError(t, err)
	result, err := client.StreamQuery(context.Background(), login.Token, "test.download", map[string]string{"path": "file"})
	require.NoError(t, err)
	assert.Equal(t, "a b.txt", result.Filename)
	assert.Equal(t, "application/octet-stream", result.MediaType)
	assert.EqualValues(t, 4, result.Size)
	body, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	require.NoError(t, result.Body.Close())
	assert.Equal(t, "data", string(body))
	assert.True(t, queryBody.closed)

	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- client.StreamAction(context.Background(), login.Token, "test.upload", map[string]string{"name": "file"}, reader)
	}()
	_, err = writer.Write([]byte("chunked"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, <-done)
	assert.Equal(t, "chunked", uploaded.String())
}

func TestBrokerClientStreamsRejectOversizedBodiesAndRefreshAuthorization(t *testing.T) {
	called := false
	resolver := &switchResolver{identity: auth.Identity{Admin: true, UID: 1000, Username: "snow"}}
	streamActions := NewStreamActionRegistry()
	streamQueries := NewStreamQueryRegistry()
	require.NoError(t, streamActions.Register(StreamActionDefinition{
		ID: "test.upload", Admin: true, Limit: 8,
		Resource: func(map[string]string) (string, error) { return "test/file", nil },
		Handler: func(_ context.Context, _ auth.Identity, _ map[string]string, body io.Reader) error {
			called = true
			_, err := io.Copy(io.Discard, body)
			return err
		},
	}))
	client := streamTestClient(t, streamActions, streamQueries, resolver)
	login, err := client.Login(context.Background(), "snow", "secret", "local")
	require.NoError(t, err)

	err = client.StreamAction(context.Background(), login.Token, "test.upload", nil, strings.NewReader("too-large"))
	assert.Equal(t, http.StatusRequestEntityTooLarge, StatusCode(err))
	assert.False(t, called)

	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- client.StreamAction(context.Background(), login.Token, "test.upload", nil, reader) }()
	_, err = writer.Write([]byte("too-large"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	err = <-done
	assert.Equal(t, http.StatusRequestEntityTooLarge, StatusCode(err))
	assert.True(t, called)

	resolver.identity.Admin = false
	err = client.StreamAction(context.Background(), login.Token, "test.upload", nil, strings.NewReader("ok"))
	assert.Equal(t, http.StatusForbidden, StatusCode(err))
}

func TestBrokerStreamMetadataLimit(t *testing.T) {
	client := streamTestClient(t, NewStreamActionRegistry(), NewStreamQueryRegistry(), &switchResolver{identity: auth.Identity{Admin: true, Username: "snow"}})
	login, err := client.Login(context.Background(), "snow", "secret", "local")
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, client.baseURL+"/v1/stream-actions/test.upload", strings.NewReader("x"))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	request.Header.Set(StreamMetadataHeader, strings.Repeat("a", 8<<10+1))
	response, err := client.http.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, response.StatusCode)
}

func streamTestClient(t *testing.T, streamActions *StreamActionRegistry, streamQueries *StreamQueryRegistry, resolver auth.Resolver) *Client {
	t.Helper()
	server := NewServer(fakeAuthenticator{}, resolver, NewSessionStore(time.Minute, time.Hour), NewActionRegistry(), NewQueryRegistry(), streamActions, streamQueries, slog.New(slog.NewTextHandler(io.Discard, nil)))
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() {
		_ = httpServer.Close()
		_ = listener.Close()
	})
	return NewClient(socket)
}

func TestBrokerStreamQueryCancellationAndBackpressure(t *testing.T) {
	started := make(chan struct{})
	closed := make(chan struct{})
	queries := NewStreamQueryRegistry()
	require.NoError(t, queries.Register(StreamQueryDefinition{
		ID: "test.block", Limit: 2 << 20,
		Handler: func(context.Context, auth.Identity, map[string]string) (StreamResult, error) {
			return StreamResult{Body: &cancellableBody{Reader: strings.NewReader(strings.Repeat("x", 2<<20)), closed: closed}, Size: 2 << 20}, nil
		},
	}))
	client := streamTestClient(t, NewStreamActionRegistry(), queries, &switchResolver{identity: auth.Identity{Admin: true, Username: "snow"}})
	login, err := client.Login(context.Background(), "snow", "secret", "local")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		result, queryErr := client.StreamQuery(ctx, login.Token, "test.block", nil)
		if queryErr == nil {
			close(started)
			<-closed
			_ = result.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream response did not begin")
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("stream body was not closed after client cancellation")
	}
}

type cancellableBody struct {
	io.Reader
	closed chan<- struct{}
}

func (b *cancellableBody) Close() error {
	select {
	case b.closed <- struct{}{}:
	default:
	}
	return nil
}
