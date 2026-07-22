package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/backups"
	"github.com/frostyard/pilothouse/internal/modules/files"
	"github.com/frostyard/pilothouse/internal/modules/logs"
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServicesManager struct{ journalUnit string }

type fakeLogsManager struct {
	filters logs.Filters
	calls   int
}

func (m *fakeLogsManager) Logs(_ context.Context, filters logs.Filters) (logs.State, error) {
	m.calls++
	m.filters = filters
	return logs.State{Filters: filters, Entries: []logs.Entry{}, Units: []string{}}, nil
}

type fakeBackupsManager struct{}

type fakeFilesManager struct {
	download files.Download
	err      error
	list     files.ListRequest
	upload   struct {
		root, directory, name, body string
	}
}

func (m *fakeFilesManager) List(_ context.Context, request files.ListRequest) (files.State, error) {
	m.list = request
	return files.State{}, m.err
}

func (m *fakeFilesManager) Download(context.Context, string, string) (files.Download, error) {
	return m.download, m.err
}

func (m *fakeFilesManager) Upload(_ context.Context, root, directory, name string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.upload.root, m.upload.directory, m.upload.name, m.upload.body = root, directory, name, string(data)
	return m.err
}

func (*fakeFilesManager) Close() error { return nil }

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

func TestRegisterLogsRequiresAdministratorAndValidatesParameters(t *testing.T) {
	queries := broker.NewQueryRegistry()
	manager := &fakeLogsManager{}
	require.NoError(t, registerLogs(queries, manager))
	parameters := map[string]string{
		"query": "panic", "priority": "warning", "unit": "sshd.service", "window": "6h",
	}

	_, err := queries.Execute(context.Background(), auth.Identity{Username: "reader"}, broker.QueryLogs, parameters)
	assert.Error(t, err)
	assert.Zero(t, manager.calls)

	_, err = queries.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, broker.QueryLogs, parameters)
	require.NoError(t, err)
	assert.Equal(t, logs.Filters{Query: "panic", Priority: "warning", Unit: "sshd.service", Window: "6h"}, manager.filters)

	invalid := []map[string]string{
		{"unexpected": "value"}, {"priority": "verbose"},
		{"window": "7d"}, {"unit": "../bad.service"},
		{"query": strings.Repeat("x", 1025)},
	}
	for _, values := range invalid {
		_, err := queries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryLogs, values)
		assert.Error(t, err)
	}
}

func TestRegisterFilesRequiresAdministratorAndUsesFixedParameters(t *testing.T) {
	queries := broker.NewQueryRegistry()
	streamQueries := broker.NewStreamQueryRegistry()
	streamActions := broker.NewStreamActionRegistry()
	manager := &fakeFilesManager{download: files.Download{Body: io.NopCloser(strings.NewReader("contents")), Name: "report.txt", Size: 8}}
	require.NoError(t, registerFiles(queries, streamQueries, streamActions, manager))

	listParameters := map[string]string{"root": "logs", "path": "recent", "filter": "error", "sort": "modified", "direction": "desc", "hidden": "true"}
	_, err := queries.Execute(context.Background(), auth.Identity{}, broker.QueryFilesList, listParameters)
	assert.Error(t, err)
	_, err = streamQueries.Execute(context.Background(), auth.Identity{}, broker.QueryFilesDownload, map[string]string{"root": "logs", "path": "report.txt"})
	assert.Error(t, err)
	err = streamActions.Execute(context.Background(), auth.Identity{}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": "recent", "name": "report.txt"}, strings.NewReader("contents"))
	assert.Error(t, err)

	_, err = queries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryFilesList, listParameters)
	require.NoError(t, err)
	assert.Equal(t, files.ListRequest{Root: "logs", Path: "recent", Filter: "error", Sort: "modified", Direction: "desc", Hidden: true}, manager.list)
	_, err = queries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryFilesList, map[string]string{"root": "logs"})
	assert.Error(t, err)

	download, err := streamQueries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryFilesDownload, map[string]string{"root": "logs", "path": "report.txt"})
	require.NoError(t, err)
	assert.Equal(t, "report.txt", download.Filename)
	assert.Equal(t, "application/octet-stream", download.MediaType)
	assert.EqualValues(t, 8, download.Size)
	contents, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	assert.Equal(t, "contents", string(contents))
	require.NoError(t, download.Body.Close())
	_, err = streamQueries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryFilesDownload, map[string]string{"root": "logs", "path": "report.txt", "extra": "x"})
	assert.Error(t, err)

	err = streamActions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": "recent", "name": "report.txt"}, strings.NewReader("contents"))
	require.NoError(t, err)
	assert.Equal(t, "logs", manager.upload.root)
	assert.Equal(t, "recent", manager.upload.directory)
	assert.Equal(t, "report.txt", manager.upload.name)
	assert.Equal(t, "contents", manager.upload.body)
	err = streamActions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": "recent", "name": "report.txt", "extra": "x"}, strings.NewReader("contents"))
	assert.Error(t, err)
}

func TestRegisterFilesMapsDomainErrorsToPublicErrors(t *testing.T) {
	for _, test := range []struct {
		name, message, category string
		err                     error
		status                  int
	}{
		{name: "invalid", err: files.ErrInvalid, status: 400, message: "invalid files request", category: "invalid_request"},
		{name: "not found", err: files.ErrNotFound, status: 404, message: "files resource not found", category: "not_found"},
		{name: "read only", err: files.ErrReadOnly, status: 403, message: "files root is read-only", category: "read_only"},
		{name: "conflict", err: files.ErrConflict, status: 409, message: "files conflict", category: "conflict"},
		{name: "too large", err: files.ErrTooLarge, status: 413, message: "file transfer is too large", category: "too_large"},
		{name: "unavailable", err: files.ErrUnavailable, status: 503, message: "files service unavailable", category: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			queries := broker.NewQueryRegistry()
			streamQueries := broker.NewStreamQueryRegistry()
			streamActions := broker.NewStreamActionRegistry()
			manager := &fakeFilesManager{err: test.err, download: files.Download{Body: io.NopCloser(strings.NewReader("contents")), Size: 8}}
			require.NoError(t, registerFiles(queries, streamQueries, streamActions, manager))

			_, err := queries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryFilesList, map[string]string{"root": "", "path": "", "filter": "", "sort": "name", "direction": "asc", "hidden": "false"})
			assert.ErrorIs(t, err, test.err)
			assert.Equal(t, test.status, broker.StatusCode(err))
			_, message, category := broker.PublicErrorDetails(err)
			assert.Equal(t, test.message, message)
			assert.Equal(t, test.category, category)

			_, err = streamQueries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryFilesDownload, map[string]string{"root": "logs", "path": "report.txt"})
			assert.ErrorIs(t, err, test.err)

			err = streamActions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": "recent", "name": "report.txt"}, strings.NewReader("contents"))
			assert.ErrorIs(t, err, test.err)
		})
	}

}

func TestRegisterFilesAuditsExactUploadDestination(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	queries := broker.NewQueryRegistry()
	streamQueries := broker.NewStreamQueryRegistry()
	streamActions := broker.NewStreamActionRegistry(store)
	require.NoError(t, registerFiles(queries, streamQueries, streamActions, &fakeFilesManager{}))

	err = streamActions.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": "recent", "name": "report.txt"}, strings.NewReader("contents"))
	require.NoError(t, err)
	records, err := store.List(context.Background(), audit.Filter{Action: broker.ActionFilesUpload, Limit: 1})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "files/logs/recent/report.txt", records[0].Resource)
}
