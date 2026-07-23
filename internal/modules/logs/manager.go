package logs

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-systemd/v22/dbus"
)

const (
	journalEntryLimit = 200
	journalScanLimit  = 10_000
	journalMaxBytes   = 256 * 1024
	journalTimeout    = 4 * time.Second
	queryMaxBytes     = 1024
	queryMaxRunes     = 200
)

var (
	errLogsUnavailable = errors.New("system journal is unavailable")
	priorityNumbers    = map[string]int{
		"emerg": 0, "alert": 1, "crit": 2, "err": 3,
		"warning": 4, "notice": 5, "info": 6, "debug": 7,
	}
	windowDurations = map[string]time.Duration{
		"15m": 15 * time.Minute, "1h": time.Hour,
		"6h": 6 * time.Hour, "24h": 24 * time.Hour,
	}
	unitNamePattern = regexp.MustCompile(`^(?:[A-Za-z0-9:_.@\-]|\\x[0-9A-Fa-f]{2})+\.(service|socket|target|device|mount|automount|swap|timer|path|slice|scope)$`)
)

type Filters struct {
	Query    string `json:"query"`
	Priority string `json:"priority"`
	Unit     string `json:"unit"`
	Window   string `json:"window"`
}

type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Priority  int       `json:"priority"`
	Severity  string    `json:"severity"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
}

type State struct {
	Entries   []Entry  `json:"entries"`
	Filters   Filters  `json:"filters"`
	Truncated bool     `json:"truncated"`
	Units     []string `json:"units"`
}

type JournalLimits struct {
	EntryLimit int
	ScanLimit  int
	MaxBytes   int
}

type JournalResult struct {
	Entries   []Entry
	Truncated bool
}

type JournalReader interface {
	Read(context.Context, Filters, JournalLimits) (JournalResult, error)
}

type Manager interface {
	Logs(context.Context, Filters) (State, error)
}

type systemdClient interface {
	ListUnitsContext(context.Context) ([]dbus.UnitStatus, error)
	ListUnitFilesContext(context.Context) ([]dbus.UnitFile, error)
}

type SystemManager struct {
	client  systemdClient
	journal JournalReader
}

func ParseBrokerFilters(parameters map[string]string) (Filters, error) {
	for key := range parameters {
		if key != "query" && key != "priority" && key != "unit" && key != "window" {
			return Filters{}, errors.New("invalid logs filter")
		}
	}
	filters := Filters{
		Query:    strings.TrimSpace(parameters["query"]),
		Priority: parameters["priority"],
		Unit:     parameters["unit"],
		Window:   parameters["window"],
	}
	if filters.Window == "" {
		filters.Window = "1h"
	}
	if err := validFilters(filters); err != nil {
		return Filters{}, err
	}
	return filters, nil
}

func PriorityNumber(value string) (int, bool) {
	number, ok := priorityNumbers[value]
	return number, ok
}

func WindowDuration(value string) time.Duration {
	return windowDurations[value]
}

func (f Filters) Active() bool {
	return f.Query != "" || f.Priority != "" || f.Unit != "" || f.Window != "1h"
}

func validFilters(filters Filters) error {
	if utf8.RuneCountInString(filters.Query) > queryMaxRunes || len(filters.Query) > queryMaxBytes || strings.Contains(filters.Query, "\x00") {
		return errors.New("invalid logs query")
	}
	if filters.Priority != "" {
		if _, ok := PriorityNumber(filters.Priority); !ok {
			return errors.New("invalid logs priority")
		}
	}
	if filters.Unit != "" && !validUnitName(filters.Unit) {
		return errors.New("invalid systemd unit name")
	}
	if WindowDuration(filters.Window) == 0 {
		return errors.New("invalid logs window")
	}
	return nil
}

func validUnitName(name string) bool {
	lowerName := strings.ToLower(name)
	return unitNamePattern.MatchString(name) && !strings.Contains(name, "..") && !strings.Contains(name, "/") && !strings.Contains(lowerName, "\\x2f") && !strings.Contains(lowerName, "\\x00")
}

// NewSystemManager builds a logs manager from a pre-opened systemd D-Bus
// client. The caller (cmd/pilothoused) is responsible for opening that
// connection -- this package no longer dials systemd itself, so
// construction can never fail because systemd is absent or unreachable.
func NewSystemManager(client systemdClient, reader JournalReader) (*SystemManager, error) {
	if client == nil {
		return nil, errors.New("systemd client is required")
	}
	return newSystemManager(client, reader), nil
}

func newSystemManager(client systemdClient, reader JournalReader) *SystemManager {
	return &SystemManager{client: client, journal: reader}
}

func (m *SystemManager) Logs(ctx context.Context, filters Filters) (State, error) {
	if err := validFilters(filters); err != nil {
		return State{}, err
	}
	statuses, err := m.client.ListUnitsContext(ctx)
	if err != nil {
		return State{}, errLogsUnavailable
	}
	files, err := m.client.ListUnitFilesContext(ctx)
	if err != nil {
		return State{}, errLogsUnavailable
	}
	unitNames := make(map[string]struct{}, len(statuses)+len(files))
	for _, status := range statuses {
		if validUnitName(status.Name) {
			unitNames[status.Name] = struct{}{}
		}
	}
	for _, file := range files {
		name := filepath.Base(file.Path)
		if validUnitName(name) {
			unitNames[name] = struct{}{}
		}
	}
	units := make([]string, 0, len(unitNames))
	for name := range unitNames {
		units = append(units, name)
	}
	slices.Sort(units)

	readCtx, cancel := context.WithTimeout(ctx, journalTimeout)
	defer cancel()
	result, err := m.journal.Read(readCtx, filters, JournalLimits{
		EntryLimit: journalEntryLimit,
		ScanLimit:  journalScanLimit,
		MaxBytes:   journalMaxBytes,
	})
	if err != nil {
		return State{}, errLogsUnavailable
	}
	if result.Entries == nil {
		result.Entries = make([]Entry, 0)
	}
	return State{Entries: result.Entries, Filters: filters, Truncated: result.Truncated, Units: units}, nil
}
