package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/backups"
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServicesManager struct{ journalUnit string }

type fakeBackupsManager struct{}

func (fakeBackupsManager) State(context.Context) (backups.State, error) {
	return backups.State{Configured: true}, nil
}

type fakeMaintenanceManager struct{ rebooted bool }

func (m *fakeMaintenanceManager) Reboot(context.Context) error { m.rebooted = true; return nil }
func (*fakeMaintenanceManager) State(context.Context) (maintenance.State, error) {
	return maintenance.State{OSVersion: "Snosi"}, nil
}

func (*fakeServicesManager) Disable(context.Context, string) error     { return nil }
func (*fakeServicesManager) Enable(context.Context, string) error      { return nil }
func (*fakeServicesManager) ResetFailed(context.Context, string) error { return nil }
func (*fakeServicesManager) Restart(context.Context, string) error     { return nil }
func (*fakeServicesManager) Start(context.Context, string) error       { return nil }
func (*fakeServicesManager) State(context.Context) (services.State, error) {
	return services.State{}, nil
}

func TestRegisterActivityRequiresAdministratorAndBoundsFilters(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	record, err := store.Begin(context.Background(), audit.Attempt{Action: broker.ActionServicesStop, Resource: "services/unit/demo.service", Username: "admin"})
	require.NoError(t, err)
	require.NoError(t, store.Complete(context.Background(), record.ID, audit.OutcomeSucceeded, ""))
	queries := broker.NewQueryRegistry()
	require.NoError(t, registerActivity(queries, store))

	_, err = queries.Execute(context.Background(), auth.Identity{Username: "reader"}, broker.QueryActivity, nil)
	assert.Error(t, err)
	result, err := queries.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, broker.QueryActivity, map[string]string{"limit": "1"})
	require.NoError(t, err)
	assert.Len(t, result, 1)
	_, err = queries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryActivity, map[string]string{"unexpected": "value"})
	assert.Error(t, err)
}

func TestServiceStopRequiresResourceConfirmation(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerServices(actions, queries, &fakeServicesManager{}))
	parameters := map[string]string{"unit": "backup.timer"}
	err := actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionServicesStop, parameters, "")
	assert.ErrorIs(t, err, broker.ErrConfirmationRequired)
	require.NoError(t, actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionServicesStop, parameters, "services/unit/backup.timer"))
}

func TestRegisterMaintenanceAndBackups(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	maintenanceManager := &fakeMaintenanceManager{}
	require.NoError(t, registerMaintenance(actions, queries, maintenanceManager))
	require.NoError(t, registerBackups(queries, fakeBackupsManager{}))
	state, err := queries.Execute(context.Background(), auth.Identity{}, broker.QueryMaintenanceState, nil)
	require.NoError(t, err)
	assert.Equal(t, "Snosi", state.(maintenance.State).OSVersion)
	backupState, err := queries.Execute(context.Background(), auth.Identity{}, broker.QueryBackupsState, nil)
	require.NoError(t, err)
	assert.True(t, backupState.(backups.State).Configured)
	err = actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionMaintenanceReboot, nil, "maintenance/reboot")
	require.NoError(t, err)
	assert.True(t, maintenanceManager.rebooted)
}

func TestRegisterJobsRequiresAdministrator(t *testing.T) {
	store, err := jobs.Open(filepath.Join(t.TempDir(), "jobs.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	queries := broker.NewQueryRegistry()
	require.NoError(t, registerJobs(queries, store))
	_, err = queries.Execute(context.Background(), auth.Identity{}, broker.QueryJobs, nil)
	assert.Error(t, err)
	result, err := queries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryJobs, map[string]string{"limit": "1"})
	require.NoError(t, err)
	assert.Empty(t, result)
}
func (*fakeServicesManager) Stop(context.Context, string) error { return nil }
func (m *fakeServicesManager) Journal(_ context.Context, unit string) (services.Journal, error) {
	m.journalUnit = unit
	return services.Journal{Unit: unit}, nil
}

func TestRegisterServicesJournalAllowsReadOnlyIdentity(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	manager := &fakeServicesManager{}
	require.NoError(t, registerServices(actions, queries, manager))
	result, err := queries.Execute(context.Background(), auth.Identity{Username: "reader"}, broker.QueryServicesJournal, map[string]string{"unit": "backup.timer"})
	require.NoError(t, err)
	assert.Equal(t, "backup.timer", manager.journalUnit)
	assert.Equal(t, services.Journal{Unit: "backup.timer"}, result)
}
