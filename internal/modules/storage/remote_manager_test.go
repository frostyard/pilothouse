//go:build linux

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestRemoteManagerMappedCreateRollsBackWhenStartFails(t *testing.T) {
	store := testArtifactStore(t)
	request := testSMBRequest(t)
	request.SMBOwnership = SMBOwnership{UID: "1000", GID: "100"}
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{fail: "start"})

	require.Error(t, manager.Create(context.Background(), request))
	assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
	assert.NoFileExists(t, filepath.Join(store.UnitRoot, mountUnitName(request.Target)))
	assert.NoFileExists(t, filepath.Join(store.UnitRoot, automountUnitName(request.Target)))
	assert.NoFileExists(t, filepath.Join(store.CredentialRoot, request.ID))
	assert.NoDirExists(t, request.Target)
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

	assert.Equal(t, []string{"start " + automountUnitName(request.Target), "stop " + automountUnitName(request.Target), "stop " + mountUnitName(request.Target)}, controller.calls)
}

func TestRemoteManagerMountAndUnmountVerifyMappedUnit(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testSMBRequest(t)
	request.SMBOwnership = SMBOwnership{UID: "1000", GID: "100"}
	require.NoError(t, manager.Create(context.Background(), request))
	controller.calls = nil

	require.NoError(t, manager.Mount(context.Background(), request.ID))
	require.NoError(t, manager.Unmount(context.Background(), request.ID))
	assert.Equal(t, []string{"start " + automountUnitName(request.Target), "stop " + automountUnitName(request.Target), "stop " + mountUnitName(request.Target)}, controller.calls)

	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	mountPath, err := store.MountUnitPath(definition)
	require.NoError(t, err)
	contents, err := os.ReadFile(mountPath)
	require.NoError(t, err)
	require.Contains(t, string(contents), "uid=1000")
	require.NoError(t, os.WriteFile(mountPath, []byte(strings.Replace(string(contents), "uid=1000", "uid=1001", 1)), 0o644))
	controller.calls = nil

	assert.Error(t, manager.Mount(context.Background(), request.ID))
	assert.Empty(t, controller.calls)
}

func TestRemoteManagerCreateNormalizesAndPersistsSMBOwnership(t *testing.T) {
	store := testArtifactStore(t)
	request := testSMBRequest(t)
	request.SMBOwnership = SMBOwnership{UID: "001000", GID: "000100"}
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})

	require.NoError(t, manager.Create(context.Background(), request))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	assert.Equal(t, ManifestFormatVersion, definition.FormatVersion)
	assert.Equal(t, SMBOwnership{UID: "1000", GID: "100"}, definition.SMBOwnership)
	mountPath, err := store.MountUnitPath(definition)
	require.NoError(t, err)
	contents, err := os.ReadFile(mountPath)
	require.NoError(t, err)
	assert.Contains(t, string(contents), "gid=100")
	assert.Contains(t, string(contents), "uid=1000")
}

func TestRemoteManagerRejectsInvalidOrNFSOwnershipBeforeMutation(t *testing.T) {
	for _, request := range []CreateRequest{
		func() CreateRequest { value := testSMBRequest(t); value.UID = "1000"; return value }(),
		func() CreateRequest {
			value := testNFSRequest(t)
			value.SMBOwnership = SMBOwnership{UID: "1000", GID: "100"}
			return value
		}(),
	} {
		store := testArtifactStore(t)
		manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
		require.Error(t, manager.Create(context.Background(), request))
		assert.NoDirExists(t, request.Target)
		assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
	}
}

func TestRemoteManagerStateDoesNotWaitForLifecycleOperation(t *testing.T) {
	store := testArtifactStore(t)
	units := &blockingUnitController{stopStarted: make(chan struct{}), release: make(chan struct{})}
	manager := NewSystemRemoteManager(staticManager{}, store, units)
	request := testNFSRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	units.blockStop = true

	deleted := make(chan error, 1)
	go func() { deleted <- manager.Delete(context.Background(), request.ID) }()
	<-units.stopStarted
	state := make(chan error, 1)
	go func() { _, err := manager.State(context.Background()); state <- err }()
	select {
	case err := <-state:
		require.NoError(t, err)
	case <-time.After(100 * time.Millisecond):
		t.Error("State waited for lifecycle operation")
	}
	close(units.release)
	if err := <-deleted; err != nil {
		t.Error(err)
	}
}

func TestRemoteManagerStateSkipsManifestRemovedAfterListing(t *testing.T) {
	store := testArtifactStore(t)
	request := testNFSRequest(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	require.NoError(t, manager.Create(context.Background(), request))
	manifest, err := store.ManifestPath(request.ID)
	require.NoError(t, err)
	manager.artifacts.beforeLoad = func() { require.NoError(t, os.Remove(manifest)) }

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Empty(t, snapshot.Mounts)
}

func TestRemoteManagerStateDegradesGracefullyOnCorruptManifest(t *testing.T) {
	store := testArtifactStore(t)
	corrupt := testNFSRequest(t)
	valid := testNFSRequest(t)
	valid.ID = "fedcba9876543210fedcba9876543210"
	valid.Target = filepath.Join(t.TempDir(), "other")
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	require.NoError(t, manager.Create(context.Background(), corrupt))
	require.NoError(t, manager.Create(context.Background(), valid))
	manifest, err := store.ManifestPath(corrupt.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest, []byte("invalid"), 0o600))

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	require.Len(t, snapshot.Mounts, 1)
	assert.Equal(t, "remote:"+valid.ID, snapshot.Mounts[0].ID)
	assert.Contains(t, snapshot.Findings, Finding{ResourceID: "remote:" + corrupt.ID, Severity: HealthWarning, Title: "Managed mount manifest is invalid", Detail: "A managed remote mount manifest failed verification and was excluded from the inventory."})
}

func TestRemoteManagerCreateRefusesWhileManifestIsCorrupt(t *testing.T) {
	store := testArtifactStore(t)
	corrupt := testNFSRequest(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	require.NoError(t, manager.Create(context.Background(), corrupt))
	manifest, err := store.ManifestPath(corrupt.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest, []byte("invalid"), 0o600))

	request := testNFSRequest(t)
	request.ID = "fedcba9876543210fedcba9876543210"
	request.Target = filepath.Join(t.TempDir(), "other")
	assert.ErrorIs(t, manager.Create(context.Background(), request), errInvalidManifest)
}

func TestRemoteManagerSerializesSameDefinitionButNotDifferentDefinitions(t *testing.T) {
	store := testArtifactStore(t)
	units := &blockingUnitController{entered: make(chan string, 3), release: make(chan struct{})}
	manager := NewSystemRemoteManager(staticManager{}, store, units)
	first := testNFSRequest(t)
	second := testNFSRequest(t)
	second.ID = "fedcba9876543210fedcba9876543210"
	second.Target = filepath.Join(t.TempDir(), "other")
	require.NoError(t, manager.Create(context.Background(), first))
	require.NoError(t, manager.Create(context.Background(), second))
	units.blockStart = true
	units.ids = map[string]string{automountUnitName(first.Target): first.ID, automountUnitName(second.Target): second.ID}

	one := make(chan error, 1)
	go func() { one <- manager.Mount(context.Background(), first.ID) }()
	require.Equal(t, first.ID, <-units.entered)
	same := make(chan error, 1)
	go func() { same <- manager.Mount(context.Background(), first.ID) }()
	different := make(chan error, 1)
	go func() { different <- manager.Mount(context.Background(), second.ID) }()
	select {
	case id := <-units.entered:
		require.Equal(t, second.ID, id)
	case <-time.After(100 * time.Millisecond):
		units.release <- struct{}{}
		require.NoError(t, <-one)
		t.Error("different IDs were serialized")
		return
	}
	select {
	case id := <-units.entered:
		t.Fatalf("same ID overlapped: %s", id)
	case <-time.After(100 * time.Millisecond):
	}
	units.release <- struct{}{}
	units.release <- struct{}{}
	require.NoError(t, <-one)
	require.NoError(t, <-different)
	require.Equal(t, first.ID, <-units.entered)
	units.release <- struct{}{}
	require.NoError(t, <-same)
}

type blockingUnitController struct {
	blockStart  bool
	blockStop   bool
	entered     chan string
	ids         map[string]string
	release     chan struct{}
	stopStarted chan struct{}
	stopOnce    sync.Once
}

func (*blockingUnitController) DaemonReload(context.Context) error    { return nil }
func (*blockingUnitController) Disable(context.Context, string) error { return nil }
func (*blockingUnitController) Enable(context.Context, string) error  { return nil }
func (u *blockingUnitController) Start(_ context.Context, name string) error {
	if u.blockStart {
		u.entered <- u.ids[name]
		<-u.release
	}
	return nil
}
func (u *blockingUnitController) Stop(_ context.Context, _ string) error {
	if u.blockStop {
		u.stopOnce.Do(func() { close(u.stopStarted) })
		<-u.release
	}
	return nil
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

func TestRemoteManagerCreateRejectsTargetContainingInactiveDefinition(t *testing.T) {
	store := testArtifactStore(t)
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	request := testNFSRequest(t)
	parent := filepath.Join(filepath.Dir(request.Target), "parent")
	require.NoError(t, os.Mkdir(parent, 0o755))
	request.Target = filepath.Join(parent, "child")
	definition, err := manager.definition(request)
	require.NoError(t, err)
	definition.State = "inactive"
	require.NoError(t, store.WriteMountUnit(definition))
	require.NoError(t, store.WriteAutomountUnit(definition))
	require.NoError(t, store.WriteManifest(definition))

	request.ID = "fedcba9876543210fedcba9876543210"
	request.Target = parent
	assert.Error(t, manager.Create(context.Background(), request))
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
	require.NoError(t, ManagedPage(snapshot, false, "csrf-token", true, true).Render(context.Background(), &html))
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
		"stop " + automountUnitName(request.Target), "stop " + mountUnitName(request.Target), "disable " + automountUnitName(request.Target), "reload",
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

func TestRemoteManagerDeleteResumesInterruptedCleanup(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testSMBRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	controller.fail = "reload"

	require.Error(t, manager.Delete(context.Background(), request.ID))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	assert.Equal(t, "needs-attention", definition.State)
	mountPath, _ := store.MountUnitPath(definition)
	assert.NoFileExists(t, mountPath)

	controller.fail = ""
	require.NoError(t, manager.Delete(context.Background(), request.ID))
	assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
}

func TestRemoteManagerDeleteDeactivatesTamperedDefinitionAndRecovers(t *testing.T) {
	store := testArtifactStore(t)
	controller := &recordingUnitController{}
	manager := NewSystemRemoteManager(staticManager{}, store, controller)
	request := testNFSRequest(t)
	require.NoError(t, manager.Create(context.Background(), request))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	mountPath, _ := store.MountUnitPath(definition)
	require.NoError(t, os.WriteFile(mountPath, []byte("tampered"), 0o644))
	controller.calls = nil

	require.Error(t, manager.Delete(context.Background(), request.ID))

	assert.Equal(t, []string{
		"stop " + automountUnitName(request.Target), "stop " + mountUnitName(request.Target), "disable " + automountUnitName(request.Target),
	}, controller.calls)
	definition, err = store.LoadDefinition(request.ID)
	require.NoError(t, err)
	assert.Equal(t, "needs-attention", definition.State)
	contents, err := os.ReadFile(mountPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("tampered"), contents)

	require.NoError(t, os.Remove(mountPath))
	require.NoError(t, manager.Delete(context.Background(), request.ID))
	assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
}

func TestRemoteManagerStateRendersIPv6NFSSource(t *testing.T) {
	store := testArtifactStore(t)
	request := testNFSRequest(t)
	request.Host = "fd00::5"
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
	require.NoError(t, manager.Create(context.Background(), request))

	snapshot, err := manager.State(context.Background())

	require.NoError(t, err)
	require.Len(t, snapshot.Mounts, 1)
	assert.Equal(t, "[fd00::5]:/exports/media", snapshot.Mounts[0].Source)
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
