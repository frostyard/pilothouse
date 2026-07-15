package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

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

type Manager interface {
	Disable(context.Context, string) error
	Enable(context.Context, string) error
	ResetFailed(context.Context, string) error
	Restart(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context) (State, error)
	Stop(context.Context, string) error
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

type SystemManager struct{ client systemdClient }

func NewSystemManager() (*SystemManager, error) {
	client, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return nil, fmt.Errorf("connect to systemd: %w", err)
	}
	return &SystemManager{client: client}, nil
}

func newSystemManager(client systemdClient) *SystemManager { return &SystemManager{client: client} }

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
	if err := m.resolve(ctx, name); err != nil {
		return err
	}
	return m.client.ResetFailedUnitContext(ctx, name)
}

func (m *SystemManager) Enable(ctx context.Context, name string) error {
	if err := validateUnit(name, "enable"); err != nil {
		return err
	}
	if err := m.resolve(ctx, name); err != nil {
		return err
	}
	_, _, err := m.client.EnableUnitFilesContext(ctx, []string{name}, false, false)
	return err
}

func (m *SystemManager) Disable(ctx context.Context, name string) error {
	if err := validateUnit(name, "disable"); err != nil {
		return err
	}
	if err := m.resolve(ctx, name); err != nil {
		return err
	}
	_, err := m.client.DisableUnitFilesContext(ctx, []string{name}, false)
	return err
}

func (m *SystemManager) job(ctx context.Context, name, action string) error {
	if err := validateUnit(name, action); err != nil {
		return err
	}
	if err := m.resolve(ctx, name); err != nil {
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

func (m *SystemManager) resolve(ctx context.Context, name string) error {
	files, err := m.client.ListUnitFilesContext(ctx)
	if err != nil {
		return err
	}
	for _, file := range files {
		if filepath.Base(file.Path) == name {
			return nil
		}
	}
	statuses, err := m.client.ListUnitsByPatternsContext(ctx, nil, []string{name})
	if err != nil {
		return err
	}
	for _, status := range statuses {
		if status.Name == name {
			return nil
		}
	}
	return fmt.Errorf("systemd unit %s does not exist", name)
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
