package main

import (
	"context"
	"testing"

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
