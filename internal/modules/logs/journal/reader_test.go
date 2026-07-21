package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/modules/logs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)

type fakeSource struct {
	records   []rawRecord
	index     int
	match     string
	err       error
	recordErr error
}

func (f *fakeSource) AddMatch(match string) error { f.match = match; return nil }
func (*fakeSource) SeekTail() error               { return nil }
func (f *fakeSource) Previous() (uint64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.index >= len(f.records) {
		return 0, nil
	}
	f.index++
	return 1, nil
}
func (f *fakeSource) Record() (rawRecord, error) {
	if f.recordErr != nil {
		return rawRecord{}, f.recordErr
	}
	return f.records[f.index-1], nil
}
func (*fakeSource) Close() error { return nil }

func TestReaderReturnsNewestMatchingEntries(t *testing.T) {
	source := &fakeSource{records: []rawRecord{
		record("panic NOW", "3", "sshd.service"),
		record("PANIC again", "4", "sshd.service"),
		record("PANIC later", "5", "sshd.service"),
	}}
	result, err := testReader(source).Read(context.Background(), logs.Filters{Query: "panic", Priority: "warning", Window: "1h"}, limits())

	require.NoError(t, err)
	assert.Equal(t, []logs.Entry{
		{Timestamp: fixedNow.Add(-time.Minute), Priority: 3, Severity: "err", Source: "sshd.service", Message: "panic NOW"},
		{Timestamp: fixedNow.Add(-time.Minute), Priority: 4, Severity: "warning", Source: "sshd.service", Message: "PANIC again"},
	}, result.Entries)
	assert.False(t, result.Truncated)
}

func TestReaderUsesExactUnitMatchAndSourceFallback(t *testing.T) {
	t.Run("exact unit match", func(t *testing.T) {
		source := &fakeSource{records: []rawRecord{record("kernel warning", "4", "sshd.service")}}
		result, err := testReader(source).Read(context.Background(), logs.Filters{Unit: "sshd.service", Window: "1h"}, limits())

		require.NoError(t, err)
		assert.Equal(t, "_SYSTEMD_UNIT=sshd.service", source.match)
		assert.Len(t, result.Entries, 1)
	})

	for _, tc := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"systemd unit", map[string]string{"_SYSTEMD_UNIT": "sshd.service"}, "sshd.service"},
		{"syslog identifier", map[string]string{"SYSLOG_IDENTIFIER": "sshd"}, "sshd"},
		{"command", map[string]string{"_COMM": "sshd"}, "sshd"},
		{"transport", map[string]string{"_TRANSPORT": "kernel"}, "kernel"},
		{"unknown", nil, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]string{"MESSAGE": "routine", "PRIORITY": "6"}
			for key, value := range tc.fields {
				fields[key] = value
			}
			result, err := testReader(&fakeSource{records: []rawRecord{{Timestamp: fixedNow.Add(-time.Minute), Fields: fields}}}).Read(context.Background(), logs.Filters{Window: "1h"}, limits())

			require.NoError(t, err)
			require.Len(t, result.Entries, 1)
			assert.Equal(t, tc.want, result.Entries[0].Source)
		})
	}
}

func TestReaderAllowsRecordsWithoutSystemdUnit(t *testing.T) {
	result, err := testReader(&fakeSource{records: []rawRecord{{Timestamp: fixedNow.Add(-time.Minute), Fields: map[string]string{"MESSAGE": "kernel warning", "PRIORITY": "4", "_TRANSPORT": "kernel"}}}}).Read(context.Background(), logs.Filters{Window: "1h"}, limits())

	require.NoError(t, err)
	assert.Equal(t, "kernel", result.Entries[0].Source)
}

func TestReaderMarksCountScanAndByteLimitsTruncated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits logs.JournalLimits
	}{
		{"entry", logs.JournalLimits{EntryLimit: 1, ScanLimit: 10, MaxBytes: 4096}},
		{"scan", logs.JournalLimits{EntryLimit: 10, ScanLimit: 1, MaxBytes: 4096}},
		{"bytes", logs.JournalLimits{EntryLimit: 10, ScanLimit: 10, MaxBytes: len("routine") + len("sshd.service") + 64 + len("kernel warning") + len("sshd.service") + 64 - 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := testReader(&fakeSource{records: []rawRecord{record("routine", "6", "sshd.service"), record("kernel warning", "4", "sshd.service")}}).Read(context.Background(), logs.Filters{Window: "1h"}, tc.limits)

			require.NoError(t, err)
			assert.Len(t, result.Entries, 1)
			assert.True(t, result.Truncated)
		})
	}
}

func TestReaderStopsAtWindowWithoutTruncation(t *testing.T) {
	result, err := testReader(&fakeSource{records: []rawRecord{record("routine", "6", "sshd.service", fixedNow.Add(-16*time.Minute))}}).Read(context.Background(), logs.Filters{Window: "15m"}, limits())

	require.NoError(t, err)
	assert.Empty(t, result.Entries)
	assert.False(t, result.Truncated)
}

func TestReaderFailsClosed(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		source  *fakeSource
		filters logs.Filters
	}{
		{"canceled context", canceled, &fakeSource{}, logs.Filters{Window: "1h"}},
		{"missing message", context.Background(), &fakeSource{records: []rawRecord{{Timestamp: fixedNow, Fields: map[string]string{"PRIORITY": "6"}}}}, logs.Filters{Window: "1h"}},
		{"malformed priority", context.Background(), &fakeSource{records: []rawRecord{record("routine", "bad", "sshd.service")}}, logs.Filters{Window: "1h"}},
		{"zero timestamp", context.Background(), &fakeSource{records: []rawRecord{{Fields: map[string]string{"MESSAGE": "routine", "PRIORITY": "6"}}}}, logs.Filters{Window: "1h"}},
		{"selected unit mismatch", context.Background(), &fakeSource{records: []rawRecord{record("routine", "6", "cron.service")}}, logs.Filters{Unit: "sshd.service", Window: "1h"}},
		{"long message", context.Background(), &fakeSource{records: []rawRecord{record(strings.Repeat("x", 64*1024+1), "6", "sshd.service")}}, logs.Filters{Window: "1h"}},
		{"long source", context.Background(), &fakeSource{records: []rawRecord{record("routine", "6", strings.Repeat("x", 4*1024+1))}}, logs.Filters{Window: "1h"}},
		{"previous error", context.Background(), &fakeSource{err: errors.New("previous")}, logs.Filters{Window: "1h"}},
		{"record error", context.Background(), &fakeSource{records: []rawRecord{record("routine", "6", "sshd.service")}, recordErr: errors.New("record")}, logs.Filters{Window: "1h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := testReader(tc.source).Read(tc.ctx, tc.filters, limits())

			assert.Error(t, err)
			assert.Equal(t, logs.JournalResult{}, result)
		})
	}
}

func testReader(journalSource source) Reader {
	return Reader{now: func() time.Time { return fixedNow }, open: func() (source, error) { return journalSource, nil }}
}

func limits() logs.JournalLimits {
	return logs.JournalLimits{EntryLimit: 10, ScanLimit: 10, MaxBytes: 4096}
}

func record(message, priority, unit string, timestamps ...time.Time) rawRecord {
	timestamp := fixedNow.Add(-time.Minute)
	if len(timestamps) > 0 {
		timestamp = timestamps[0]
	}
	return rawRecord{Timestamp: timestamp, Fields: map[string]string{"MESSAGE": message, "PRIORITY": priority, "_SYSTEMD_UNIT": unit}}
}
