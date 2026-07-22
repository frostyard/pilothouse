package broker

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

func TestStreamRegistriesAuthorizeValidateAndLimit(t *testing.T) {
	queries := NewStreamQueryRegistry()
	called := false
	require.NoError(t, queries.Register(StreamQueryDefinition{
		ID: "test.download", Admin: true, Parameters: []string{"path"}, Limit: 4,
		Handler: func(context.Context, auth.Identity, map[string]string) (StreamResult, error) {
			called = true
			return StreamResult{Body: io.NopCloser(strings.NewReader("five!")), Size: 5}, nil
		},
	}))
	_, err := queries.Execute(context.Background(), auth.Identity{}, "test.download", map[string]string{"path": "file"})
	assert.ErrorContains(t, err, "not authorized")
	assert.False(t, called)
	_, err = queries.Execute(context.Background(), auth.Identity{Admin: true}, "test.download", map[string]string{"path": "file", "extra": "x"})
	assert.ErrorContains(t, err, "parameters")
	_, err = queries.Execute(context.Background(), auth.Identity{Admin: true}, "test.download", map[string]string{"path": ""})
	assert.ErrorIs(t, err, ErrStreamTooLarge)

	body := &trackingBody{Reader: strings.NewReader("five!")}
	require.NoError(t, queries.Register(StreamQueryDefinition{
		ID: "test.close", Parameters: []string{"path"}, Limit: 4,
		Handler: func(context.Context, auth.Identity, map[string]string) (StreamResult, error) {
			return StreamResult{Body: body, Size: 5}, nil
		},
	}))
	_, err = queries.Execute(context.Background(), auth.Identity{}, "test.close", map[string]string{"path": "file"})
	assert.ErrorIs(t, err, ErrStreamTooLarge)
	assert.True(t, body.closed)
}

func TestStreamParametersRejectInvalidMetadata(t *testing.T) {
	registry := NewStreamQueryRegistry()
	require.NoError(t, registry.Register(StreamQueryDefinition{
		ID: "test.metadata", Parameters: []string{"alpha", "path"}, Limit: 1,
		Handler: func(context.Context, auth.Identity, map[string]string) (StreamResult, error) {
			return StreamResult{Body: io.NopCloser(strings.NewReader("x")), Size: 1}, nil
		},
	}))

	_, err := registry.Execute(context.Background(), auth.Identity{}, "test.metadata", map[string]string{"alpha": "", "path": "file"})
	assert.NoError(t, err)

	for _, parameters := range []map[string]string{
		{"alpha": strings.Repeat("x", 4097), "path": "file"},
		{"alpha": "bad\x00value", "path": "file"},
		{"alpha": strings.Repeat("x", 4096), "path": strings.Repeat("y", 4096)},
	} {
		_, err := registry.Execute(context.Background(), auth.Identity{}, "test.metadata", parameters)
		assert.ErrorContains(t, err, "parameters")
	}
}

func TestStreamActionsLimitSerializeCancelAndAudit(t *testing.T) {
	store := &memoryAudit{}
	registry := NewStreamActionRegistry(store)
	identity := auth.Identity{Admin: true, Username: "admin", UID: 1000}
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	require.NoError(t, registry.Register(StreamActionDefinition{
		ID: "test.upload", Admin: true, Parameters: []string{"path"}, Limit: 4,
		Resource: func(parameters map[string]string) (string, error) { return parameters["path"], nil },
		Handler: func(ctx context.Context, _ auth.Identity, _ map[string]string, body io.Reader) error {
			if _, err := io.ReadAll(body); err != nil {
				return err
			}
			mu.Lock()
			started++
			mu.Unlock()
			select {
			case entered <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}))

	err := registry.Execute(context.Background(), auth.Identity{}, "test.upload", map[string]string{"path": "same"}, strings.NewReader("ok"))
	assert.ErrorContains(t, err, "not authorized")
	err = registry.Execute(context.Background(), identity, "test.upload", map[string]string{"path": "same"}, strings.NewReader("five!"))
	assert.ErrorIs(t, err, ErrStreamTooLarge)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- registry.Execute(context.Background(), identity, "test.upload", map[string]string{"path": "same"}, strings.NewReader("ok"))
	}()
	<-entered
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- registry.Execute(context.Background(), identity, "test.upload", map[string]string{"path": "same"}, strings.NewReader("ok"))
	}()
	select {
	case <-entered:
		t.Fatal("same resource action overlapped")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-firstDone)
	<-entered
	require.NoError(t, <-secondDone)
	assert.Equal(t, 2, started)

	for _, want := range []struct {
		err      error
		category string
	}{
		{errors.New("failed"), "operation_failed"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "cancelled"},
	} {
		require.NoError(t, registry.Register(StreamActionDefinition{
			ID: "test." + want.category, Limit: 4,
			Resource: func(map[string]string) (string, error) { return want.category, nil },
			Handler:  func(context.Context, auth.Identity, map[string]string, io.Reader) error { return want.err },
		}))
		err := registry.Execute(context.Background(), identity, "test."+want.category, nil, strings.NewReader("ok"))
		assert.ErrorIs(t, err, want.err)
	}
	require.Len(t, store.completed, 5)
	assert.Equal(t, audit.OutcomeSucceeded, store.completed[0].Outcome)
	assert.Equal(t, audit.OutcomeSucceeded, store.completed[1].Outcome)
	assert.Equal(t, audit.OutcomeFailed, store.completed[2].Outcome)
	assert.Equal(t, "operation_failed", store.completed[2].ErrorCategory)
	assert.Equal(t, audit.OutcomeFailed, store.completed[3].Outcome)
	assert.Equal(t, "timeout", store.completed[3].ErrorCategory)
	assert.Equal(t, audit.OutcomeFailed, store.completed[4].Outcome)
	assert.Equal(t, "cancelled", store.completed[4].ErrorCategory)
}

func TestStreamActionCancellationReleasesLock(t *testing.T) {
	registry := NewStreamActionRegistry()
	require.NoError(t, registry.Register(StreamActionDefinition{
		ID: "test.cancel", Limit: 1,
		Resource: func(map[string]string) (string, error) { return "file", nil },
		Handler: func(ctx context.Context, _ auth.Identity, _ map[string]string, _ io.Reader) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := registry.Execute(ctx, auth.Identity{}, "test.cancel", nil, strings.NewReader("x"))
	assert.ErrorIs(t, err, context.Canceled)
	registry.locks.mu.Lock()
	assert.Empty(t, registry.locks.locks)
	registry.locks.mu.Unlock()
}

func TestStreamRegistryPublicErrors(t *testing.T) {
	err := NewPublicError(413, "too large", "stream_too_large", ErrStreamTooLarge)
	status, message, category := PublicErrorDetails(err)
	assert.Equal(t, 413, status)
	assert.Equal(t, "too large", message)
	assert.Equal(t, "stream_too_large", category)
	assert.Equal(t, 413, StatusCode(err))
	assert.Equal(t, 503, StatusCode(errors.New("unavailable")))
}
