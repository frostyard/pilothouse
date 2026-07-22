package broker

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryAudit struct {
	begin     func()
	beginErr  error
	completed []audit.Record
	mu        sync.Mutex
	records   []audit.Record
}

func (s *memoryAudit) Begin(_ context.Context, attempt audit.Attempt) (audit.Record, error) {
	if s.beginErr != nil {
		return audit.Record{}, s.beginErr
	}
	if s.begin != nil {
		s.begin()
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

func TestActionPrepareRunsBeforeAuditResourceAndHandler(t *testing.T) {
	steps := []string{}
	store := &memoryAudit{begin: func() { steps = append(steps, "audit") }}
	registry := NewActionRegistry(store)
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.create", Admin: true, Parameters: []string{"value"},
		Prepare: func(_ context.Context, _ auth.Identity, parameters map[string]string) (map[string]string, error) {
			steps = append(steps, "prepare")
			prepared := cloneParameters(parameters)
			prepared["_id"] = "trusted-id"
			return prepared, nil
		},
		Resource: func(parameters map[string]string) (string, error) {
			steps = append(steps, "resource")
			return "thing/" + parameters["_id"], nil
		},
		Handler: func(_ context.Context, _ auth.Identity, parameters map[string]string) error {
			steps = append(steps, "handler")
			assert.Equal(t, "trusted-id", parameters["_id"])
			return nil
		},
	}))

	require.NoError(t, registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.create", map[string]string{"value": "public"}, ""))
	assert.Equal(t, []string{"prepare", "resource", "audit", "handler"}, steps)
	require.Len(t, store.records, 1)
	assert.Equal(t, "thing/trusted-id", store.records[0].Resource)
}

func TestActionPrepareRejectsUntrustedOrInvalidParameters(t *testing.T) {
	t.Run("external internal id", func(t *testing.T) {
		prepared := false
		registry := NewActionRegistry(&memoryAudit{})
		require.NoError(t, registry.RegisterDefinition(ActionDefinition{
			ID: "test.create", Admin: true, Parameters: []string{"value"},
			Prepare: func(context.Context, auth.Identity, map[string]string) (map[string]string, error) {
				prepared = true
				return nil, nil
			},
			Resource: func(map[string]string) (string, error) { return "thing", nil },
			Handler:  func(context.Context, auth.Identity, map[string]string) error { return nil },
		}))

		err := registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.create", map[string]string{"value": "public", "_id": "untrusted"}, "")
		assert.ErrorContains(t, err, "action parameters")
		assert.False(t, prepared)
	})

	t.Run("prepare error has no side effects", func(t *testing.T) {
		store := &memoryAudit{}
		resourceCalled := false
		handlerCalled := false
		registry := NewActionRegistry(store)
		require.NoError(t, registry.RegisterDefinition(ActionDefinition{
			ID: "test.create", Admin: true, Parameters: []string{"value"},
			Prepare: func(context.Context, auth.Identity, map[string]string) (map[string]string, error) {
				return nil, errors.New("cannot prepare")
			},
			Resource: func(map[string]string) (string, error) { resourceCalled = true; return "thing", nil },
			Handler:  func(context.Context, auth.Identity, map[string]string) error { handlerCalled = true; return nil },
		}))

		err := registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.create", map[string]string{"value": "public"}, "")
		assert.ErrorContains(t, err, "prepare action")
		assert.False(t, resourceCalled)
		assert.False(t, handlerCalled)
		assert.Empty(t, store.records)
	})

	for _, value := range []string{"\r", "\n", "\x00", string(make([]byte, 513))} {
		t.Run("invalid derived id", func(t *testing.T) {
			store := &memoryAudit{}
			handlerCalled := false
			registry := NewActionRegistry(store)
			require.NoError(t, registry.RegisterDefinition(ActionDefinition{
				ID: "test.create", Admin: true, Parameters: []string{"value"},
				Prepare: func(_ context.Context, _ auth.Identity, parameters map[string]string) (map[string]string, error) {
					prepared := cloneParameters(parameters)
					prepared["_id"] = value
					return prepared, nil
				},
				Resource: func(map[string]string) (string, error) { return "thing", nil },
				Handler:  func(context.Context, auth.Identity, map[string]string) error { handlerCalled = true; return nil },
			}))

			err := registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.create", map[string]string{"value": "public"}, "")
			assert.ErrorContains(t, err, "prepared action parameters")
			assert.False(t, handlerCalled)
			assert.Empty(t, store.records)
		})
	}
}

func TestActionPrepareCannotMutateCallerParameters(t *testing.T) {
	parameters := map[string]string{"value": "public"}
	registry := NewActionRegistry(&memoryAudit{})
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.create", Admin: true, Parameters: []string{"value"},
		Prepare: func(_ context.Context, _ auth.Identity, parameters map[string]string) (map[string]string, error) {
			parameters["_id"] = "trusted-id"
			return parameters, nil
		},
		Resource: func(map[string]string) (string, error) { return "thing", nil },
		Handler:  func(context.Context, auth.Identity, map[string]string) error { return nil },
	}))

	require.NoError(t, registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.create", parameters, ""))
	assert.Equal(t, map[string]string{"value": "public"}, parameters)
}

func TestActionWithoutPrepareRetainsCallerParameterSemantics(t *testing.T) {
	parameters := map[string]string{"value": "public"}
	registry := NewActionRegistry(&memoryAudit{})
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.create", Admin: true, Parameters: []string{"value"},
		Resource: func(map[string]string) (string, error) { return "thing", nil },
		Handler: func(_ context.Context, _ auth.Identity, parameters map[string]string) error {
			parameters["value"] = "handled"
			return nil
		},
	}))

	require.NoError(t, registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.create", parameters, ""))
	assert.Equal(t, map[string]string{"value": "handled"}, parameters)
}

func TestAuditBeginFailurePreventsMutation(t *testing.T) {
	registry := NewActionRegistry(&memoryAudit{beginErr: errors.New("disk full")})
	called := false
	require.NoError(t, registry.RegisterDefinition(safetyDefinition(func(context.Context, auth.Identity, map[string]string) error { called = true; return nil })))
	err := registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.stop", map[string]string{"id": "web"}, "container/web")
	assert.ErrorContains(t, err, "record action intent")
	assert.False(t, called)
	registry.locks.mu.Lock()
	assert.Empty(t, registry.locks.locks)
	registry.locks.mu.Unlock()
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

func TestBlockingLockAcquiresAfterTryLockRelease(t *testing.T) {
	locks := newResourceLocks()
	backgroundUnlock, acquired := locks.tryLock("resource")
	require.True(t, acquired)
	acquiredByWaiter := make(chan func(), 1)
	go func() {
		unlock, err := locks.lock(context.Background(), "resource")
		if err == nil {
			acquiredByWaiter <- unlock
		}
	}()
	select {
	case <-acquiredByWaiter:
		t.Fatal("blocking lock overlapped try lock")
	case <-time.After(20 * time.Millisecond):
	}
	backgroundUnlock()
	select {
	case unlock := <-acquiredByWaiter:
		unlock()
	case <-time.After(time.Second):
		t.Fatal("blocking lock did not acquire after try lock release")
	}
}

func TestBackgroundActionOutlivesRequestAndCompletesAudit(t *testing.T) {
	jobStore, err := jobs.Open(filepath.Join(t.TempDir(), "jobs.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jobStore.Close()) })
	auditStore := &memoryAudit{}
	registry := NewActionRegistry(auditStore)
	registry.UseJobs(jobStore)
	started := make(chan struct{})
	release := make(chan struct{})
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.update", Admin: true, Background: true, RebootRequired: true, Timeout: time.Second,
		Resource: func(map[string]string) (string, error) { return "updates/global", nil },
		Handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			close(started)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, registry.Execute(ctx, auth.Identity{Admin: true, Username: "admin"}, "test.update", nil, ""))
	cancel()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background action did not start")
	}
	err = registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.update", nil, "")
	assert.ErrorContains(t, err, "already has a maintenance job")
	close(release)
	require.NoError(t, registry.Wait(context.Background()))
	records, err := jobStore.List(context.Background(), jobs.Filter{})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, jobs.StatusSucceeded, records[0].Status)
	assert.True(t, records[0].RebootRequired)
	require.Len(t, auditStore.completed, 1)
	assert.Equal(t, audit.OutcomeSucceeded, auditStore.completed[0].Outcome)
}

func TestShutdownCancelsBackgroundAction(t *testing.T) {
	jobStore, err := jobs.Open(filepath.Join(t.TempDir(), "jobs.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jobStore.Close()) })
	auditStore := &memoryAudit{}
	registry := NewActionRegistry(auditStore)
	registry.UseJobs(jobStore)
	started := make(chan struct{})
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.update", Admin: true, Background: true, Timeout: time.Minute,
		Resource: func(map[string]string) (string, error) { return "updates/global", nil },
		Handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}))
	require.NoError(t, registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.update", nil, ""))
	<-started
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, registry.Shutdown(shutdownCtx))
	records, err := jobStore.List(context.Background(), jobs.Filter{})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, jobs.StatusFailed, records[0].Status)
	assert.Equal(t, "cancelled", records[0].ErrorCategory)
}
