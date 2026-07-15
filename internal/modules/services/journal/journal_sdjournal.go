//go:build sdjournal

// Package journal reads systemd journal entries. This file holds the real
// implementation backed by the cgo systemd bindings (go-systemd/sdjournal),
// which require the libsystemd development headers. It is compiled only when
// the "sdjournal" build tag is set; without it, journal_stub.go provides a
// header-free fallback so the daemon (and go vet / test / lint / govulncheck)
// build on toolchains that lack libsystemd-dev.
package journal

import (
	"context"
	"errors"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
	"github.com/frostyard/pilothouse/internal/modules/services"
)

const maxBytes = 256 * 1024

type Reader struct{}

func New() Reader { return Reader{} }

func (Reader) Read(ctx context.Context, unit string, since time.Time, limit int) ([]services.JournalRecord, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, err
	}
	defer func() { _ = j.Close() }()
	if err := j.AddMatch("_SYSTEMD_UNIT=" + unit); err != nil {
		return nil, err
	}
	if err := j.SeekRealtimeUsec(uint64(since.UnixMicro())); err != nil {
		return nil, err
	}
	if err := j.SetDataThreshold(maxBytes + 1); err != nil {
		return nil, err
	}
	records := make([]services.JournalRecord, 0, limit)
	for len(records) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next, err := j.Next()
		if err != nil {
			return nil, err
		}
		if next == 0 {
			break
		}
		priority, err := j.GetDataValue("PRIORITY")
		if err != nil {
			return nil, err
		}
		message, err := j.GetDataValue("MESSAGE")
		if err != nil || len(message) > maxBytes {
			return nil, errors.New("journal entry unavailable")
		}
		recordUnit, err := j.GetDataValue("_SYSTEMD_UNIT")
		if err != nil {
			return nil, err
		}
		timestamp, err := j.GetRealtimeUsec()
		if err != nil {
			return nil, err
		}
		entryTime := time.UnixMicro(int64(timestamp))
		if entryTime.Before(since) {
			continue
		}
		records = append(records, services.JournalRecord{Timestamp: entryTime, Fields: map[string]string{
			"PRIORITY": priority, "MESSAGE": message, "_SYSTEMD_UNIT": recordUnit,
		}})
	}
	return records, nil
}
