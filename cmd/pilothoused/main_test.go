package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServicesManager struct{ journalUnit string }

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
