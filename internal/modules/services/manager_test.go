package services

import (
	"context"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	statuses []dbus.UnitStatus
	files    []dbus.UnitFile
	stopped  string
}

func (f *fakeClient) DisableUnitFilesContext(context.Context, []string, bool) ([]dbus.DisableUnitFileChange, error) {
	return nil, nil
}
func (f *fakeClient) EnableUnitFilesContext(context.Context, []string, bool, bool) (bool, []dbus.EnableUnitFileChange, error) {
	return true, nil, nil
}
func (f *fakeClient) ListUnitFilesContext(context.Context) ([]dbus.UnitFile, error) {
	return f.files, nil
}
func (f *fakeClient) ListUnitsByPatternsContext(context.Context, []string, []string) ([]dbus.UnitStatus, error) {
	return f.statuses, nil
}
func (f *fakeClient) ResetFailedUnitContext(context.Context, string) error { return nil }
func (f *fakeClient) RestartUnitContext(context.Context, string, string, chan<- string) (int, error) {
	return 1, nil
}
func (f *fakeClient) StartUnitContext(context.Context, string, string, chan<- string) (int, error) {
	return 1, nil
}
func (f *fakeClient) StopUnitContext(_ context.Context, name, _ string, _ chan<- string) (int, error) {
	f.stopped = name
	return 1, nil
}

func TestStateFiltersAndSummarizesSupportedUnits(t *testing.T) {
	manager := newSystemManager(&fakeClient{
		statuses: []dbus.UnitStatus{{Name: "backup.timer", ActiveState: "active", Description: "Backup"}, {Name: "broken.service", ActiveState: "failed"}, {Name: "session.scope", ActiveState: "active"}},
		files:    []dbus.UnitFile{{Path: "/etc/systemd/system/backup.timer", Type: "enabled"}, {Path: "/usr/lib/systemd/system/idle.service", Type: "disabled"}},
	})
	state, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Summary{Total: 3, Active: 1, Failed: 1}, state.Summary)
	assert.Equal(t, "backup.timer", state.Units[0].Name)
	assert.Equal(t, "enabled", state.Units[0].UnitFileState)
	assert.Equal(t, Unit{Name: "idle.service", Description: "idle.service", LoadState: "not-found", ActiveState: "inactive", SubState: "dead", UnitFileState: "disabled"}, state.Units[2])
}

func TestProtectedAndMalformedUnitMutationsAreRejected(t *testing.T) {
	client := &fakeClient{files: []dbus.UnitFile{{Path: "/etc/systemd/system/backup.timer"}}}
	manager := newSystemManager(client)
	assert.Error(t, manager.Stop(context.Background(), "pilothouse.service"))
	assert.Error(t, manager.Disable(context.Background(), "pilothoused.service"))
	assert.Error(t, manager.Stop(context.Background(), "../evil.service"))
	assert.NoError(t, manager.Stop(context.Background(), "backup.timer"))
	assert.Equal(t, "backup.timer", client.stopped)
	assert.Error(t, manager.Start(context.Background(), "missing.service"))
}
