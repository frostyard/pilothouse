package incus

import (
	"context"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateSnapshotTakesNonStatefulSnapshot(t *testing.T) {
	client := stateClient()
	require.NoError(t, NewSystemManager(client).CreateSnapshot(context.Background(), "production", "api", "before-patch"))
	assert.Equal(t, "snapshot create production api before-patch", client.actions[len(client.actions)-1])
}

// TestCreateSnapshotRejectsDuplicateName proves a create cannot silently
// overwrite: the fixture already carries "nightly".
func TestCreateSnapshotRejectsDuplicateName(t *testing.T) {
	client := stateClient()
	err := NewSystemManager(client).CreateSnapshot(context.Background(), "production", "api", "nightly")
	assert.EqualError(t, err, "a snapshot with that name already exists")
	assertNoSnapshotMutation(t, client)
}

func TestCreateSnapshotValidatesName(t *testing.T) {
	for _, name := range []string{
		"", "-leading", "trailing-", ".leading", "trailing.",
		"has/slash", "has space", "has:colon", "../escape", strings.Repeat("x", 64),
	} {
		client := stateClient()
		err := NewSystemManager(client).CreateSnapshot(context.Background(), "production", "api", name)
		assert.EqualError(t, err, "invalid snapshot name", "name %q", name)
		assertNoSnapshotMutation(t, client)
	}

	for _, name := range []string{"a", "nightly-1", "before.upgrade", "snap_01", strings.Repeat("x", 63)} {
		assert.True(t, validSnapshotName(name), name)
	}
}

// TestRestoreSnapshotRequiresStoppedInstance pins the precondition Incus
// itself enforces for a non-stateful snapshot, checked here so the operator
// gets an actionable message instead of an opaque API error.
func TestRestoreSnapshotRequiresStoppedInstance(t *testing.T) {
	client := stateClient()
	err := NewSystemManager(client).RestoreSnapshot(context.Background(), "production", "api", "nightly")
	assert.EqualError(t, err, "stop the instance before restoring a snapshot")
	assertNoSnapshotMutation(t, client)

	// The same snapshot restores once the instance is stopped.
	client = stateClient()
	client.instances[1].StatusCode = api.Stopped
	client.instances[1].Status = "Stopped"
	require.NoError(t, NewSystemManager(client).RestoreSnapshot(context.Background(), "production", "api", "nightly"))
	assert.Equal(t, "snapshot restore production api nightly", client.actions[len(client.actions)-1])
}

// TestSnapshotMutationsRediscoverTheSnapshot proves both destructive
// actions resolve the name against the instance's live snapshot list before
// touching the API, so a crafted name never reaches Incus.
func TestSnapshotMutationsRediscoverTheSnapshot(t *testing.T) {
	for _, name := range []string{"", "does-not-exist", "api/nightly", "../nightly", "NIGHTLY"} {
		client := stateClient()
		err := NewSystemManager(client).DeleteSnapshot(context.Background(), "production", "api", name)
		assert.EqualError(t, err, "snapshot no longer exists", "delete %q", name)
		assertNoSnapshotMutation(t, client)

		client = stateClient()
		client.instances[1].StatusCode = api.Stopped
		err = NewSystemManager(client).RestoreSnapshot(context.Background(), "production", "api", name)
		assert.EqualError(t, err, "snapshot no longer exists", "restore %q", name)
		assertNoSnapshotMutation(t, client)
	}
}

func TestDeleteSnapshotRemovesExistingSnapshot(t *testing.T) {
	client := stateClient()
	require.NoError(t, NewSystemManager(client).DeleteSnapshot(context.Background(), "production", "api", "nightly"))
	assert.Equal(t, "snapshot delete production api nightly", client.actions[len(client.actions)-1])
}

// TestSnapshotActionsValidateInstanceAndProject proves all three actions
// inherit the same instance-name and project validation the rest of the
// manager applies.
func TestSnapshotActionsValidateInstanceAndProject(t *testing.T) {
	manager := NewSystemManager(stateClient())

	for _, call := range []struct {
		name string
		run  func(project, instance string) error
	}{
		{"create", func(project, instance string) error {
			return manager.CreateSnapshot(context.Background(), project, instance, "fresh")
		}},
		{"delete", func(project, instance string) error {
			return manager.DeleteSnapshot(context.Background(), project, instance, "nightly")
		}},
		{"restore", func(project, instance string) error {
			return manager.RestoreSnapshot(context.Background(), project, instance, "nightly")
		}},
	} {
		assert.EqualError(t, call.run("production", "../default/api"), "invalid instance name", call.name)
		assert.EqualError(t, call.run("missing", "api"), "project is not available", call.name)
	}
}

// TestStopForceKillsInstance proves the forced path reaches the client with
// force set, and that the graceful path still does not.
func TestStopForceKillsInstance(t *testing.T) {
	client := stateClient()
	manager := NewSystemManager(client)

	require.NoError(t, manager.StopForce(context.Background(), "production", "api"))
	assert.Equal(t, "stop 30 force=true production api", client.actions[len(client.actions)-1])

	require.NoError(t, manager.Stop(context.Background(), "production", "api"))
	assert.Equal(t, "stop 30 force=false production api", client.actions[len(client.actions)-1])
}

func TestStopForceRequiresRunningInstance(t *testing.T) {
	manager := NewSystemManager(stateClient())
	assert.EqualError(t, manager.StopForce(context.Background(), "production", "worker-vm"), "instance is not running")
	assert.EqualError(t, manager.StopForce(context.Background(), "production", "../default/api"), "invalid instance name")
}

// assertNoSnapshotMutation proves a rejection happened before any snapshot
// call reached the client, rather than only that an error was returned.
func assertNoSnapshotMutation(t *testing.T, client *fakeClient) {
	t.Helper()
	for _, action := range client.actions {
		assert.False(t, strings.HasPrefix(action, "snapshot "), "no snapshot call may reach the client, got %q", action)
	}
}
