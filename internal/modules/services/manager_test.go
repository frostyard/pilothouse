package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	statuses []dbus.UnitStatus
	files    []dbus.UnitFile
	stopped  string
}

type fakeJournalReader struct {
	records []JournalRecord
	err     error
	unit    string
	since   time.Time
	limit   int
	calls   int
}

func (f *fakeJournalReader) Read(_ context.Context, unit string, since time.Time, limit int) ([]JournalRecord, error) {
	f.calls++
	f.unit, f.since, f.limit = unit, since, limit
	return f.records, f.err
}

func (f *fakeClient) DisableUnitFilesContext(context.Context, []string, bool) ([]dbus.DisableUnitFileChange, error) {
	return nil, nil
}

func TestJournalValidatesResolvesBoundsAndMapsEntries(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeJournalReader{records: []JournalRecord{
		{Timestamp: now.Add(-2 * time.Hour), Fields: map[string]string{"PRIORITY": "6", "MESSAGE": "too old", "_SYSTEMD_UNIT": "backup.timer"}},
		{Timestamp: now, Fields: map[string]string{"PRIORITY": "3", "MESSAGE": "failed safely", "_SYSTEMD_UNIT": "backup.timer", "SECRET": "not exposed"}},
	}}
	manager := newSystemManagerWithJournal(&fakeClient{statuses: []dbus.UnitStatus{{Name: "backup.timer", Description: "Nightly backup"}}}, reader)
	journal, err := manager.Journal(context.Background(), "backup.timer")
	require.NoError(t, err)
	assert.Equal(t, "Nightly backup", journal.Description)
	assert.Equal(t, []JournalEntry{{Timestamp: now, Priority: 3, Severity: "err", Message: "failed safely", Unit: "backup.timer"}}, journal.Entries)
	assert.Equal(t, "backup.timer", reader.unit)
	assert.Equal(t, journalLimit, reader.limit)
	assert.WithinDuration(t, time.Now().Add(-journalWindow), reader.since, time.Second)

	for _, invalid := range []string{"missing.scope", "../evil.service", "bad.service.extra", ""} {
		_, err := manager.Journal(context.Background(), invalid)
		assert.Error(t, err)
	}
	assert.Equal(t, 1, reader.calls)
}

func TestJournalRejectsUnknownMalformedOversizedAndReaderErrors(t *testing.T) {
	client := &fakeClient{}
	reader := &fakeJournalReader{}
	manager := newSystemManagerWithJournal(client, reader)
	_, err := manager.Journal(context.Background(), "missing.service")
	assert.ErrorContains(t, err, "does not exist")
	assert.Zero(t, reader.calls)

	client.statuses = []dbus.UnitStatus{{Name: "known.service"}}
	cases := []struct {
		name    string
		records []JournalRecord
		err     error
	}{
		{"unavailable", nil, errors.New("journal details must not leak")},
		{"malformed priority", []JournalRecord{{Timestamp: time.Now(), Fields: map[string]string{"PRIORITY": "loud", "MESSAGE": "partial", "_SYSTEMD_UNIT": "known.service"}}}, nil},
		{"wrong unit", []JournalRecord{{Timestamp: time.Now(), Fields: map[string]string{"PRIORITY": "4", "MESSAGE": "partial", "_SYSTEMD_UNIT": "other.service"}}}, nil},
		{"oversized", []JournalRecord{{Timestamp: time.Now(), Fields: map[string]string{"PRIORITY": "4", "MESSAGE": strings.Repeat("x", journalMaxBytes), "_SYSTEMD_UNIT": "known.service"}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader.records, reader.err = tc.records, tc.err
			journal, err := manager.Journal(context.Background(), "known.service")
			assert.ErrorIs(t, err, errJournalUnavailable)
			assert.Empty(t, journal.Entries)
			assert.NotContains(t, err.Error(), "partial")
		})
	}
}
func (f *fakeClient) EnableUnitFilesContext(context.Context, []string, bool, bool) (bool, []dbus.EnableUnitFileChange, error) {
	return true, nil, nil
}
func (f *fakeClient) ListUnitFilesContext(context.Context) ([]dbus.UnitFile, error) {
	return f.files, nil
}
func (f *fakeClient) ListUnitsByPatternsContext(context.Context, []string, []string) ([]dbus.UnitStatus, error) {
	return f.statuses, nil
}
func (f *fakeClient) ResetFailedUnitContext(context.Context, string) error { return nil }
func (f *fakeClient) RestartUnitContext(context.Context, string, string, chan<- string) (int, error) {
	return 1, nil
}
func (f *fakeClient) StartUnitContext(context.Context, string, string, chan<- string) (int, error) {
	return 1, nil
}
func (f *fakeClient) StopUnitContext(_ context.Context, name, _ string, _ chan<- string) (int, error) {
	f.stopped = name
	return 1, nil
}

func TestStateFiltersAndSummarizesSupportedUnits(t *testing.T) {
	manager := newSystemManager(&fakeClient{
		statuses: []dbus.UnitStatus{{Name: "backup.timer", ActiveState: "active", Description: "Backup"}, {Name: "broken.service", ActiveState: "failed"}, {Name: "session.scope", ActiveState: "active"}},
		files:    []dbus.UnitFile{{Path: "/etc/systemd/system/backup.timer", Type: "enabled"}, {Path: "/usr/lib/systemd/system/idle.service", Type: "disabled"}},
	})
	state, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Summary{Total: 3, Active: 1, Failed: 1}, state.Summary)
	assert.Equal(t, "backup.timer", state.Units[0].Name)
	assert.Equal(t, "enabled", state.Units[0].UnitFileState)
	assert.Equal(t, Unit{Name: "idle.service", Description: "idle.service", LoadState: "not-found", ActiveState: "inactive", SubState: "dead", UnitFileState: "disabled"}, state.Units[2])
}

func TestProtectedAndMalformedUnitMutationsAreRejected(t *testing.T) {
	client := &fakeClient{files: []dbus.UnitFile{{Path: "/etc/systemd/system/backup.timer"}}}
	manager := newSystemManager(client)
	assert.Error(t, manager.Stop(context.Background(), "pilothouse.service"))
	assert.Error(t, manager.Disable(context.Background(), "pilothoused.service"))
	assert.Error(t, manager.Stop(context.Background(), "../evil.service"))
	assert.NoError(t, manager.Stop(context.Background(), "backup.timer"))
	assert.Equal(t, "backup.timer", client.stopped)
	assert.Error(t, manager.Start(context.Background(), "missing.service"))
}
