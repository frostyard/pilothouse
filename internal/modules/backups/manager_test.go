package backups

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type propertyCall struct {
	name     string
	unitType string
}

type fakeSystemdClient struct {
	calls          []propertyCall
	properties     map[string]map[string]any
	propertyErrors map[string]error
	typeProperties map[string]map[string]any
	typeErrors     map[string]error
}

func (client *fakeSystemdClient) GetUnitPropertiesContext(_ context.Context, name string) (map[string]any, error) {
	client.calls = append(client.calls, propertyCall{name: name})
	return client.properties[name], client.propertyErrors[name]
}

func (client *fakeSystemdClient) GetUnitTypePropertiesContext(_ context.Context, name, unitType string) (map[string]any, error) {
	client.calls = append(client.calls, propertyCall{name: name, unitType: unitType})
	key := name + ":" + unitType
	return client.typeProperties[key], client.typeErrors[key]
}

func TestSystemManagerHealthyTimer(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	client := healthyClient(now, "nightly.timer", "nightly.service")
	manager, err := newSystemManager(client, []string{"nightly.timer"}, 25*time.Hour)
	require.NoError(t, err)
	manager.now = func() time.Time { return now }

	state, err := manager.State(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Timers, 1)
	timer := state.Timers[0]
	assert.True(t, state.Configured)
	assert.Equal(t, HealthHealthy, timer.Health)
	assert.Equal(t, "nightly.service", timer.Service)
	assert.Equal(t, "success", timer.Result)
	assert.Equal(t, now.Add(-time.Hour), timer.LastRun)
	assert.Equal(t, now.Add(23*time.Hour), timer.NextRun)
	assert.Equal(t, []propertyCall{{name: "nightly.timer"}, {name: "nightly.timer", unitType: "Timer"}, {name: "nightly.service", unitType: "Service"}}, client.calls)
}

func TestSystemManagerClassifiesUnhealthyTimers(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		configure  func(*fakeSystemdClient)
		wantHealth Health
		wantDetail string
	}{
		{
			name: "failed service",
			configure: func(client *fakeSystemdClient) {
				client.typeProperties["nightly.service:Service"]["Result"] = "exit-code"
			},
			wantHealth: HealthCritical,
			wantDetail: "exit-code",
		},
		{
			name: "inactive timer",
			configure: func(client *fakeSystemdClient) {
				client.properties["nightly.timer"]["ActiveState"] = "inactive"
			},
			wantHealth: HealthCritical,
			wantDetail: "inactive",
		},
		{
			name: "never run",
			configure: func(client *fakeSystemdClient) {
				client.typeProperties["nightly.timer:Timer"]["LastTriggerUSec"] = uint64(0)
			},
			wantHealth: HealthWarning,
			wantDetail: "never run",
		},
		{
			name: "service result unavailable",
			configure: func(client *fakeSystemdClient) {
				client.typeErrors["nightly.service:Service"] = errors.New("denied")
			},
			wantHealth: HealthUnknown,
			wantDetail: "result is unavailable",
		},
		{
			name: "stale",
			configure: func(client *fakeSystemdClient) {
				client.typeProperties["nightly.timer:Timer"]["LastTriggerUSec"] = uint64(now.Add(-48 * time.Hour).UnixMicro())
			},
			wantHealth: HealthWarning,
			wantDetail: "older than",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := healthyClient(now, "nightly.timer", "nightly.service")
			test.configure(client)
			manager, err := newSystemManager(client, []string{"nightly.timer"}, 25*time.Hour)
			require.NoError(t, err)
			manager.now = func() time.Time { return now }

			state, err := manager.State(context.Background())
			require.NoError(t, err)
			require.Len(t, state.Timers, 1)
			assert.Equal(t, test.wantHealth, state.Timers[0].Health)
			assert.Contains(t, state.Timers[0].Detail, test.wantDetail)
		})
	}
}

func TestSystemManagerKeepsUnknownTimerAlongsideHealthyTimers(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	client := healthyClient(now, "healthy.timer", "healthy.service")
	client.propertyErrors["unknown.timer"] = errors.New("unit not found")
	manager, err := newSystemManager(client, []string{"unknown.timer", "healthy.timer"}, 25*time.Hour)
	require.NoError(t, err)
	manager.now = func() time.Time { return now }

	state, err := manager.State(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Timers, 2)
	assert.Equal(t, HealthUnknown, state.Timers[0].Health)
	assert.Equal(t, "Timer status is unavailable.", state.Timers[0].Detail)
	assert.Equal(t, HealthHealthy, state.Timers[1].Health)
}

func TestNewSystemManagerRejectsInvalidNames(t *testing.T) {
	for _, names := range [][]string{{"backup.service"}, {"../backup.timer"}, {"backup..timer"}, {"backup.timer", "backup.timer"}} {
		_, err := newSystemManager(&fakeSystemdClient{}, names, 24*time.Hour)
		assert.Error(t, err, names)
	}
	_, err := newSystemManager(&fakeSystemdClient{}, []string{"backup.timer"}, 0)
	assert.Error(t, err)
}

func TestSystemManagerReportsUnconfiguredState(t *testing.T) {
	client := &fakeSystemdClient{}
	manager, err := newSystemManager(client, nil, time.Hour)
	require.NoError(t, err)
	state, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.False(t, state.Configured)
	assert.Empty(t, state.Timers)
	assert.Empty(t, client.calls)
}

func TestNewSystemManagerAcceptsPreOpenedClientAndRejectsNil(t *testing.T) {
	client := &fakeSystemdClient{}
	manager, err := NewSystemManager(client, []string{"backup.timer"}, time.Hour)
	require.NoError(t, err)
	require.NotNil(t, manager)

	_, err = NewSystemManager(nil, []string{"backup.timer"}, time.Hour)
	assert.Error(t, err)
}

func TestNewSystemManagerRejectsBadConfigurationWithoutRequiringDBus(t *testing.T) {
	client := &fakeSystemdClient{}
	_, err := NewSystemManager(client, []string{"backup.service"}, time.Hour)
	assert.Error(t, err)
	_, err = NewSystemManager(client, []string{"backup.timer"}, 0)
	assert.Error(t, err)
}

func TestValidateConfigurationChecksNamesAndMaxAgeIndependentlyOfSystemd(t *testing.T) {
	assert.NoError(t, ValidateConfiguration([]string{"backup.timer"}, time.Hour))
	assert.Error(t, ValidateConfiguration([]string{"backup.service"}, time.Hour))
	assert.Error(t, ValidateConfiguration([]string{"backup.timer"}, 0))
	assert.Error(t, ValidateConfiguration([]string{"backup.timer", "backup.timer"}, time.Hour))
}

func healthyClient(now time.Time, timer, service string) *fakeSystemdClient {
	return &fakeSystemdClient{
		properties: map[string]map[string]any{
			timer: {"ActiveState": "active", "UnitFileState": "enabled"},
		},
		propertyErrors: map[string]error{},
		typeProperties: map[string]map[string]any{
			timer + ":Timer":     {"Unit": service, "LastTriggerUSec": uint64(now.Add(-time.Hour).UnixMicro()), "NextElapseUSecRealtime": uint64(now.Add(23 * time.Hour).UnixMicro())},
			service + ":Service": {"Result": "success"},
		},
		typeErrors: map[string]error{},
	}
}
