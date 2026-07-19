package broker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryAudit struct {
	beginErr  error
	completed []audit.Record
	mu        sync.Mutex
	records   []audit.Record
}

func (s *memoryAudit) Begin(_ context.Context, attempt audit.Attempt) (audit.Record, error) {
	if s.beginErr != nil {
		return audit.Record{}, s.beginErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := audit.Record{ID: uint64(len(s.records) + 1), Action: attempt.Action, Resource: attempt.Resource, Username: attempt.Username, UID: attempt.UID, Outcome: audit.OutcomeStarted}
	s.records = append(s.records, record)
	return record, nil
}

func (s *memoryAudit) Complete(_ context.Context, id uint64, outcome, category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[id-1]
	record.Outcome = outcome
	record.ErrorCategory = category
	s.completed = append(s.completed, record)
	return nil
}

func safetyDefinition(handler ActionHandler) ActionDefinition {
	return ActionDefinition{
		ID: "test.stop", Admin: true, ConfirmationRequired: true, Parameters: []string{"id"},
		Resource: func(parameters map[string]string) (string, error) { return "container/" + parameters["id"], nil },
		Handler:  handler,
	}
}

func TestActionRequiresExactParametersAndConfirmation(t *testing.T) {
	store := &memoryAudit{}
	called := false
	registry := NewActionRegistry(store)
	require.NoError(t, registry.RegisterDefinition(safetyDefinition(func(context.Context, auth.Identity, map[string]string) error { called = true; return nil })))
	identity := auth.Identity{Admin: true, UID: 1000, Username: "admin"}

	err := registry.Execute(context.Background(), identity, "test.stop", map[string]string{"id": "web", "extra": "no"}, "container/web")
	assert.ErrorContains(t, err, "action parameters")
	err = registry.Execute(context.Background(), identity, "test.stop", map[string]string{"id": "web"}, "")
	assert.ErrorIs(t, err, ErrConfirmationRequired)
	assert.False(t, called)

	require.NoError(t, registry.Execute(context.Background(), identity, "test.stop", map[string]string{"id": "web"}, "container/web"))
	assert.True(t, called)
	require.Len(t, store.completed, 1)
	assert.Equal(t, "container/web", store.completed[0].Resource)
	assert.Equal(t, audit.OutcomeSucceeded, store.completed[0].Outcome)
}

func TestAuditBeginFailurePreventsMutation(t *testing.T) {
	registry := NewActionRegistry(&memoryAudit{beginErr: errors.New("disk full")})
	called := false
	require.NoError(t, registry.RegisterDefinition(safetyDefinition(func(context.Context, auth.Identity, map[string]string) error { called = true; return nil })))
	err := registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.stop", map[string]string{"id": "web"}, "container/web")
	assert.ErrorContains(t, err, "record action intent")
	assert.False(t, called)
}

func TestActionsSerializeSameResourceAndAllowDifferentResources(t *testing.T) {
	registry := NewActionRegistry(&memoryAudit{})
	entered := make(chan string, 3)
	release := make(chan struct{})
	require.NoError(t, registry.RegisterDefinition(safetyDefinition(func(_ context.Context, _ auth.Identity, parameters map[string]string) error {
		entered <- parameters["id"]
		<-release
		return nil
	})))
	identity := auth.Identity{Admin: true}
	run := func(id string) {
		_ = registry.Execute(context.Background(), identity, "test.stop", map[string]string{"id": id}, "container/"+id)
	}
	go run("same")
	require.Equal(t, "same", <-entered)
	go run("same")
	go run("different")
	select {
	case got := <-entered:
		assert.Equal(t, "different", got)
	case <-time.After(time.Second):
		t.Fatal("different resource was blocked")
	}
	select {
	case got := <-entered:
		t.Fatalf("same resource overlapped: %s", got)
	case <-time.After(30 * time.Millisecond):
	}
	release <- struct{}{}
	release <- struct{}{}
	require.Equal(t, "same", <-entered)
	release <- struct{}{}
}

func TestResourceLockWaitHonorsCancellationAndReclaimsEntry(t *testing.T) {
	locks := newResourceLocks()
	unlock, err := locks.lock(context.Background(), "resource")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = locks.lock(ctx, "resource")
	assert.ErrorIs(t, err, context.Canceled)
	unlock()
	locks.mu.Lock()
	assert.Empty(t, locks.locks)
	locks.mu.Unlock()
}
