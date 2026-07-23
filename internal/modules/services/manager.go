package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

var unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9:_.@-]+\.(service|socket|timer)$`)

var protectedUnits = map[string]bool{"pilothouse.service": true, "pilothoused.service": true}

type Unit struct {
	ActiveState   string `json:"active_state"`
	Description   string `json:"description"`
	LoadState     string `json:"load_state"`
	Name          string `json:"name"`
	SubState      string `json:"sub_state"`
	UnitFileState string `json:"unit_file_state"`
}

type Summary struct {
	Active int `json:"active"`
	Failed int `json:"failed"`
	Total  int `json:"total"`
}

type State struct {
	Summary Summary `json:"summary"`
	Units   []Unit  `json:"units"`
}

type JournalEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Priority  int       `json:"priority"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	Unit      string    `json:"unit"`
}

type Journal struct {
	Unit        string         `json:"unit"`
	Description string         `json:"description"`
	Entries     []JournalEntry `json:"entries"`
}

type Manager interface {
	Disable(context.Context, string) error
	Enable(context.Context, string) error
	Journal(context.Context, string) (Journal, error)
	ResetFailed(context.Context, string) error
	Restart(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context) (State, error)
	Stop(context.Context, string) error
}

type JournalRecord struct {
	Timestamp time.Time
	Fields    map[string]string
}

type JournalReader interface {
	Read(context.Context, string, time.Time, int) ([]JournalRecord, error)
}

type systemdClient interface {
	DisableUnitFilesContext(context.Context, []string, bool) ([]dbus.DisableUnitFileChange, error)
	EnableUnitFilesContext(context.Context, []string, bool, bool) (bool, []dbus.EnableUnitFileChange, error)
	ListUnitFilesContext(context.Context) ([]dbus.UnitFile, error)
	ListUnitsByPatternsContext(context.Context, []string, []string) ([]dbus.UnitStatus, error)
	ResetFailedUnitContext(context.Context, string) error
	RestartUnitContext(context.Context, string, string, chan<- string) (int, error)
	StartUnitContext(context.Context, string, string, chan<- string) (int, error)
	StopUnitContext(context.Context, string, string, chan<- string) (int, error)
}

type SystemManager struct {
	client  systemdClient
	journal JournalReader
}

const (
	journalLimit    = 200
	journalMaxBytes = 256 * 1024
	journalTimeout  = 5 * time.Second
	journalWindow   = time.Hour
)

var errJournalUnavailable = errors.New("recent service diagnostics are unavailable")

// NewSystemManager builds a services manager from a pre-opened systemd
// D-Bus client. The caller (cmd/pilothoused) is responsible for opening
// that connection -- this package no longer dials systemd itself, so
// construction can never fail because systemd is absent or unreachable.
func NewSystemManager(client systemdClient, journal JournalReader) (*SystemManager, error) {
	if client == nil {
		return nil, errors.New("systemd client is required")
	}
	return newSystemManagerWithJournal(client, journal), nil
}

func newSystemManager(client systemdClient) *SystemManager { return &SystemManager{client: client} }

func newSystemManagerWithJournal(client systemdClient, journal JournalReader) *SystemManager {
	return &SystemManager{client: client, journal: journal}
}

func (m *SystemManager) Journal(ctx context.Context, name string) (Journal, error) {
	if !validUnitName(name) {
		return Journal{}, errors.New("invalid systemd unit name")
	}
	unit, err := m.resolveUnit(ctx, name)
	if err != nil {
		return Journal{}, err
	}
	readCtx, cancel := context.WithTimeout(ctx, journalTimeout)
	defer cancel()
	since := time.Now().Add(-journalWindow)
	records, err := m.journal.Read(readCtx, name, since, journalLimit)
	if err != nil {
		return Journal{}, errJournalUnavailable
	}
	if len(records) > journalLimit {
		records = records[:journalLimit]
	}
	result := Journal{Unit: name, Description: unit.Description, Entries: make([]JournalEntry, 0, len(records))}
	totalBytes := 0
	for _, record := range records {
		if record.Timestamp.Before(since) {
			continue
		}
		entry, ok := parseJournalRecord(record, name)
		if !ok {
			return Journal{}, errJournalUnavailable
		}
		totalBytes += len(entry.Message) + len(entry.Unit) + 64
		if totalBytes > journalMaxBytes {
			return Journal{}, errJournalUnavailable
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

var journalSeverities = [...]string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// parseJournalRecord validates a raw journal record against the field whitelist
// and maps it to a JournalEntry. Any missing or malformed field, or a unit
// mismatch, reports ok as false so the caller fails closed instead of leaking
// partial or unexpected journal data.
func parseJournalRecord(record JournalRecord, unit string) (entry JournalEntry, ok bool) {
	priorityText, hasPriority := record.Fields["PRIORITY"]
	message, hasMessage := record.Fields["MESSAGE"]
	recordUnit, hasUnit := record.Fields["_SYSTEMD_UNIT"]
	priority, parseErr := strconv.Atoi(priorityText)
	if !hasPriority || !hasMessage || !hasUnit || parseErr != nil || priority < 0 || priority > 7 || record.Timestamp.IsZero() || recordUnit != unit {
		return JournalEntry{}, false
	}
	return JournalEntry{Timestamp: record.Timestamp, Priority: priority, Severity: journalSeverities[priority], Message: message, Unit: recordUnit}, true
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	statuses, err := m.client.ListUnitsByPatternsContext(ctx, nil, []string{"*.service", "*.socket", "*.timer"})
	if err != nil {
		return State{}, err
	}
	files, err := m.client.ListUnitFilesContext(ctx)
	if err != nil {
		return State{}, err
	}
	unitsByName := make(map[string]Unit, len(statuses)+len(files))
	for _, file := range files {
		name := filepath.Base(file.Path)
		if validUnitName(name) {
			unitsByName[name] = Unit{
				Name:          name,
				Description:   name,
				LoadState:     "not-found",
				ActiveState:   "inactive",
				SubState:      "dead",
				UnitFileState: file.Type,
			}
		}
	}
	for _, status := range statuses {
		if !validUnitName(status.Name) {
			continue
		}
		unit := unitsByName[status.Name]
		unit.Name = status.Name
		unit.Description = status.Description
		unit.LoadState = status.LoadState
		unit.ActiveState = status.ActiveState
		unit.SubState = status.SubState
		unitsByName[status.Name] = unit
	}
	units := make([]Unit, 0, len(unitsByName))
	for _, unit := range unitsByName {
		units = append(units, unit)
	}
	slices.SortFunc(units, func(a, b Unit) int { return strings.Compare(a.Name, b.Name) })
	state := State{Units: units, Summary: Summary{Total: len(units)}}
	for _, unit := range units {
		if unit.ActiveState == "active" {
			state.Summary.Active++
		}
		if unit.ActiveState == "failed" {
			state.Summary.Failed++
		}
	}
	return state, nil
}

func (m *SystemManager) Start(ctx context.Context, name string) error {
	return m.job(ctx, name, "start")
}
func (m *SystemManager) Stop(ctx context.Context, name string) error { return m.job(ctx, name, "stop") }
func (m *SystemManager) Restart(ctx context.Context, name string) error {
	return m.job(ctx, name, "restart")
}

func (m *SystemManager) ResetFailed(ctx context.Context, name string) error {
	if err := validateUnit(name, "reset-failed"); err != nil {
		return err
	}
	if _, err := m.resolveUnit(ctx, name); err != nil {
		return err
	}
	return m.client.ResetFailedUnitContext(ctx, name)
}

func (m *SystemManager) Enable(ctx context.Context, name string) error {
	if err := validateUnit(name, "enable"); err != nil {
		return err
	}
	if _, err := m.resolveUnit(ctx, name); err != nil {
		return err
	}
	_, _, err := m.client.EnableUnitFilesContext(ctx, []string{name}, false, false)
	return err
}

func (m *SystemManager) Disable(ctx context.Context, name string) error {
	if err := validateUnit(name, "disable"); err != nil {
		return err
	}
	if _, err := m.resolveUnit(ctx, name); err != nil {
		return err
	}
	_, err := m.client.DisableUnitFilesContext(ctx, []string{name}, false)
	return err
}

func (m *SystemManager) job(ctx context.Context, name, action string) error {
	if err := validateUnit(name, action); err != nil {
		return err
	}
	if _, err := m.resolveUnit(ctx, name); err != nil {
		return err
	}
	var err error
	switch action {
	case "start":
		_, err = m.client.StartUnitContext(ctx, name, "replace", nil)
	case "stop":
		_, err = m.client.StopUnitContext(ctx, name, "replace", nil)
	case "restart":
		_, err = m.client.RestartUnitContext(ctx, name, "replace", nil)
	}
	return err
}

func (m *SystemManager) resolveUnit(ctx context.Context, name string) (Unit, error) {
	files, err := m.client.ListUnitFilesContext(ctx)
	if err != nil {
		return Unit{}, err
	}
	for _, file := range files {
		if filepath.Base(file.Path) == name {
			return Unit{Name: name, Description: name, UnitFileState: file.Type}, nil
		}
	}
	statuses, err := m.client.ListUnitsByPatternsContext(ctx, nil, []string{name})
	if err != nil {
		return Unit{}, err
	}
	for _, status := range statuses {
		if status.Name == name {
			return Unit{Name: name, Description: status.Description, LoadState: status.LoadState, ActiveState: status.ActiveState, SubState: status.SubState}, nil
		}
	}
	return Unit{}, fmt.Errorf("systemd unit %s does not exist", name)
}

func validateUnit(name, action string) error {
	if !validUnitName(name) {
		return errors.New("invalid systemd unit name")
	}
	if protectedUnits[name] && (action == "stop" || action == "disable") {
		return fmt.Errorf("%s cannot be %sd", name, action)
	}
	return nil
}

func validUnitName(name string) bool {
	return unitNamePattern.MatchString(name) && !strings.Contains(name, "..")
}
