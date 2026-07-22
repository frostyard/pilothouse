package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/backups"
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/frostyard/pilothouse/internal/modules/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServicesManager struct{ journalUnit string }

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
	require.NoError(t, registerStorageActions(actions, manager))

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
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store := &fakeRemoteManager{}, &recordingAuditStore{}
			actions := broker.NewActionRegistry(store)
			require.NoError(t, registerStorageActions(actions, manager))

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
			require.NoError(t, storage.ValidateDefinitionID(manager.create.ID))
			assert.Equal(t, "storage/mount/"+manager.create.ID, store.last().Resource)
			for _, secret := range test.parameters {
				assert.NotContains(t, store.last().Resource, secret)
			}
		})
	}
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
			require.NoError(t, registerStorageActions(actions, manager))
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
	require.NoError(t, registerStorageActions(actions, &fakeRemoteManager{}))
	err := actions.Execute(context.Background(), auth.Identity{Admin: true}, broker.ActionStorageMount, map[string]string{"id": "0123456789abcdef0123456789abcdef", "target": "/secret"}, "")
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "/secret")
}

func TestRegisterStorageCredentialActionAuditsOnlyOpaqueID(t *testing.T) {
	const secret = "never-record-this-secret"
	manager, store := &fakeRemoteManager{}, &recordingAuditStore{}
	actions := broker.NewActionRegistry(store)
	require.NoError(t, registerStorageActions(actions, manager))

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
	require.NoError(t, registerStorageActions(actions, manager))
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
			require.NoError(t, registerStorageActions(actions, manager))
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
