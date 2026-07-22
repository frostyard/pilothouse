//go:build linux

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingUnitController struct {
	calls       []string
	fail        string
	cleanupFail string
}

func (controller *recordingUnitController) DaemonReload(context.Context) error {
	controller.calls = append(controller.calls, "reload")
	return controller.errorFor("reload")
}
func (controller *recordingUnitController) Disable(_ context.Context, name string) error {
	controller.calls = append(controller.calls, "disable "+name)
	return controller.errorFor("disable")
}
func (controller *recordingUnitController) Enable(_ context.Context, name string) error {
	controller.calls = append(controller.calls, "enable "+name)
	return controller.errorFor("enable")
}
func (controller *recordingUnitController) Start(_ context.Context, name string) error {
	controller.calls = append(controller.calls, "start "+name)
	return controller.errorFor("start")
}
func (controller *recordingUnitController) Stop(_ context.Context, name string) error {
	controller.calls = append(controller.calls, "stop "+name)
	return controller.errorFor("stop")
}
func (controller *recordingUnitController) errorFor(operation string) error {
	if controller.fail == operation || controller.cleanupFail == operation {
		return errors.New("injected " + operation + " failure")
	}
	return nil
}

func TestRemoteManagerRecordsNeedsAttentionWhenRollbackCleanupFails(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{fail: "start", cleanupFail: "disable"}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testNFSRequest(t)

	require.Error(t, manager.Create(context.Background(), request))

	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	assert.Equal(t, "needs-attention", definition.State)
}

type staticManager struct{ snapshot Snapshot }

func (manager staticManager) State(context.Context) (Snapshot, error) { return manager.snapshot, nil }

func TestRemoteManagerCreateWritesManagedArtifactsAndStartsAutomount(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testNFSRequest(t)

	require.NoError(t, manager.Create(context.Background(), request))

	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	assert.Equal(t, "active", definition.State)
	assert.Equal(t, []string{"reload", "enable " + automountUnitName(request.Target), "start " + automountUnitName(request.Target)}, controller.calls)
	assert.FileExists(t, filepath.Join(store.UnitRoot, mountUnitName(request.Target)))
	assert.FileExists(t, filepath.Join(store.UnitRoot, automountUnitName(request.Target)))
}

func TestRemoteManagerCreateRollsBackArtifactsWhenStartFails(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{fail: "start"}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testNFSRequest(t)

	err := manager.Create(context.Background(), request)

	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
	assert.NoFileExists(t, filepath.Join(store.UnitRoot, mountUnitName(request.Target)))
	assert.NoFileExists(t, filepath.Join(store.UnitRoot, automountUnitName(request.Target)))
	assert.NoDirExists(t, request.Target)
	assert.Equal(t, []string{"reload", "enable " + automountUnitName(request.Target), "start " + automountUnitName(request.Target), "disable " + automountUnitName(request.Target), "reload"}, controller.calls)
}

func TestRemoteManagerCreateRollsBackForEachUnitFailure(t *testing.T) {
	for _, operation := range []string{"reload", "enable", "start"} {
		t.Run(operation, func(t *testing.T) {
			store := testArtifactStore(t)
			controller := &recordingUnitController{fail: operation}
			manager := NewSystemRemoteManager(staticManager{}, store, controller)
			request := testNFSRequest(t)

			require.Error(t, manager.Create(context.Background(), request))

			assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
			assert.NoFileExists(t, filepath.Join(store.UnitRoot, mountUnitName(request.Target)))
			assert.NoFileExists(t, filepath.Join(store.UnitRoot, automountUnitName(request.Target)))
			assert.NoDirExists(t, request.Target)
		})
	}
}

func TestRemoteManagerMountAndUnmountUseExactUnits(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testNFSRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	controller.calls = nil

	require.NoError(t, manager.Mount(context.Background(), request.ID))
	require.NoError(t, manager.Unmount(context.Background(), request.ID))

	assert.Equal(t, []string{"start " + automountUnitName(request.Target), "stop " + mountUnitName(request.Target)}, controller.calls)
}

func TestRemoteManagerRefusesMalformedIDsAndModifiedArtifacts(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testNFSRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	controller.calls = nil

	assert.Error(t, manager.Mount(context.Background(), "not-an-id"))
	mountPath := filepath.Join(store.UnitRoot, mountUnitName(request.Target))
	require.NoError(t, os.WriteFile(mountPath, []byte("modified"), 0o644))
	assert.Error(t, manager.Unmount(context.Background(), request.ID))
	assert.Empty(t, controller.calls)
}

func TestRemoteManagerCreateRefusesAnExistingDefinition(t *testing.T) {
	store := testArtifactStore(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	request := testNFSRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	manager.units.(*recordingUnitController).calls = nil

	err := manager.Create(context.Background(), request)

	require.Error(t, err)
	assert.Empty(t, manager.units.(*recordingUnitController).calls)
}

func TestRemoteManagerDeleteDeactivatesBeforeRemovingCredentialAndCreatedTarget(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testSMBRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))

	require.NoError(t, manager.Delete(context.Background(), request.ID))

	assert.Equal(t, []string{
		"reload", "enable " + automountUnitName(request.Target), "start " + automountUnitName(request.Target),
		"stop " + mountUnitName(request.Target), "stop " + automountUnitName(request.Target), "disable " + automountUnitName(request.Target), "reload",
	}, controller.calls)
	assert.NoFileExists(t, filepath.Join(store.CredentialRoot, request.ID))
	assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
	assert.NoDirExists(t, request.Target)
}

func TestManagedDefinitionSnapshotContainsNoPasswordOrCredentialPath(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	request := testSMBRequest(t)
	manager := NewSystemRemoteManager(staticManager{snapshot: Snapshot{Mounts: []Mount{{Target: "/unmanaged", Filesystem: "nfs"}}}}, store, controller)
	require.NoError(t, manager.Create(context.Background(), request))

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	require.Len(t, snapshot.Mounts, 2)
	assert.Equal(t, Mount{ID: "remote:" + request.ID, Filesystem: "cifs", Health: HealthHealthy, Managed: true, ReadOnly: false, Source: "//files.example/media", State: "active", Target: request.Target}, snapshot.Mounts[0])
	assert.NotContains(t, fmt.Sprintf("%+v", snapshot), request.Password)
	assert.NotContains(t, fmt.Sprintf("%+v", snapshot), filepath.Join(store.CredentialRoot, request.ID))
}

func testArtifactStore(t *testing.T) ArtifactStore {
	t.Helper()
	root := t.TempDir()
	return ArtifactStore{ManifestRoot: filepath.Join(root, "manifests"), CredentialRoot: filepath.Join(root, "credentials"), UnitRoot: filepath.Join(root, "units"), UID: os.Getuid(), GID: os.Getgid()}
}

func testNFSRequest(t *testing.T) CreateRequest {
	t.Helper()
	return CreateRequest{ID: "0123456789abcdef0123456789abcdef", Protocol: "nfs", Host: "nas.example", Export: "/exports/media", Target: filepath.Join(t.TempDir(), "media"), Version: "4.2"}
}

func testSMBRequest(t *testing.T) CreateRequest {
	t.Helper()
	return CreateRequest{ID: "0123456789abcdef0123456789abcdef", Protocol: "smb", Server: "files.example", Share: "media", Target: filepath.Join(t.TempDir(), "media"), Username: "operator", Password: "never-record-this-secret", Version: "3.1.1"}
}
