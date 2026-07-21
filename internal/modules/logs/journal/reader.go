package journal

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/modules/logs"
)

const (
	messageMaxBytes = 64 * 1024
	sourceMaxBytes  = 4 * 1024
)

var errUnavailable = errors.New("system journal reader unavailable")

type rawRecord struct {
	Timestamp time.Time
	Fields    map[string]string
}

type source interface {
	AddMatch(string) error
	SeekTail() error
	Previous() (uint64, error)
	Record() (rawRecord, error)
	Close() error
}

type Reader struct {
	now  func() time.Time
	open func() (source, error)
}

func (r Reader) Read(ctx context.Context, filters logs.Filters, limits logs.JournalLimits) (logs.JournalResult, error) {
	source, err := r.open()
	if err != nil {
		return logs.JournalResult{}, errUnavailable
	}
	defer func() { _ = source.Close() }()

	if filters.Unit != "" {
		if err := source.AddMatch("_SYSTEMD_UNIT=" + filters.Unit); err != nil {
			return logs.JournalResult{}, err
		}
	}
	if err := source.SeekTail(); err != nil {
		return logs.JournalResult{}, err
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	boundary := now().Add(-logs.WindowDuration(filters.Window))
	result := logs.JournalResult{Entries: make([]logs.Entry, 0)}
	inspected, aggregateBytes := 0, 0

	for {
		if err := ctx.Err(); err != nil {
			return logs.JournalResult{}, err
		}
		next, err := source.Previous()
		if err != nil {
			return logs.JournalResult{}, err
		}
		if next == 0 {
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return logs.JournalResult{}, err
		}
		record, err := source.Record()
		if err != nil {
			return logs.JournalResult{}, err
		}
		inspected++
		if !record.Timestamp.IsZero() && record.Timestamp.Before(boundary) {
			return result, nil
		}
		journalEntry, matched := entry(record, filters)
		if matched {
			entryBytes := len(journalEntry.Message) + len(journalEntry.Source) + 64
			if entryBytes > limits.MaxBytes-aggregateBytes {
				result.Truncated = true
				return result, nil
			}
			result.Entries = append(result.Entries, journalEntry)
			aggregateBytes += entryBytes
			if len(result.Entries) >= limits.EntryLimit {
				result.Truncated = true
				return result, nil
			}
		}
		if inspected >= limits.ScanLimit {
			result.Truncated = true
			return result, nil
		}
	}
}

func entry(record rawRecord, filters logs.Filters) (logs.Entry, bool) {
	if record.Timestamp.IsZero() {
		return logs.Entry{}, false
	}
	priority, err := strconv.Atoi(record.Fields["PRIORITY"])
	if err != nil || priority < 0 || priority > 7 {
		return logs.Entry{}, false
	}
	message, ok := record.Fields["MESSAGE"]
	if !ok || len(message) > messageMaxBytes {
		return logs.Entry{}, false
	}
	if filters.Unit != "" && record.Fields["_SYSTEMD_UNIT"] != filters.Unit {
		return logs.Entry{}, false
	}
	source := "unknown"
	for _, field := range []string{"_SYSTEMD_UNIT", "SYSLOG_IDENTIFIER", "_COMM", "_TRANSPORT"} {
		if len(record.Fields[field]) > sourceMaxBytes {
			return logs.Entry{}, false
		}
		if source == "unknown" && record.Fields[field] != "" {
			source = record.Fields[field]
		}
	}
	if filters.Query != "" && !strings.Contains(strings.ToLower(message), strings.ToLower(filters.Query)) {
		return logs.Entry{}, false
	}
	threshold := 7
	if filters.Priority != "" {
		var ok bool
		threshold, ok = logs.PriorityNumber(filters.Priority)
		if !ok {
			return logs.Entry{}, false
		}
	}
	if priority > threshold {
		return logs.Entry{}, false
	}
	return logs.Entry{Timestamp: record.Timestamp, Priority: priority, Severity: []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}[priority], Source: source, Message: message}, true
}
