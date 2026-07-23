package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/backups"
	"github.com/frostyard/pilothouse/internal/modules/docker"
	"github.com/frostyard/pilothouse/internal/modules/files"
	"github.com/frostyard/pilothouse/internal/modules/incus"
	"github.com/frostyard/pilothouse/internal/modules/logs"
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/podman"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/frostyard/pilothouse/internal/modules/storage"
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

type fakeStorageManager struct{ snapshot storage.Snapshot }

func (m fakeStorageManager) State(context.Context) (storage.Snapshot, error) { return m.snapshot, nil }

type fakeRemoteManager struct {
	create   storage.CreateRequest
	deleted  string
	mounted  string
	snapshot storage.Snapshot
	stopped  string
}

func (m *fakeRemoteManager) Create(_ context.Context, request storage.CreateRequest) error {
	m.create = request
	return nil
}
func (m *fakeRemoteManager) Delete(_ context.Context, id string) error  { m.deleted = id; return nil }
func (m *fakeRemoteManager) Mount(_ context.Context, id string) error   { m.mounted = id; return nil }
func (m *fakeRemoteManager) Unmount(_ context.Context, id string) error { m.stopped = id; return nil }
func (m *fakeRemoteManager) State(context.Context) (storage.Snapshot, error) {
	return m.snapshot, nil
}

type recordingAuditStore struct {
	mu       sync.Mutex
	attempts []audit.Attempt
}

func (s *recordingAuditStore) Begin(_ context.Context, attempt audit.Attempt) (audit.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, attempt)
	return audit.Record{ID: uint64(len(s.attempts))}, nil
}

func (*recordingAuditStore) Complete(context.Context, uint64, string, string) error { return nil }

func (s *recordingAuditStore) last() audit.Attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts[len(s.attempts)-1]
}

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
	require.NoError(t, registerServices(actions, queries, &fakeServicesManager{}, capability.New(capability.Systemd, capability.Journald)))
	parameters := map[string]string{"unit": "backup.timer"}
	err := actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionServicesStop, parameters, "")
	assert.ErrorIs(t, err, broker.ErrConfirmationRequired)
	require.NoError(t, actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionServicesStop, parameters, "services/unit/backup.timer"))
}

func TestRegisterMaintenanceAndBackups(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	maintenanceManager := &fakeMaintenanceManager{}
	require.NoError(t, registerMaintenance(actions, queries, maintenanceManager))
	require.NoError(t, registerBackups(queries, fakeBackupsManager{}, capability.New(capability.Systemd)))
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
	require.NoError(t, registerServices(actions, queries, manager, capability.New(capability.Systemd, capability.Journald)))
	result, err := queries.Execute(context.Background(), auth.Identity{Username: "reader"}, broker.QueryServicesJournal, map[string]string{"unit": "backup.timer"})
	require.NoError(t, err)
	assert.Equal(t, "backup.timer", manager.journalUnit)
	assert.Equal(t, services.Journal{Unit: "backup.timer"}, result)
}
func TestRegisterStorageAllowsAuthenticatedRead(t *testing.T) {
	queries := broker.NewQueryRegistry()
	expected := storage.Snapshot{Summary: storage.Summary{ActiveMounts: 2}}
	require.NoError(t, registerStorage(queries, fakeStorageManager{snapshot: expected}))

	result, err := queries.Execute(context.Background(), auth.Identity{Username: "viewer"}, broker.QueryStorageState, nil)
	require.NoError(t, err)
	assert.Equal(t, expected, result)
}

func TestRegisterStorageRejectsParameters(t *testing.T) {
	queries := broker.NewQueryRegistry()
	require.NoError(t, registerStorage(queries, fakeStorageManager{}))

	_, err := queries.Execute(context.Background(), auth.Identity{Username: "viewer"}, broker.QueryStorageState, map[string]string{"unexpected": "value"})
	assert.EqualError(t, err, "storage state query does not accept parameters")
}

func TestStorageQueryAndActionsShareManagedRemoteComposition(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	manager := &fakeRemoteManager{snapshot: storage.Snapshot{Mounts: []storage.Mount{{ID: "remote:" + id, Managed: true, State: "needs-attention"}}, Findings: []storage.Finding{{ResourceID: "remote:" + id, Severity: storage.HealthWarning, Title: "Managed remote mount needs attention"}}}}
	queries, actions := broker.NewQueryRegistry(), broker.NewActionRegistry()
	require.NoError(t, registerStorage(queries, manager))
	require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))

	result, err := queries.Execute(context.Background(), auth.Identity{Username: "viewer"}, broker.QueryStorageState, nil)
	require.NoError(t, err)
	snapshot := result.(storage.Snapshot)
	require.Len(t, snapshot.Mounts, 1)
	assert.True(t, snapshot.Mounts[0].Managed)
	assert.Equal(t, "remote:"+id, snapshot.Mounts[0].ID)
	assert.Contains(t, snapshot.Findings, storage.Finding{ResourceID: "remote:" + id, Severity: storage.HealthWarning, Title: "Managed remote mount needs attention"})
	require.NoError(t, actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionStorageMount, map[string]string{"id": id}, ""))
	assert.Equal(t, id, manager.mounted)
}

func TestRegisterStorageCreateActionsUseTrustedIDsAndGlobalLock(t *testing.T) {
	for _, test := range []struct {
		name       string
		action     string
		parameters map[string]string
		want       storage.CreateRequest
	}{
		{"nfs", broker.ActionStorageCreateNFS, map[string]string{"host": "nas.example", "export": "/media", "target": "/mnt/media", "version": "4.2", "read_only": "true"}, storage.CreateRequest{Protocol: "nfs", Host: "nas.example", Export: "/media", Target: "/mnt/media", Version: "4.2", ReadOnly: true}},
		{"smb guest", broker.ActionStorageCreateSMBGuest, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false"}, storage.CreateRequest{Protocol: "smb", Server: "nas.example", Share: "media", Target: "/mnt/media", Version: "3.1.1"}},
		{"smb credentials", broker.ActionStorageCreateSMBCredentials, map[string]string{"server": "nas.example", "share": "media", "username": "mount-user", "password": "secret", "target": "/mnt/media", "version": "3.1.1", "read_only": "false"}, storage.CreateRequest{Protocol: "smb", Server: "nas.example", Share: "media", Username: "mount-user", Password: "secret", Target: "/mnt/media", Version: "3.1.1"}},
		{"smb guest owned", broker.ActionStorageCreateSMBGuestOwned, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "001000", "gid": "000100"}, storage.CreateRequest{Protocol: "smb", Server: "nas.example", Share: "media", Target: "/mnt/media", Version: "3.1.1", SMBOwnership: storage.SMBOwnership{UID: "1000", GID: "100"}}},
		{"smb credentials owned", broker.ActionStorageCreateSMBCredentialsOwned, map[string]string{"server": "nas.example", "share": "media", "username": "mount-user", "password": "secret", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000", "gid": "100"}, storage.CreateRequest{Protocol: "smb", Server: "nas.example", Share: "media", Username: "mount-user", Password: "secret", Target: "/mnt/media", Version: "3.1.1", SMBOwnership: storage.SMBOwnership{UID: "1000", GID: "100"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store := &fakeRemoteManager{}, &recordingAuditStore{}
			actions := broker.NewActionRegistry(store)
			require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))

			err := actions.Execute(context.Background(), auth.Identity{Username: "viewer"}, test.action, test.parameters, "")
			assert.Error(t, err)
			require.NoError(t, actions.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, test.action, test.parameters, ""))
			assert.Equal(t, test.want.Protocol, manager.create.Protocol)
			assert.Equal(t, test.want.Host, manager.create.Host)
			assert.Equal(t, test.want.Export, manager.create.Export)
			assert.Equal(t, test.want.Server, manager.create.Server)
			assert.Equal(t, test.want.Share, manager.create.Share)
			assert.Equal(t, test.want.Username, manager.create.Username)
			assert.Equal(t, test.want.Password, manager.create.Password)
			assert.Equal(t, test.want.Target, manager.create.Target)
			assert.Equal(t, test.want.Version, manager.create.Version)
			assert.Equal(t, test.want.ReadOnly, manager.create.ReadOnly)
			assert.Equal(t, test.want.SMBOwnership, manager.create.SMBOwnership)
			require.NoError(t, storage.ValidateDefinitionID(manager.create.ID))
			assert.Equal(t, "storage/mount/"+manager.create.ID, store.last().Resource)
			if test.want.Password != "" {
				assert.NotContains(t, store.last().Resource, test.want.Password)
			}
		})
	}
}

func TestRegisterStorageOwnedSMBCreateActionsRejectInvalidOwnership(t *testing.T) {
	base := map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000", "gid": "100"}
	for _, test := range []struct {
		name       string
		action     string
		parameters map[string]string
		password   string
	}{
		{"missing ownership", broker.ActionStorageCreateSMBGuestOwned, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000"}, ""},
		{"extra parameter", broker.ActionStorageCreateSMBGuestOwned, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000", "gid": "100", "unexpected": "secret"}, ""},
		{"sentinel ownership", broker.ActionStorageCreateSMBGuestOwned, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "4294967295", "gid": "100"}, ""},
		{"malformed ownership", broker.ActionStorageCreateSMBGuestOwned, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "invalid", "gid": "100"}, ""},
		{"credentials ownership", broker.ActionStorageCreateSMBCredentialsOwned, map[string]string{"server": "nas.example", "share": "media", "username": "mount-user", "password": "credentials-owned-sentinel", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "invalid", "gid": "100"}, "credentials-owned-sentinel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store := &fakeRemoteManager{}, &recordingAuditStore{}
			actions := broker.NewActionRegistry(store)
			require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))

			err := actions.Execute(context.Background(), auth.Identity{Admin: true}, test.action, test.parameters, "")
			require.Error(t, err)
			assert.Equal(t, storage.CreateRequest{}, manager.create)
			if test.password != "" {
				assert.NotContains(t, err.Error(), test.password)
				assert.NotContains(t, fmt.Sprintf("%+v", store.attempts), test.password)
			}
			if len(store.attempts) != 0 {
				assert.Regexp(t, `^storage/mount/[a-f0-9]{32}$`, store.last().Resource)
			}
		})
	}

	manager, store := &fakeRemoteManager{}, &recordingAuditStore{}
	actions := broker.NewActionRegistry(store)
	require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))
	require.NoError(t, actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionStorageCreateSMBGuestOwned, base, ""))
	assert.Regexp(t, `^storage/mount/[a-f0-9]{32}$`, store.last().Resource)
}

func TestRegisterStorageLifecycleActionsValidateIDAndConfirmation(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name         string
		action       string
		confirmation bool
		called       func(*fakeRemoteManager) string
	}{
		{"mount", broker.ActionStorageMount, false, func(m *fakeRemoteManager) string { return m.mounted }},
		{"unmount", broker.ActionStorageUnmount, true, func(m *fakeRemoteManager) string { return m.stopped }},
		{"delete", broker.ActionStorageDelete, true, func(m *fakeRemoteManager) string { return m.deleted }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store := &fakeRemoteManager{}, &recordingAuditStore{}
			actions := broker.NewActionRegistry(store)
			require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))
			parameters := map[string]string{"id": id}

			err := actions.Execute(context.Background(), auth.Identity{Username: "viewer"}, test.action, parameters, "")
			assert.Error(t, err)
			confirmation := ""
			if test.confirmation {
				assert.ErrorIs(t, actions.Execute(context.Background(), auth.Identity{Admin: true}, test.action, parameters, ""), broker.ErrConfirmationRequired)
				confirmation = "storage/mount/" + id
			}
			require.NoError(t, actions.Execute(context.Background(), auth.Identity{Admin: true}, test.action, parameters, confirmation))
			assert.Equal(t, id, test.called(manager))
			assert.Equal(t, "storage/mount/"+id, store.last().Resource)

			err = actions.Execute(context.Background(), auth.Identity{Admin: true}, test.action, map[string]string{"id": "bad-id"}, confirmation)
			assert.Error(t, err)
			assert.NotContains(t, err.Error(), "bad-id")
			assert.Equal(t, id, test.called(manager))
		})
	}
}

func TestRegisterStorageActionsRejectUnexpectedParameters(t *testing.T) {
	actions := broker.NewActionRegistry()
	require.NoError(t, registerStorageActions(actions, &fakeRemoteManager{}, capability.New(capability.Systemd)))
	err := actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionStorageMount, map[string]string{"id": "0123456789abcdef0123456789abcdef", "target": "/secret"}, "")
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "/secret")
}

func TestRegisterStorageCredentialActionAuditsOnlyOpaqueID(t *testing.T) {
	const secret = "never-record-this-secret"
	manager, store := &fakeRemoteManager{}, &recordingAuditStore{}
	actions := broker.NewActionRegistry(store)
	require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))

	require.NoError(t, actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionStorageCreateSMBCredentials, map[string]string{
		"server": "nas.example", "share": "media", "username": "mount-user", "password": secret,
		"target": "/mnt/media", "version": "3.1.1", "read_only": "false",
	}, ""))

	assert.Regexp(t, `^storage/mount/[a-f0-9]{32}$`, store.last().Resource)
	assert.NotContains(t, store.last().Resource, secret)
	assert.NotContains(t, fmt.Sprintf("%+v", store.last()), secret)
}

func TestRegisterStorageCreateActionUsesGlobalLock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	manager := &blockingRemoteManager{started: started, release: release}
	actions := broker.NewActionRegistry()
	require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))
	parameters := map[string]string{"host": "nas.example", "export": "/media", "target": "/mnt/media", "version": "4.2", "read_only": "false"}
	first := make(chan error, 1)
	go func() {
		first <- actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionStorageCreateNFS, parameters, "")
	}()
	<-started
	second := make(chan error, 1)
	go func() {
		second <- actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionStorageCreateNFS, parameters, "")
	}()
	select {
	case err := <-second:
		t.Fatalf("second create bypassed global lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
}

func TestRegisterStorageLifecycleActionsSerializeSameIDAndAllowDifferentIDs(t *testing.T) {
	for _, test := range []struct {
		name         string
		action       string
		confirmation bool
	}{
		{"mount", broker.ActionStorageMount, false},
		{"unmount", broker.ActionStorageUnmount, true},
		{"delete", broker.ActionStorageDelete, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &blockingLifecycleRemoteManager{entered: make(chan string, 3), release: make(chan struct{})}
			actions := broker.NewActionRegistry()
			require.NoError(t, registerStorageActions(actions, manager, capability.New(capability.Systemd)))
			confirmation := ""
			if test.confirmation {
				confirmation = "storage/mount/0123456789abcdef0123456789abcdef"
			}
			run := func(id string, done chan<- error) {
				confirm := confirmation
				if test.confirmation && id != "0123456789abcdef0123456789abcdef" {
					confirm = "storage/mount/11111111111111111111111111111111"
				}
				done <- actions.Execute(context.Background(), auth.Identity{Admin: true}, test.action, map[string]string{"id": id}, confirm)
			}
			first := make(chan error, 1)
			go run("0123456789abcdef0123456789abcdef", first)
			require.Equal(t, "0123456789abcdef0123456789abcdef", <-manager.entered)
			same := make(chan error, 1)
			go run("0123456789abcdef0123456789abcdef", same)
			different := make(chan error, 1)
			go run("11111111111111111111111111111111", different)
			require.Equal(t, "11111111111111111111111111111111", <-manager.entered)
			select {
			case id := <-manager.entered:
				t.Fatalf("same ID overlapped: %s", id)
			case <-time.After(20 * time.Millisecond):
			}
			manager.release <- struct{}{}
			manager.release <- struct{}{}
			require.NoError(t, <-first)
			require.NoError(t, <-different)
			require.Equal(t, "0123456789abcdef0123456789abcdef", <-manager.entered)
			manager.release <- struct{}{}
			require.NoError(t, <-same)
		})
	}
}

type blockingRemoteManager struct {
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (m *blockingRemoteManager) Create(context.Context, storage.CreateRequest) error {
	block := false
	m.once.Do(func() { block = true })
	if block {
		m.started <- struct{}{}
		<-m.release
	}
	return nil
}
func (*blockingRemoteManager) Delete(context.Context, string) error  { return nil }
func (*blockingRemoteManager) Mount(context.Context, string) error   { return nil }
func (*blockingRemoteManager) Unmount(context.Context, string) error { return nil }
func (*blockingRemoteManager) State(context.Context) (storage.Snapshot, error) {
	return storage.Snapshot{}, nil
}

type blockingLifecycleRemoteManager struct {
	entered chan string
	release chan struct{}
}

func (*blockingLifecycleRemoteManager) Create(context.Context, storage.CreateRequest) error {
	return nil
}
func (*blockingLifecycleRemoteManager) State(context.Context) (storage.Snapshot, error) {
	return storage.Snapshot{}, nil
}
func (m *blockingLifecycleRemoteManager) Delete(_ context.Context, id string) error {
	m.entered <- id
	<-m.release
	return nil
}
func (m *blockingLifecycleRemoteManager) Mount(_ context.Context, id string) error {
	m.entered <- id
	<-m.release
	return nil
}
func (m *blockingLifecycleRemoteManager) Unmount(_ context.Context, id string) error {
	m.entered <- id
	<-m.release
	return nil
}

func TestStorageManagerCompositionReportsUnsupportedOptionalBackends(t *testing.T) {
	root := t.TempDir()
	lsblk := writeStorageTool(t, root, `{"blockdevices":[]}`)
	findmnt := writeStorageTool(t, root, `{"filesystems":[]}`)
	manager, err := newStorageManager(func(candidates []string) (string, bool, error) {
		switch candidates[0] {
		case "/usr/bin/lsblk":
			return lsblk, true, nil
		case "/usr/bin/findmnt":
			return findmnt, true, nil
		default:
			return "", false, nil
		}
	}, root)
	require.NoError(t, err)

	snapshot, err := manager.State(context.Background())
	require.NoError(t, err)
	for _, name := range []string{"smart", "mdraid", "lvm", "device-mapper", "multipath", "zfs", "btrfs"} {
		assert.Equal(t, storage.BackendUnsupported, backendAvailability(snapshot.Backends, name), name)
	}
}

func writeStorageTool(t *testing.T, directory, output string) string {
	t.Helper()
	path := filepath.Join(directory, "tool-"+output[2:3])
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0o700))
	return path
}

func backendAvailability(backends []storage.BackendStatus, name string) storage.Availability {
	for _, backend := range backends {
		if backend.Name == name {
			return backend.Availability
		}
	}
	return ""
}

func TestRegisterLogsRequiresAdministratorAndValidatesParameters(t *testing.T) {
	queries := broker.NewQueryRegistry()
	manager := &fakeLogsManager{}
	require.NoError(t, registerLogs(queries, manager, capability.New(capability.Systemd, capability.Journald)))
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

func TestRegisterFilesUsesDownloadBaseName(t *testing.T) {
	queries := broker.NewQueryRegistry()
	streamQueries := broker.NewStreamQueryRegistry()
	streamActions := broker.NewStreamActionRegistry()
	manager := &fakeFilesManager{download: files.Download{Body: io.NopCloser(strings.NewReader("contents")), Name: "nested/report.txt", Size: 8}}
	require.NoError(t, registerFiles(queries, streamQueries, streamActions, manager))

	download, err := streamQueries.Execute(context.Background(), auth.Identity{Admin: true}, broker.QueryFilesDownload, map[string]string{"root": "logs", "path": "nested/report.txt"})

	require.NoError(t, err)
	assert.Equal(t, "report.txt", download.Filename)
	require.NoError(t, download.Body.Close())
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

func TestRegisterFilesBoundsAndAuditsUploadDestination(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	queries := broker.NewQueryRegistry()
	streamQueries := broker.NewStreamQueryRegistry()
	streamActions := broker.NewStreamActionRegistry(store)
	require.NoError(t, registerFiles(queries, streamQueries, streamActions, &fakeFilesManager{}))

	nearDirectory := strings.Repeat("a", files.MaxPathBytes-2)
	err = streamActions.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": nearDirectory, "name": "x"}, strings.NewReader("contents"))
	require.NoError(t, err)
	err = streamActions.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": strings.Repeat("a", files.MaxPathBytes-1), "name": "x"}, strings.NewReader("contents"))
	assert.Equal(t, 400, broker.StatusCode(err))

	err = streamActions.Execute(context.Background(), auth.Identity{Admin: true, Username: "admin"}, broker.ActionFilesUpload, map[string]string{"root": "logs", "directory": "", "name": "root.txt"}, strings.NewReader("contents"))
	require.NoError(t, err)
	records, err := store.List(context.Background(), audit.Filter{Action: broker.ActionFilesUpload, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, "files/logs/root.txt", records[0].Resource)
}

func TestRegisterCapabilitiesIsUnconditionalNonAdminAndReturnsProbedSet(t *testing.T) {
	queries := broker.NewQueryRegistry()
	caps := capability.New(capability.Systemd, capability.Docker, capability.Journald)
	require.NoError(t, registerCapabilities(queries, caps))
	assert.True(t, queries.Registered(broker.QueryCapabilities))

	result, err := queries.Execute(context.Background(), auth.Identity{Username: "reader"}, broker.QueryCapabilities, nil)
	require.NoError(t, err)
	assert.Equal(t, caps, result)

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{"capabilities":["docker","journald","systemd"]}`, string(encoded))
}

func TestRegisterCapabilitiesOmitsAbsentCapabilitiesAndNeverErrorsOnEmptySet(t *testing.T) {
	queries := broker.NewQueryRegistry()
	require.NoError(t, registerCapabilities(queries, capability.New()))

	result, err := queries.Execute(context.Background(), auth.Identity{Username: "reader"}, broker.QueryCapabilities, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{"capabilities":[]}`, string(encoded))
}

type fakePodmanManager struct{}

func (fakePodmanManager) Logs(context.Context, string) (podman.Logs, error) {
	return podman.Logs{}, nil
}
func (fakePodmanManager) Remove(context.Context, string) error        { return nil }
func (fakePodmanManager) RemoveImage(context.Context, string) error   { return nil }
func (fakePodmanManager) Restart(context.Context, string) error       { return nil }
func (fakePodmanManager) Start(context.Context, string) error         { return nil }
func (fakePodmanManager) State(context.Context) (podman.State, error) { return podman.State{}, nil }
func (fakePodmanManager) Stop(context.Context, string) error          { return nil }

func TestRegisterPodmanNoOpsWithoutPodmanCapability(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerPodman(actions, queries, fakePodmanManager{}, capability.New(capability.Systemd)))

	assert.False(t, queries.Registered(broker.QueryPodmanState))
	assert.False(t, queries.Registered(broker.QueryPodmanLogs))
	for _, id := range []string{broker.ActionPodmanRemove, broker.ActionPodmanRemoveImage, broker.ActionPodmanRestart, broker.ActionPodmanStart, broker.ActionPodmanStop} {
		assert.False(t, actions.Registered(id))
	}
}

func TestRegisterPodmanRegistersEverythingWithPodmanCapability(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerPodman(actions, queries, fakePodmanManager{}, capability.New(capability.Podman)))

	assert.True(t, queries.Registered(broker.QueryPodmanState))
	assert.True(t, queries.Registered(broker.QueryPodmanLogs))
	for _, id := range []string{broker.ActionPodmanRemove, broker.ActionPodmanRemoveImage, broker.ActionPodmanRestart, broker.ActionPodmanStart, broker.ActionPodmanStop} {
		assert.True(t, actions.Registered(id))
	}
}

type fakeDockerManager struct{}

func (fakeDockerManager) Logs(context.Context, string) (docker.Logs, error) {
	return docker.Logs{}, nil
}
func (fakeDockerManager) Remove(context.Context, string) error        { return nil }
func (fakeDockerManager) RemoveImage(context.Context, string) error   { return nil }
func (fakeDockerManager) Restart(context.Context, string) error       { return nil }
func (fakeDockerManager) Start(context.Context, string) error         { return nil }
func (fakeDockerManager) State(context.Context) (docker.State, error) { return docker.State{}, nil }
func (fakeDockerManager) Stop(context.Context, string) error          { return nil }

func TestRegisterDockerNoOpsWithoutDockerCapability(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerDocker(actions, queries, fakeDockerManager{}, capability.New(capability.Systemd)))

	assert.False(t, queries.Registered(broker.QueryDockerState))
	assert.False(t, queries.Registered(broker.QueryDockerLogs))
	for _, id := range []string{broker.ActionDockerRemove, broker.ActionDockerRemoveImage, broker.ActionDockerRestart, broker.ActionDockerStart, broker.ActionDockerStop} {
		assert.False(t, actions.Registered(id))
	}
}

func TestRegisterDockerRegistersEverythingWithDockerCapability(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerDocker(actions, queries, fakeDockerManager{}, capability.New(capability.Docker)))

	assert.True(t, queries.Registered(broker.QueryDockerState))
	assert.True(t, queries.Registered(broker.QueryDockerLogs))
	for _, id := range []string{broker.ActionDockerRemove, broker.ActionDockerRemoveImage, broker.ActionDockerRestart, broker.ActionDockerStart, broker.ActionDockerStop} {
		assert.True(t, actions.Registered(id))
	}
}

type fakeIncusManager struct{}

func (fakeIncusManager) Remove(context.Context, string, string) error      { return nil }
func (fakeIncusManager) RemoveImage(context.Context, string, string) error { return nil }
func (fakeIncusManager) Restart(context.Context, string, string) error     { return nil }
func (fakeIncusManager) Start(context.Context, string, string) error       { return nil }
func (fakeIncusManager) State(context.Context, string) (incus.State, error) {
	return incus.State{}, nil
}
func (fakeIncusManager) Stop(context.Context, string, string) error { return nil }

func TestRegisterIncusNoOpsWithoutIncusCapability(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerIncus(actions, queries, fakeIncusManager{}, capability.New(capability.Systemd)))

	assert.False(t, queries.Registered(broker.QueryIncusState))
	for _, id := range []string{broker.ActionIncusRemove, broker.ActionIncusRemoveImage, broker.ActionIncusRestart, broker.ActionIncusStart, broker.ActionIncusStop} {
		assert.False(t, actions.Registered(id))
	}
}

func TestRegisterIncusRegistersEverythingWithIncusCapability(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerIncus(actions, queries, fakeIncusManager{}, capability.New(capability.Incus)))

	assert.True(t, queries.Registered(broker.QueryIncusState))
	for _, id := range []string{broker.ActionIncusRemove, broker.ActionIncusRemoveImage, broker.ActionIncusRestart, broker.ActionIncusStart, broker.ActionIncusStop} {
		assert.True(t, actions.Registered(id))
	}
}

func TestConnectSystemdNeverCallsConnectWithoutSystemdCapability(t *testing.T) {
	called := false
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	client := connectSystemd(context.Background(), capability.New(capability.Docker, capability.Journald), func(context.Context) (*dbus.Conn, error) {
		called = true
		return &dbus.Conn{}, nil
	}, logger)

	assert.Nil(t, client)
	assert.False(t, called, "connect must never be invoked when the Systemd capability is absent")
}

func TestConnectSystemdReturnsNilAndWarnsWhenConnectFails(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	client := connectSystemd(context.Background(), capability.New(capability.Systemd), func(context.Context) (*dbus.Conn, error) {
		return nil, errors.New("dial unix /run/systemd/private: connect: no such file or directory")
	}, logger)

	assert.Nil(t, client)
	assert.Contains(t, logged.String(), "systemd connection unavailable")
	assert.Contains(t, logged.String(), "level=WARN")
}

func TestConnectSystemdReturnsConnectionWhenCapabilityPresentAndConnectSucceeds(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	want := &dbus.Conn{}

	client := connectSystemd(context.Background(), capability.New(capability.Systemd), func(context.Context) (*dbus.Conn, error) {
		return want, nil
	}, logger)

	assert.Same(t, want, client)
}

func TestBuildSystemdManagersSkipsConstructionWithoutClient(t *testing.T) {
	root := t.TempDir()
	lsblk := writeStorageTool(t, root, `{"blockdevices":[]}`)
	findmnt := writeStorageTool(t, root, `{"filesystems":[]}`)
	storageManager, err := newStorageManager(func(candidates []string) (string, bool, error) {
		switch candidates[0] {
		case "/usr/bin/lsblk":
			return lsblk, true, nil
		case "/usr/bin/findmnt":
			return findmnt, true, nil
		default:
			return "", false, nil
		}
	}, root)
	require.NoError(t, err)

	managers, err := buildSystemdManagers(nil, storageManager, []string{"backup.timer"}, time.Hour, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, managers.remoteManager, "remote-mount unit controller must not be constructed without a systemd client")
	assert.Nil(t, managers.backupManager, "backups.SystemManager must not be constructed without a systemd client")
	assert.Nil(t, managers.servicesManager, "services.SystemManager must not be constructed without a systemd client")
	assert.Nil(t, managers.logsManager, "logs.SystemManager must not be constructed without a systemd client")
}

// TestStorageInventoryIsRegisteredAndFunctionalWithoutSystemd is the
// fixture-style test proving, at the construction level (c7) as well as the
// registration-level capability guard (c9's registerBackups and
// registerStorageActions conversions), that QueryStorageState is registered
// and backed by a working manager even when the Systemd capability is
// absent: it builds managers exactly the way run() does (buildSystemdManagers
// with a nil client), registers every dependent handler the same way run()
// does, and asserts the daemon (a) never errors, (b) still answers
// QueryStorageState with real data, and (c) leaves every systemd-dependent
// handler -- including all eight storage remote-mount actions -- unregistered.
func TestStorageInventoryIsRegisteredAndFunctionalWithoutSystemd(t *testing.T) {
	root := t.TempDir()
	lsblk := writeStorageTool(t, root, `{"blockdevices":[]}`)
	findmnt := writeStorageTool(t, root, `{"filesystems":[]}`)
	storageManager, err := newStorageManager(func(candidates []string) (string, bool, error) {
		switch candidates[0] {
		case "/usr/bin/lsblk":
			return lsblk, true, nil
		case "/usr/bin/findmnt":
			return findmnt, true, nil
		default:
			return "", false, nil
		}
	}, root)
	require.NoError(t, err)

	// No Systemd capability: connectSystemd (already proven above to never
	// invoke connect in this case) yields a nil client.
	client := connectSystemd(context.Background(), capability.New(), func(context.Context) (*dbus.Conn, error) {
		t.Fatal("connect must never be invoked when the Systemd capability is absent")
		return nil, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Nil(t, client)

	managers, err := buildSystemdManagers(client, storageManager, []string{"backup.timer"}, time.Hour, nil, nil)
	require.NoError(t, err, "construction must never fail fatally because systemd is absent")

	queries, actions := broker.NewQueryRegistry(), broker.NewActionRegistry()
	require.NoError(t, registerStorage(queries, storageManager))
	require.NoError(t, registerStorageActions(actions, managers.remoteManager, capability.New()))
	require.NoError(t, registerBackups(queries, managers.backupManager, capability.New()))
	require.NoError(t, registerServices(actions, queries, managers.servicesManager, capability.New()))
	require.NoError(t, registerLogs(queries, managers.logsManager, capability.New()))

	require.True(t, queries.Registered(broker.QueryStorageState))
	result, err := queries.Execute(context.Background(), auth.Identity{Username: "viewer"}, broker.QueryStorageState, nil)
	require.NoError(t, err)
	snapshot, ok := result.(storage.Snapshot)
	require.True(t, ok)
	assert.NotEmpty(t, snapshot.Backends, "storage inventory must run its real enrichers, not a stub, independent of systemd")

	assert.False(t, queries.Registered(broker.QueryBackupsState))
	assert.False(t, queries.Registered(broker.QueryServicesState))
	assert.False(t, queries.Registered(broker.QueryServicesJournal))
	assert.False(t, queries.Registered(broker.QueryLogs))
	for _, id := range allStorageRemoteMountActions {
		assert.False(t, actions.Registered(id))
	}
	for _, id := range []string{broker.ActionServicesDisable, broker.ActionServicesEnable, broker.ActionServicesStart, broker.ActionServicesStop} {
		assert.False(t, actions.Registered(id))
	}
}

func TestRegisterBackupsNoOpsWithNilManager(t *testing.T) {
	queries := broker.NewQueryRegistry()
	// Systemd present: proves the nil-manager guard applies independent of
	// caps, since the real run() wiring would never see a nil manager and a
	// present Systemd capability together.
	require.NoError(t, registerBackups(queries, nil, capability.New(capability.Systemd)))
	assert.False(t, queries.Registered(broker.QueryBackupsState))
}

func TestRegisterBackupsRegistersWithSystemd(t *testing.T) {
	queries := broker.NewQueryRegistry()
	require.NoError(t, registerBackups(queries, fakeBackupsManager{}, capability.New(capability.Systemd)))
	assert.True(t, queries.Registered(broker.QueryBackupsState))
}

func TestRegisterBackupsNoOpsWithoutSystemdCapability(t *testing.T) {
	queries := broker.NewQueryRegistry()
	// A non-nil fake manager proves registerBackups' own caps guard
	// independently withholds registration, since backups.Manager monitors
	// systemd timers and requires Systemd uniformly.
	require.NoError(t, registerBackups(queries, fakeBackupsManager{}, capability.New()))
	assert.False(t, queries.Registered(broker.QueryBackupsState))
}

func TestRegisterServicesNoOpsWithNilManager(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	require.NoError(t, registerServices(actions, queries, nil, capability.New(capability.Systemd, capability.Journald)))
	assert.False(t, queries.Registered(broker.QueryServicesState))
	assert.False(t, queries.Registered(broker.QueryServicesJournal))
	for _, id := range []string{broker.ActionServicesDisable, broker.ActionServicesEnable, broker.ActionServicesResetFailed, broker.ActionServicesRestart, broker.ActionServicesStart, broker.ActionServicesStop} {
		assert.False(t, actions.Registered(id))
	}
}

func TestRegisterLogsNoOpsWithNilManager(t *testing.T) {
	queries := broker.NewQueryRegistry()
	require.NoError(t, registerLogs(queries, nil, capability.New(capability.Systemd, capability.Journald)))
	assert.False(t, queries.Registered(broker.QueryLogs))
}

func TestRegisterServicesRegistersStateAndActionsWithSystemdOnly(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	manager := &fakeServicesManager{}
	require.NoError(t, registerServices(actions, queries, manager, capability.New(capability.Systemd)))

	assert.True(t, queries.Registered(broker.QueryServicesState))
	assert.False(t, queries.Registered(broker.QueryServicesJournal), "journal query requires journald in addition to systemd")
	for _, id := range []string{broker.ActionServicesDisable, broker.ActionServicesEnable, broker.ActionServicesResetFailed, broker.ActionServicesRestart, broker.ActionServicesStart, broker.ActionServicesStop} {
		assert.True(t, actions.Registered(id))
	}
}

func TestRegisterServicesNoOpsWithoutSystemdCapability(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	manager := &fakeServicesManager{}
	// journald present, systemd absent: per c7, a real nil-manager fixture
	// would already imply this, but registerServices' own caps guard must
	// independently withhold every registration even given a non-nil fake
	// manager, since QueryServicesJournal additionally requires systemd
	// (docs/capabilities.md exception #2) and service management itself
	// requires systemd.
	require.NoError(t, registerServices(actions, queries, manager, capability.New(capability.Journald)))

	assert.False(t, queries.Registered(broker.QueryServicesState))
	assert.False(t, queries.Registered(broker.QueryServicesJournal))
	for _, id := range []string{broker.ActionServicesDisable, broker.ActionServicesEnable, broker.ActionServicesResetFailed, broker.ActionServicesRestart, broker.ActionServicesStart, broker.ActionServicesStop} {
		assert.False(t, actions.Registered(id))
	}
}

func TestRegisterServicesRegistersJournalWithSystemdAndJournald(t *testing.T) {
	actions, queries := broker.NewActionRegistry(), broker.NewQueryRegistry()
	manager := &fakeServicesManager{}
	require.NoError(t, registerServices(actions, queries, manager, capability.New(capability.Systemd, capability.Journald)))

	assert.True(t, queries.Registered(broker.QueryServicesState))
	assert.True(t, queries.Registered(broker.QueryServicesJournal))
	for _, id := range []string{broker.ActionServicesDisable, broker.ActionServicesEnable, broker.ActionServicesResetFailed, broker.ActionServicesRestart, broker.ActionServicesStart, broker.ActionServicesStop} {
		assert.True(t, actions.Registered(id))
	}
}

func TestRegisterLogsNoOpsWithSystemdOnlyNoJournald(t *testing.T) {
	queries := broker.NewQueryRegistry()
	manager := &fakeLogsManager{}
	require.NoError(t, registerLogs(queries, manager, capability.New(capability.Systemd)))
	assert.False(t, queries.Registered(broker.QueryLogs))
}

func TestRegisterLogsNoOpsWithoutSystemdRegardlessOfJournald(t *testing.T) {
	for _, caps := range []capability.Set{capability.New(), capability.New(capability.Journald)} {
		queries := broker.NewQueryRegistry()
		manager := &fakeLogsManager{}
		require.NoError(t, registerLogs(queries, manager, caps))
		assert.False(t, queries.Registered(broker.QueryLogs))
	}
}

func TestRegisterLogsRegistersWithSystemdAndJournald(t *testing.T) {
	queries := broker.NewQueryRegistry()
	manager := &fakeLogsManager{}
	require.NoError(t, registerLogs(queries, manager, capability.New(capability.Systemd, capability.Journald)))
	assert.True(t, queries.Registered(broker.QueryLogs))
}

func TestRegisterStorageActionsNoOpsWithNilManager(t *testing.T) {
	actions := broker.NewActionRegistry()
	// Systemd present: proves the nil-manager guard applies independent of
	// caps, since the real run() wiring would never see a nil manager and a
	// present Systemd capability together.
	require.NoError(t, registerStorageActions(actions, nil, capability.New(capability.Systemd)))
	for _, id := range allStorageRemoteMountActions {
		assert.False(t, actions.Registered(id))
	}
}

func TestRegisterStorageActionsRegistersWithSystemd(t *testing.T) {
	actions := broker.NewActionRegistry()
	require.NoError(t, registerStorageActions(actions, &fakeRemoteManager{}, capability.New(capability.Systemd)))
	for _, id := range allStorageRemoteMountActions {
		assert.True(t, actions.Registered(id))
	}
}

func TestRegisterStorageActionsNoOpsWithoutSystemdCapability(t *testing.T) {
	actions := broker.NewActionRegistry()
	// A non-nil fake manager proves registerStorageActions' own caps guard
	// independently withholds registration, since every remote-mount action
	// generates or controls systemd units and requires Systemd uniformly.
	require.NoError(t, registerStorageActions(actions, &fakeRemoteManager{}, capability.New()))
	for _, id := range allStorageRemoteMountActions {
		assert.False(t, actions.Registered(id))
	}
}

// allStorageRemoteMountActions is every fixed broker action ID for storage
// remote-mount lifecycle, so capability-guard tests can assert on all eight
// rather than a partial sample.
var allStorageRemoteMountActions = []string{
	broker.ActionStorageCreateNFS,
	broker.ActionStorageCreateSMBGuest,
	broker.ActionStorageCreateSMBCredentials,
	broker.ActionStorageCreateSMBGuestOwned,
	broker.ActionStorageCreateSMBCredentialsOwned,
	broker.ActionStorageMount,
	broker.ActionStorageUnmount,
	broker.ActionStorageDelete,
}
