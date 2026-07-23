package logs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSystemdClient struct {
	statuses []dbus.UnitStatus
	files    []dbus.UnitFile
	unitsErr error
	filesErr error
}

func (f *fakeSystemdClient) ListUnitsContext(context.Context) ([]dbus.UnitStatus, error) {
	return f.statuses, f.unitsErr
}

func (f *fakeSystemdClient) ListUnitFilesContext(context.Context) ([]dbus.UnitFile, error) {
	return f.files, f.filesErr
}

type fakeJournalReader struct {
	filters Filters
	limits  JournalLimits
	result  JournalResult
	err     error
	calls   int
}

func (f *fakeJournalReader) Read(_ context.Context, filters Filters, limits JournalLimits) (JournalResult, error) {
	f.calls++
	f.filters, f.limits = filters, limits
	return f.result, f.err
}

func TestParseBrokerFiltersAcceptsOnlyFixedGrammar(t *testing.T) {
	valid, err := ParseBrokerFilters(map[string]string{
		"query": "panic", "priority": "warning", "unit": "session\\x2d4.scope", "window": "6h",
	})
	require.NoError(t, err)
	assert.Equal(t, Filters{Query: "panic", Priority: "warning", Unit: "session\\x2d4.scope", Window: "6h"}, valid)

	defaults, err := ParseBrokerFilters(nil)
	require.NoError(t, err)
	assert.Equal(t, Filters{Window: "1h"}, defaults)

	invalid := []map[string]string{
		{"unexpected": "value"},
		{"priority": "verbose"},
		{"window": "7d"},
		{"unit": "../system.service"},
		{"unit": "system\\x2fjournal.service"},
		{"unit": "missing.suffix"},
		{"query": strings.Repeat("x", 1025)},
		{"query": strings.Repeat("界", 201)},
	}
	for _, parameters := range invalid {
		_, err := ParseBrokerFilters(parameters)
		assert.Error(t, err)
	}
}

func TestSystemManagerListsAllUnitsAndPassesFixedLimits(t *testing.T) {
	reader := &fakeJournalReader{result: JournalResult{Entries: []Entry{{Message: "ready"}}, Truncated: true}}
	manager := newSystemManager(&fakeSystemdClient{
		statuses: []dbus.UnitStatus{{Name: "session-4.scope"}, {Name: "var.mount"}},
		files: []dbus.UnitFile{
			{Path: "/usr/lib/systemd/system/sshd.service"},
			{Path: "/etc/systemd/system/var.mount"},
		},
	}, reader)
	filters := Filters{Query: "ready", Priority: "info", Unit: "session-4.scope", Window: "15m"}
	state, err := manager.Logs(context.Background(), filters)
	require.NoError(t, err)
	assert.Equal(t, []string{"session-4.scope", "sshd.service", "var.mount"}, state.Units)
	assert.Equal(t, reader.result.Entries, state.Entries)
	assert.True(t, state.Truncated)
	assert.Equal(t, filters, reader.filters)
	assert.Equal(t, JournalLimits{EntryLimit: 200, ScanLimit: 10_000, MaxBytes: 256 * 1024}, reader.limits)
}

func TestNewSystemManagerAcceptsPreOpenedClientAndRejectsNil(t *testing.T) {
	manager, err := NewSystemManager(&fakeSystemdClient{}, &fakeJournalReader{})
	require.NoError(t, err)
	require.NotNil(t, manager)

	_, err = NewSystemManager(nil, &fakeJournalReader{})
	assert.Error(t, err)
}

func TestSystemManagerFailsClosedForInvalidFiltersAndDependencies(t *testing.T) {
	t.Run("invalid filters do not invoke the reader", func(t *testing.T) {
		reader := &fakeJournalReader{}
		manager := newSystemManager(&fakeSystemdClient{}, reader)

		_, err := manager.Logs(context.Background(), Filters{Unit: "../bad.service"})

		assert.Error(t, err)
		assert.Zero(t, reader.calls)
	})

	for _, tc := range []struct {
		name    string
		client  *fakeSystemdClient
		reader  *fakeJournalReader
		filters Filters
	}{
		{
			name:    "unit list failure",
			client:  &fakeSystemdClient{unitsErr: errors.New("secret backend detail")},
			reader:  &fakeJournalReader{},
			filters: Filters{Window: "1h"},
		},
		{
			name:    "unit file list failure",
			client:  &fakeSystemdClient{filesErr: errors.New("secret backend detail")},
			reader:  &fakeJournalReader{},
			filters: Filters{Window: "1h"},
		},
		{
			name:    "reader failure",
			client:  &fakeSystemdClient{},
			reader:  &fakeJournalReader{err: errors.New("secret backend detail")},
			filters: Filters{Window: "1h"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := newSystemManager(tc.client, tc.reader)

			_, err := manager.Logs(context.Background(), tc.filters)

			assert.ErrorIs(t, err, errLogsUnavailable)
			assert.NotContains(t, err.Error(), "secret backend detail")
		})
	}
}
