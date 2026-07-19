package backups

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

var timerNamePattern = regexp.MustCompile(`^[A-Za-z0-9:_.@-]+\.timer$`)

type Health string

const (
	HealthCritical Health = "critical"
	HealthHealthy  Health = "healthy"
	HealthUnknown  Health = "unknown"
	HealthWarning  Health = "warning"
)

type Timer struct {
	ActiveState   string    `json:"active_state"`
	Detail        string    `json:"detail"`
	Health        Health    `json:"health"`
	LastRun       time.Time `json:"last_run"`
	Name          string    `json:"name"`
	NextRun       time.Time `json:"next_run"`
	Result        string    `json:"result"`
	Service       string    `json:"service"`
	UnitFileState string    `json:"unit_file_state"`
}

type State struct {
	Configured bool    `json:"configured"`
	Timers     []Timer `json:"timers"`
}

type Manager interface {
	State(context.Context) (State, error)
}

type systemdClient interface {
	GetUnitPropertiesContext(context.Context, string) (map[string]any, error)
	GetUnitTypePropertiesContext(context.Context, string, string) (map[string]any, error)
}

type SystemManager struct {
	client systemdClient
	maxAge time.Duration
	names  []string
	now    func() time.Time
}

func NewSystemManager(names []string, maxAge time.Duration) (*SystemManager, error) {
	if err := validateConfiguration(names, maxAge); err != nil {
		return nil, err
	}
	client, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return nil, fmt.Errorf("connect to systemd: %w", err)
	}
	return newSystemManager(client, names, maxAge)
}

func newSystemManager(client systemdClient, names []string, maxAge time.Duration) (*SystemManager, error) {
	if client == nil {
		return nil, errors.New("systemd client is required")
	}
	if err := validateConfiguration(names, maxAge); err != nil {
		return nil, err
	}
	return &SystemManager{client: client, maxAge: maxAge, names: slices.Clone(names), now: time.Now}, nil
}

func (m *SystemManager) Close() {
	if closer, ok := m.client.(interface{ Close() }); ok {
		closer.Close()
	}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	state := State{Configured: len(m.names) > 0, Timers: make([]Timer, 0, len(m.names))}
	for _, name := range m.names {
		state.Timers = append(state.Timers, m.timerState(ctx, name))
	}
	return state, nil
}

func (m *SystemManager) timerState(ctx context.Context, name string) Timer {
	timer := Timer{Name: name, Health: HealthUnknown}
	unitProperties, err := m.client.GetUnitPropertiesContext(ctx, name)
	if err != nil {
		timer.Detail = "Timer status is unavailable."
		return timer
	}
	var ok bool
	timer.ActiveState, ok = stringProperty(unitProperties, "ActiveState")
	if !ok {
		timer.Detail = "Timer status did not include ActiveState."
		return timer
	}
	if value, exists := stringProperty(unitProperties, "UnitFileState"); exists {
		timer.UnitFileState = value
	}

	timerProperties, err := m.client.GetUnitTypePropertiesContext(ctx, name, "Timer")
	if err != nil {
		timer.Detail = "Timer schedule is unavailable."
		return timer
	}
	timer.Service, ok = stringProperty(timerProperties, "Unit")
	if !ok || !strings.HasSuffix(timer.Service, ".service") || strings.Contains(timer.Service, "..") {
		timer.Detail = "Timer schedule did not identify its triggered unit."
		return timer
	}
	lastTrigger, ok := usecProperty(timerProperties, "LastTriggerUSec")
	if !ok {
		timer.Detail = "Timer schedule did not include a valid LastTriggerUSec."
		return timer
	}
	nextElapse, ok := usecProperty(timerProperties, "NextElapseUSecRealtime")
	if !ok {
		timer.Detail = "Timer schedule did not include a valid NextElapseUSecRealtime."
		return timer
	}
	timer.LastRun = usecTime(lastTrigger)
	timer.NextRun = usecTime(nextElapse)

	var resultError string
	if lastTrigger != 0 {
		serviceProperties, serviceErr := m.client.GetUnitTypePropertiesContext(ctx, timer.Service, "Service")
		if serviceErr != nil {
			resultError = "Last backup result is unavailable."
		} else {
			timer.Result, ok = stringProperty(serviceProperties, "Result")
			if !ok || timer.Result == "" {
				resultError = "Last backup result is unavailable."
			}
		}
	}

	switch {
	case timer.ActiveState != "active":
		timer.Health = HealthCritical
		timer.Detail = "Timer is " + timer.ActiveState + "; future backups are not scheduled."
	case timer.UnitFileState == "disabled" || timer.UnitFileState == "masked":
		timer.Health = HealthCritical
		timer.Detail = "Timer unit file is " + timer.UnitFileState + "."
	case lastTrigger == 0:
		timer.Health = HealthWarning
		timer.Detail = "Backup has never run."
	case timer.Result != "" && timer.Result != "success":
		timer.Health = HealthCritical
		timer.Detail = "Last backup finished with result " + timer.Result + "."
	case m.now().Sub(timer.LastRun) > m.maxAge:
		timer.Health = HealthWarning
		timer.Detail = "Last successful backup is older than " + m.maxAge.String() + "."
	case resultError != "":
		timer.Detail = resultError
	case nextElapse == 0:
		timer.Health = HealthWarning
		timer.Detail = "Timer has no next run scheduled."
	default:
		timer.Health = HealthHealthy
		timer.Detail = "Last backup completed successfully."
	}
	return timer
}

func validateConfiguration(names []string, maxAge time.Duration) error {
	if maxAge <= 0 {
		return errors.New("backup max age must be positive")
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !timerNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("invalid systemd timer name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate systemd timer name %q", name)
		}
		seen[name] = true
	}
	return nil
}

func stringProperty(properties map[string]any, name string) (string, bool) {
	value, ok := properties[name].(string)
	return value, ok
}

func usecProperty(properties map[string]any, name string) (uint64, bool) {
	value, ok := properties[name].(uint64)
	return value, ok && value <= math.MaxInt64
}

func usecTime(value uint64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMicro(int64(value)).UTC()
}
