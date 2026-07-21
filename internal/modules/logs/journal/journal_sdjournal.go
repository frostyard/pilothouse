//go:build sdjournal

package journal

import (
	"errors"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
)

type journalSource struct {
	journal *sdjournal.Journal
}

func New() Reader {
	return Reader{open: func() (source, error) {
		journal, err := sdjournal.NewJournal()
		if err != nil {
			return nil, err
		}
		// Retain one byte beyond the presentation limit so oversized messages are
		// detected and skipped instead of silently appearing truncated.
		if err := journal.SetDataThreshold(uint64(messageMaxBytes + len("MESSAGE=") + 1)); err != nil {
			_ = journal.Close()
			return nil, err
		}
		return &journalSource{journal: journal}, nil
	}}
}

func (s *journalSource) AddMatch(match string) error { return s.journal.AddMatch(match) }
func (s *journalSource) SeekTail() error             { return s.journal.SeekTail() }
func (s *journalSource) Previous() (uint64, error)   { return s.journal.Previous() }
func (s *journalSource) Close() error                { return s.journal.Close() }

func (s *journalSource) Record() (rawRecord, error) {
	usec, err := s.journal.GetRealtimeUsec()
	if err != nil {
		return rawRecord{}, err
	}
	fields, err := recordFields(s.journal.GetData)
	if err != nil {
		return rawRecord{}, err
	}
	return rawRecord{Timestamp: time.UnixMicro(int64(usec)), Fields: fields}, nil
}

func recordFields(getData func(string) (string, error)) (map[string]string, error) {
	fields := make(map[string]string, 6)
	for _, name := range []string{"PRIORITY", "MESSAGE", "_SYSTEMD_UNIT", "SYSLOG_IDENTIFIER", "_COMM", "_TRANSPORT"} {
		raw, err := getData(name)
		if err != nil {
			if errors.Is(err, syscall.ENOENT) {
				continue
			}
			return nil, err
		}
		field, value, ok := strings.Cut(raw, "=")
		if !ok || field != name {
			return nil, errors.New("journal record has malformed field")
		}
		fields[name] = value
	}
	return fields, nil
}
