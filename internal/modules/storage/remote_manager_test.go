//go:build linux

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingUnitController struct {
	calls       []string
	fail        string
	cleanupFail string
	failCalls   map[string]map[int]bool
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
	if controller.failCalls != nil && controller.failCalls[operation][len(controller.calls)] {
		return errors.New("injected " + operation + " failure")
	}
	if controller.fail == operation || controller.cleanupFail == operation {
		return errors.New("injected " + operation + " failure")
	}
	return nil
}

type failingArtifactWriter struct {
	ArtifactStore
	fail string
}

func (writer failingArtifactWriter) WriteCredentials(id, username, password string) error {
	if writer.fail == "credentials" {
		return errors.New("injected credentials failure")
	}
	return writer.ArtifactStore.WriteCredentials(id, username, password)
}

func (writer failingArtifactWriter) WriteMountUnit(definition Definition) error {
	if writer.fail == "mount" {
		return errors.New("injected mount failure")
	}
	return writer.ArtifactStore.WriteMountUnit(definition)
}

func (writer failingArtifactWriter) WriteAutomountUnit(definition Definition) error {
	if writer.fail == "automount" {
		return errors.New("injected automount failure")
	}
	return writer.ArtifactStore.WriteAutomountUnit(definition)
}

func (writer failingArtifactWriter) WriteManifest(definition Definition) error {
	if writer.fail == "manifest" {
		return errors.New("injected manifest failure")
	}
	return writer.ArtifactStore.WriteManifest(definition)
}

func TestRemoteManagerCreateWriteFailuresRollBackOnlyCompletedArtifacts(t *testing.T) {
	for _, step := range []string{"credentials", "mount", "automount", "manifest"} {
		t.Run(step, func(t *testing.T) {
			store := testArtifactStore(t)
			request := testSMBRequest(t)
			unrelated := filepath.Join(store.UnitRoot, "unrelated.mount")
			require.NoError(t, os.MkdirAll(store.UnitRoot, 0o700))
			require.NoError(t, os.WriteFile(unrelated, []byte("foreign"), 0o600))
			manager := NewSystemRemoteManagerWithWriter(staticManager{}, store, failingArtifactWriter{ArtifactStore: store, fail: step}, &recordingUnitController{})

			require.Error(t, manager.Create(context.Background(), request))

			assert.NoFileExists(t, filepath.Join(store.CredentialRoot, request.ID))
			assert.NoFileExists(t, filepath.Join(store.UnitRoot, mountUnitName(request.Target)))
			assert.NoFileExists(t, filepath.Join(store.UnitRoot, automountUnitName(request.Target)))
			assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
			assert.FileExists(t, unrelated)
			assert.NoDirExists(t, request.Target)
		})
	}
}

func TestRemoteManagerRollbackAttemptsEveryUndoAfterFailures(t *testing.T) {
	store := testArtifactStore(t)
	request := testNFSRequest(t)
	controller := &recordingUnitController{failCalls: map[string]map[int]bool{
		"start":   {3: true},
		"disable": {4: true},
		"reload":  {5: true},
	}}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)

	err := manager.Create(context.Background(), request)

	require.Error(t, err)
	assert.Equal(t, []string{
		"reload", "enable " + automountUnitName(request.Target), "start " + automountUnitName(request.Target),
		"disable " + automountUnitName(request.Target), "reload", "reload",
	}, controller.calls)
	definition, loadErr := store.LoadDefinition(request.ID)
	require.NoError(t, loadErr)
	assert.Equal(t, "needs-attention", definition.State)
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
	assert.Equal(t, []string{"reload", "enable " + automountUnitName(request.Target), "start " + automountUnitName(request.Target), "disable " + automountUnitName(request.Target), "reload", "reload"}, controller.calls)
}

func TestRemoteManagerCreateRollsBackForEachUnitFailure(t *testing.T) {
	for _, operation := range []string{"reload", "enable", "start"} {
		t.Run(operation, func(t *testing.T) {
			store := testArtifactStore(t)
			controller := &recordingUnitController{fail: operation}
			manager := NewSystemRemoteManager(staticManager{}, store, controller)
			request := testNFSRequest(t)

			require.Error(t, manager.Create(context.Background(), request))

			if operation == "reload" {
				definition, err := store.LoadDefinition(request.ID)
				require.NoError(t, err)
				assert.Equal(t, "needs-attention", definition.State)
			} else {
				assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
			}
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

func TestRemoteManagerMountAndUnmountRefuseNeedsAttentionDefinitions(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testNFSRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	definition.State = "needs-attention"
	require.NoError(t, store.UpdateManifest(definition))
	controller.calls = nil

	assert.Error(t, manager.Mount(context.Background(), request.ID))
	assert.Error(t, manager.Unmount(context.Background(), request.ID))
	assert.Empty(t, controller.calls)
}

func TestRemoteManagerCreateRejectsTargetNestedUnderInactiveDefinition(t *testing.T) {
	store := testArtifactStore(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	request := testNFSRequest(t)
	require.NoError(t, os.Mkdir(request.Target, 0o755))
	definition, err := manager.definition(request)
	require.NoError(t, err)
	definition.State = "inactive"
	require.NoError(t, store.WriteMountUnit(definition))
	require.NoError(t, store.WriteAutomountUnit(definition))
	require.NoError(t, store.WriteManifest(definition))

	request.ID = "fedcba9876543210fedcba9876543210"
	request.Target = filepath.Join(request.Target, "nested")
	assert.Error(t, manager.Create(context.Background(), request))
	assert.NoDirExists(t, request.Target)
}

func TestRemoteMountCredentialOnlyAppearsInCredentialArtifact(t *testing.T) {
	store := testArtifactStore(t)
	request := testSMBRequest(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	require.NoError(t, manager.Create(context.Background(), request))

	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	manifestPath, _ := store.ManifestPath(request.ID)
	mountPath, _ := store.MountUnitPath(definition)
	automountPath, _ := store.AutomountUnitPath(definition)
	credentialPath, _ := store.CredentialPath(request.ID)
	snapshot, err := manager.State(context.Background())
	require.NoError(t, err)
	var html strings.Builder
	require.NoError(t, ManagedPage(snapshot, false, "csrf-token", true).Render(context.Background(), &html))
	invalid := request
	invalid.Target = "/etc/never-record-this-secret"
	createErr := manager.Create(context.Background(), invalid)
	require.Error(t, createErr)

	credential, err := os.ReadFile(credentialPath)
	require.NoError(t, err)
	assert.Contains(t, string(credential), request.Password)
	for _, contents := range []string{html.String(), createErr.Error(), fmt.Sprintf("%+v", snapshot)} {
		assert.NotContains(t, contents, request.Password)
	}
	for _, path := range []string{manifestPath, mountPath, automountPath} {
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotContains(t, string(contents), request.Password)
	}
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

func TestRemoteManagerDeleteNeedsAttentionAcceptsMissingCanonicalArtifacts(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testSMBRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	definition.State = "needs-attention"
	require.NoError(t, store.UpdateManifest(definition))
	mountPath, _ := store.MountUnitPath(definition)
	automountPath, _ := store.AutomountUnitPath(definition)
	credentialPath, _ := store.CredentialPath(definition.ID)
	require.NoError(t, os.Remove(mountPath))
	require.NoError(t, os.Remove(automountPath))
	require.NoError(t, os.Remove(credentialPath))

	require.NoError(t, manager.Delete(context.Background(), request.ID))

	assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
}

func TestRemoteManagerDeleteNeedsAttentionPreservesForeignArtifact(t *testing.T) {
	store := testArtifactStore(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	request := testSMBRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	definition.State = "needs-attention"
	require.NoError(t, store.UpdateManifest(definition))
	mountPath, _ := store.MountUnitPath(definition)
	require.NoError(t, os.WriteFile(mountPath, []byte("foreign"), 0o644))

	require.Error(t, manager.Delete(context.Background(), request.ID))

	contents, err := os.ReadFile(mountPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("foreign"), contents)
	definition, err = store.LoadDefinition(request.ID)
	require.NoError(t, err)
	assert.Equal(t, "needs-attention", definition.State)
}

func TestRemoteManagerDeleteNeedsAttentionPreservesForeignArtifactSymlink(t *testing.T) {
	store := testArtifactStore(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	request := testNFSRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	definition.State = "needs-attention"
	require.NoError(t, store.UpdateManifest(definition))
	mountPath, _ := store.MountUnitPath(definition)
	mount, err := RenderMountUnit(definition)
	require.NoError(t, err)
	foreign := filepath.Join(t.TempDir(), "foreign.mount")
	require.NoError(t, os.WriteFile(foreign, mount, 0o644))
	require.NoError(t, os.Remove(mountPath))
	require.NoError(t, os.Symlink(foreign, mountPath))

	require.Error(t, manager.Delete(context.Background(), request.ID))

	info, err := os.Lstat(mountPath)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
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

func TestManagedDefinitionSnapshotSummaryCountsMergedAndInactiveDefinitions(t *testing.T) {
	store := testArtifactStore(t)
	request := testNFSRequest(t)
	core := Mount{Filesystem: "nfs", Source: request.Host + ":" + request.Export, State: "mounted", Target: request.Target}
	manager := NewSystemRemoteManager(staticManager{snapshot: Snapshot{Mounts: []Mount{core}}}, store, &recordingUnitController{})
	definition, err := manager.definition(request)
	require.NoError(t, err)
	require.NoError(t, store.WriteMountUnit(definition))
	require.NoError(t, store.WriteAutomountUnit(definition))
	require.NoError(t, store.WriteManifest(definition))

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Len(t, snapshot.Mounts, 1)
	assert.Equal(t, 1, snapshot.Summary.ActiveMounts)

	store = testArtifactStore(t)
	request = testNFSRequest(t)
	manager = NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	definition, err = manager.definition(request)
	require.NoError(t, err)
	require.NoError(t, store.WriteMountUnit(definition))
	require.NoError(t, store.WriteAutomountUnit(definition))
	require.NoError(t, store.WriteManifest(definition))
	snapshot, err = manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, snapshot.Summary.ActiveMounts)
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
