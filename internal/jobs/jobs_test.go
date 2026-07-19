package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreLifecycleAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	store, err := Open(path, 10)
	require.NoError(t, err)

	job, err := store.Enqueue(context.Background(), Attempt{
		Action:   "restart",
		AuditID:  42,
		Resource: "service:sshd",
		Username: "alice",
		UID:      1000,
	})
	require.NoError(t, err)
	require.NotZero(t, job.ID)
	require.Equal(t, StatusQueued, job.Status)
	require.False(t, job.CreatedAt.IsZero())
	require.Nil(t, job.StartedAt)
	require.Nil(t, job.FinishedAt)

	require.NoError(t, store.Start(context.Background(), job.ID))
	time.Sleep(time.Millisecond)
	require.NoError(t, store.Complete(context.Background(), job.ID, StatusFailed, "permission_denied", true))
	require.NoError(t, store.Close())

	store, err = Open(path, 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	jobs, err := store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	got := jobs[0]
	require.Equal(t, job.ID, got.ID)
	require.Equal(t, uint64(42), got.AuditID)
	require.Equal(t, "restart", got.Action)
	require.Equal(t, "service:sshd", got.Resource)
	require.Equal(t, "alice", got.Username)
	require.Equal(t, 1000, got.UID)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, "permission_denied", got.ErrorCategory)
	require.True(t, got.RebootRequired)
	require.NotNil(t, got.StartedAt)
	require.NotNil(t, got.FinishedAt)
	require.GreaterOrEqual(t, got.DurationMS, int64(1))
}

func TestJobJSONContainsOnlyDeclaredFields(t *testing.T) {
	job := Job{ID: 1, Action: "upgrade", Resource: "host", Status: StatusQueued, CreatedAt: time.Now().UTC()}
	encoded, err := json.Marshal(job)
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.ElementsMatch(t, []string{
		"id", "audit_id", "action", "resource", "username", "uid", "status", "reboot_required", "created_at", "duration_ms",
	}, mapKeys(fields))
	require.NotContains(t, string(encoded), "parameter")
	require.NotContains(t, string(encoded), "raw_error")
}

func TestOpenRecoversQueuedAndRunningJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	store, err := Open(path, 10)
	require.NoError(t, err)
	queued, err := store.Enqueue(context.Background(), Attempt{Action: "queued"})
	require.NoError(t, err)
	running, err := store.Enqueue(context.Background(), Attempt{Action: "running"})
	require.NoError(t, err)
	require.NoError(t, store.Start(context.Background(), running.ID))
	require.NoError(t, store.Close())

	beforeOpen := time.Now().UTC()
	store, err = Open(path, 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	jobs, err := store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	byID := map[uint64]Job{jobs[0].ID: jobs[0], jobs[1].ID: jobs[1]}
	for _, id := range []uint64{queued.ID, running.ID} {
		job := byID[id]
		require.Equal(t, StatusUnknown, job.Status)
		require.NotNil(t, job.FinishedAt)
		require.False(t, job.FinishedAt.Before(beforeOpen))
		require.Empty(t, job.ErrorCategory)
		require.False(t, job.RebootRequired)
	}
	require.Nil(t, byID[queued.ID].StartedAt)
	require.Zero(t, byID[queued.ID].DurationMS)
	require.NotNil(t, byID[running.ID].StartedAt)
	require.GreaterOrEqual(t, byID[running.ID].DurationMS, int64(0))
}

func TestRebootRequiredSinceScansBeyondDisplayLimit(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "jobs.db"), 200)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	bootedAt := time.Now().Add(-time.Hour)
	job, err := store.Enqueue(context.Background(), Attempt{Action: "update"})
	require.NoError(t, err)
	require.NoError(t, store.Start(context.Background(), job.ID))
	require.NoError(t, store.Complete(context.Background(), job.ID, StatusSucceeded, "", true))
	for range 25 {
		other, enqueueErr := store.Enqueue(context.Background(), Attempt{Action: "refresh"})
		require.NoError(t, enqueueErr)
		require.NoError(t, store.Start(context.Background(), other.ID))
		require.NoError(t, store.Complete(context.Background(), other.ID, StatusSucceeded, "", false))
	}
	required, err := store.RebootRequiredSince(context.Background(), bootedAt)
	require.NoError(t, err)
	require.True(t, required)
	required, err = store.RebootRequiredSince(context.Background(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.False(t, required)
}

func TestRetentionKeepsNewestWithoutDeletingActiveJobs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "jobs.db"), 3)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	active, err := store.Enqueue(context.Background(), Attempt{Action: "active"})
	require.NoError(t, err)
	var completed []uint64
	for i := 0; i < 4; i++ {
		job, enqueueErr := store.Enqueue(context.Background(), Attempt{Action: "completed"})
		require.NoError(t, enqueueErr)
		require.NoError(t, store.Start(context.Background(), job.ID))
		require.NoError(t, store.Complete(context.Background(), job.ID, StatusSucceeded, "", false))
		completed = append(completed, job.ID)
	}

	jobs, err := store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Equal(t, []uint64{completed[3], completed[2], active.ID}, jobIDs(jobs))
	require.NoError(t, store.Start(context.Background(), active.ID))
	require.ErrorIs(t, store.Start(context.Background(), completed[0]), ErrNotFound)
}

func TestRetentionCanExceedBoundForActiveJobs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "jobs.db"), 2)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	for i := 0; i < 4; i++ {
		_, err = store.Enqueue(context.Background(), Attempt{Action: "active"})
		require.NoError(t, err)
	}
	jobs, err := store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Len(t, jobs, 4)
}

func TestListFiltersOrdersAndCapsLimit(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "jobs.db"), 150)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	first := enqueueAndComplete(t, store, Attempt{Action: "start"}, StatusSucceeded)
	second := enqueueAndComplete(t, store, Attempt{Action: "restart"}, StatusFailed)
	third := enqueueAndComplete(t, store, Attempt{Action: "start"}, StatusFailed)

	tests := []struct {
		name   string
		filter Filter
		want   []uint64
	}{
		{name: "all", filter: Filter{}, want: []uint64{third.ID, second.ID, first.ID}},
		{name: "action exact", filter: Filter{Action: "start"}, want: []uint64{third.ID, first.ID}},
		{name: "status exact", filter: Filter{Status: StatusFailed}, want: []uint64{third.ID, second.ID}},
		{name: "combined", filter: Filter{Action: "start", Status: StatusFailed}, want: []uint64{third.ID}},
		{name: "limit", filter: Filter{Limit: 2}, want: []uint64{third.ID, second.ID}},
		{name: "no substring", filter: Filter{Action: "star"}, want: []uint64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs, listErr := store.List(context.Background(), tt.filter)
			require.NoError(t, listErr)
			require.Equal(t, tt.want, jobIDs(jobs))
		})
	}

	for i := 0; i < 102; i++ {
		_, err = store.Enqueue(context.Background(), Attempt{Action: "queued"})
		require.NoError(t, err)
	}
	jobs, err := store.List(context.Background(), Filter{Limit: 1000})
	require.NoError(t, err)
	require.Len(t, jobs, 100)

	_, err = store.List(context.Background(), Filter{Limit: -1})
	require.Error(t, err)
	_, err = store.List(context.Background(), Filter{Status: "partial"})
	require.Error(t, err)
}

func TestValidationTransitionsAndCancellation(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "jobs.db"), 0)
	require.Error(t, err)

	store, err := Open(filepath.Join(t.TempDir(), "jobs.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	job, err := store.Enqueue(context.Background(), Attempt{Action: "start"})
	require.NoError(t, err)

	require.Error(t, store.Complete(context.Background(), job.ID, StatusRunning, "", false))
	require.ErrorIs(t, store.Complete(context.Background(), job.ID, StatusSucceeded, "", false), ErrInvalidTransition)
	require.ErrorIs(t, store.Start(context.Background(), job.ID+1), ErrNotFound)
	require.NoError(t, store.Start(context.Background(), job.ID))
	require.ErrorIs(t, store.Start(context.Background(), job.ID), ErrInvalidTransition)
	require.NoError(t, store.Complete(context.Background(), job.ID, StatusSucceeded, "", false))
	require.ErrorIs(t, store.Complete(context.Background(), job.ID, StatusFailed, "", false), ErrInvalidTransition)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Enqueue(ctx, Attempt{})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, store.Start(ctx, job.ID), context.Canceled)
	require.ErrorIs(t, store.Complete(ctx, job.ID, StatusSucceeded, "", false), context.Canceled)
	_, err = store.List(ctx, Filter{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpenUsesAndTightensPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	store, err := Open(path, 10)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoError(t, os.Chmod(path, 0o644))

	store, err = Open(path, 10)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	info, err = os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func enqueueAndComplete(t *testing.T, store *Store, attempt Attempt, status string) Job {
	t.Helper()
	job, err := store.Enqueue(context.Background(), attempt)
	require.NoError(t, err)
	require.NoError(t, store.Start(context.Background(), job.ID))
	require.NoError(t, store.Complete(context.Background(), job.ID, status, "", false))
	return job
}

func jobIDs(jobs []Job) []uint64 {
	ids := make([]uint64, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}
	return ids
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
