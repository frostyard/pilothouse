package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStorePersistsCompletedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := Open(path, 10)
	require.NoError(t, err)

	record, err := store.Begin(context.Background(), Attempt{
		Action:   "restart",
		Resource: "service:sshd",
		Username: "alice",
		UID:      1000,
	})
	require.NoError(t, err)
	require.NotZero(t, record.ID)
	require.Equal(t, OutcomeStarted, record.Outcome)
	require.Nil(t, record.FinishedAt)

	time.Sleep(time.Millisecond)
	require.NoError(t, store.Complete(context.Background(), record.ID, OutcomeFailed, "permission_denied"))
	require.NoError(t, store.Close())

	store, err = Open(path, 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	records, err := store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Len(t, records, 1)
	got := records[0]
	require.Equal(t, record.ID, got.ID)
	require.Equal(t, "restart", got.Action)
	require.Equal(t, "service:sshd", got.Resource)
	require.Equal(t, "alice", got.Username)
	require.Equal(t, 1000, got.UID)
	require.Equal(t, OutcomeFailed, got.Outcome)
	require.Equal(t, "permission_denied", got.ErrorCategory)
	require.NotNil(t, got.FinishedAt)
	require.GreaterOrEqual(t, got.DurationMS, int64(0))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestOpenRecoversStartedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := Open(path, 10)
	require.NoError(t, err)
	record, err := store.Begin(context.Background(), Attempt{Action: "stop", Resource: "container:web"})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	beforeOpen := time.Now().UTC()
	store, err = Open(path, 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	records, err := store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, record.ID, records[0].ID)
	require.Equal(t, OutcomeUnknown, records[0].Outcome)
	require.Empty(t, records[0].ErrorCategory)
	require.NotNil(t, records[0].FinishedAt)
	require.False(t, records[0].FinishedAt.Before(beforeOpen))
	require.GreaterOrEqual(t, records[0].DurationMS, int64(0))
}

func TestStoreRetainsNewestRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := Open(path, 3)
	require.NoError(t, err)

	var ids []uint64
	for i := 0; i < 5; i++ {
		record, beginErr := store.Begin(context.Background(), Attempt{Action: "action"})
		require.NoError(t, beginErr)
		ids = append(ids, record.ID)
		require.NoError(t, store.Complete(context.Background(), record.ID, OutcomeSucceeded, ""))
	}

	records, err := store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Equal(t, []uint64{ids[4], ids[3], ids[2]}, recordIDs(records))
	require.ErrorIs(t, store.Complete(context.Background(), ids[0], OutcomeSucceeded, ""), ErrNotFound)
	require.NoError(t, store.Close())

	store, err = Open(path, 2)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	records, err = store.List(context.Background(), Filter{})
	require.NoError(t, err)
	require.Equal(t, []uint64{ids[4], ids[3]}, recordIDs(records))
}

func TestListFiltersExactlyAndOrdersNewestFirst(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), 200)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	first, err := store.Begin(context.Background(), Attempt{Action: "start", Resource: "one"})
	require.NoError(t, err)
	require.NoError(t, store.Complete(context.Background(), first.ID, OutcomeSucceeded, ""))
	second, err := store.Begin(context.Background(), Attempt{Action: "restart", Resource: "two"})
	require.NoError(t, err)
	require.NoError(t, store.Complete(context.Background(), second.ID, OutcomeFailed, "timeout"))
	third, err := store.Begin(context.Background(), Attempt{Action: "start", Resource: "three"})
	require.NoError(t, err)
	require.NoError(t, store.Complete(context.Background(), third.ID, OutcomeFailed, "conflict"))

	tests := []struct {
		name   string
		filter Filter
		want   []uint64
	}{
		{name: "all", filter: Filter{}, want: []uint64{third.ID, second.ID, first.ID}},
		{name: "action exact", filter: Filter{Action: "start"}, want: []uint64{third.ID, first.ID}},
		{name: "outcome exact", filter: Filter{Outcome: OutcomeFailed}, want: []uint64{third.ID, second.ID}},
		{name: "combined", filter: Filter{Action: "start", Outcome: OutcomeFailed}, want: []uint64{third.ID}},
		{name: "limit", filter: Filter{Limit: 2}, want: []uint64{third.ID, second.ID}},
		{name: "no substring match", filter: Filter{Action: "star"}, want: []uint64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records, listErr := store.List(context.Background(), tt.filter)
			require.NoError(t, listErr)
			require.Equal(t, tt.want, recordIDs(records))
		})
	}
}

func TestListCapsLimitAndValidatesFilter(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), 150)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	for i := 0; i < 105; i++ {
		_, err = store.Begin(context.Background(), Attempt{Action: "inspect"})
		require.NoError(t, err)
	}
	records, err := store.List(context.Background(), Filter{Limit: 1000})
	require.NoError(t, err)
	require.Len(t, records, 100)

	_, err = store.List(context.Background(), Filter{Limit: -1})
	require.Error(t, err)
	_, err = store.List(context.Background(), Filter{Outcome: "partial"})
	require.Error(t, err)
}

func TestValidationAndCanceledContexts(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "audit.db"), 0)
	require.Error(t, err)

	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	record, err := store.Begin(context.Background(), Attempt{Action: "start"})
	require.NoError(t, err)
	require.Error(t, store.Complete(context.Background(), record.ID, OutcomeStarted, ""))
	require.ErrorIs(t, store.Complete(context.Background(), record.ID+1, OutcomeSucceeded, ""), ErrNotFound)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Begin(ctx, Attempt{})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, store.Complete(ctx, record.ID, OutcomeSucceeded, ""), context.Canceled)
	_, err = store.List(ctx, Filter{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpenTightensExistingFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := Open(path, 10)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, os.Chmod(path, 0o644))

	store, err = Open(path, 10)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func recordIDs(records []Record) []uint64 {
	ids := make([]uint64, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	return ids
}
