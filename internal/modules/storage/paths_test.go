//go:build linux

package storage

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const testDefinitionID = "0123456789abcdef0123456789abcdef"

func newPathManager(t *testing.T) PathManager {
	t.Helper()
	manager, err := NewPathManager(testDefinitionID)
	require.NoError(t, err)
	return manager
}

func TestValidateTargetRejectsProtectedTrees(t *testing.T) {
	manager := newPathManager(t)
	for _, target := range []string{
		"/",
		"/proc", "/proc/x",
		"/sys", "/sys/x",
		"/dev", "/dev/x",
		"/run", "/run/x",
		"/boot", "/boot/x",
		"/etc", "/etc/x",
		"/usr", "/usr/x",
		"/var/lib/pilothouse", "/var/lib/pilothouse/x",
	} {
		t.Run(target, func(t *testing.T) {
			assert.Error(t, manager.ValidateTarget(context.Background(), target, nil))
		})
	}
}

func TestNewPathManagerRequiresValidDefinitionID(t *testing.T) {
	_, err := NewPathManager("new")
	assert.Error(t, err)
	_, err = NewPathManager(testDefinitionID)
	assert.NoError(t, err)
}

func TestValidateTargetRejectsPolicyBeforeFilesystemAccess(t *testing.T) {
	fs := &recordingPathFS{}
	manager := PathManager{DefinitionID: "definition", fs: fs}

	assert.Error(t, manager.ValidateTarget(context.Background(), "/etc/pilothouse", nil))
	assert.Zero(t, fs.openRootCalls)
	assert.Error(t, manager.ValidateTarget(context.Background(), "/safe", &TargetInventory{UnitOwners: map[string]string{mountUnitName("/safe"): "other"}}))
	assert.Zero(t, fs.openRootCalls)
}

func TestValidateTargetRejectsUnsafePaths(t *testing.T) {
	manager := newPathManager(t)
	for _, target := range []string{"relative", "/tmp/../tmp", "/tmp//target"} {
		t.Run(target, func(t *testing.T) {
			assert.Error(t, manager.ValidateTarget(context.Background(), target, nil))
		})
	}
}

func TestValidateTargetRejectsSymlinksInEveryComponent(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(real, 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(real, "child"), 0o755))
	require.NoError(t, os.Symlink(real, filepath.Join(base, "link")))
	require.NoError(t, os.Symlink(filepath.Join(real, "child"), filepath.Join(real, "leaf-link")))

	manager := newPathManager(t)
	assert.Error(t, manager.ValidateTarget(context.Background(), filepath.Join(base, "link", "child"), nil))
	assert.Error(t, manager.ValidateTarget(context.Background(), filepath.Join(real, "leaf-link"), nil))
}

func TestValidateTargetRejectsExistingFileAndNonEmptyDirectory(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	nonEmpty := filepath.Join(base, "non-empty")
	require.NoError(t, os.Mkdir(nonEmpty, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nonEmpty, "child"), []byte("x"), 0o600))

	manager := newPathManager(t)
	assert.Error(t, manager.ValidateTarget(context.Background(), file, nil))
	assert.Error(t, manager.ValidateTarget(context.Background(), nonEmpty, nil))
	empty := filepath.Join(base, "empty")
	require.NoError(t, os.Mkdir(empty, 0o755))
	assert.NoError(t, manager.ValidateTarget(context.Background(), empty, nil))
	assert.NoError(t, manager.ValidateTarget(context.Background(), filepath.Join(base, "missing"), nil))
}

func TestValidateTargetRejectsMountAndUnitConflicts(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	manager := newPathManager(t)

	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{Mounts: []string{target}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{Mounts: []string{filepath.Join(target, "nested")}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{mountUnitName(target): "fedcba9876543210fedcba9876543210"}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{automountUnitName(target): "fedcba9876543210fedcba9876543210"}}))
	assert.NoError(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{mountUnitName(target): manager.DefinitionID}}))
}

func TestValidateTargetRejectsInvalidInventory(t *testing.T) {
	manager := newPathManager(t)
	target := filepath.Join(t.TempDir(), "target")

	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{Mounts: []string{"relative"}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{"unsafe/name.mount": testDefinitionID}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{mountUnitName(target): "invalid"}}))
}

func TestMountUnitNameEscapesLiteralHyphens(t *testing.T) {
	assert.Equal(t, "var-lib-media\\x2darchive.mount", mountUnitName("/var/lib/media-archive"))
}

func TestCreateTargetCreatesOnlyMissingLeafWithExpectedMode(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	manager := newPathManager(t)

	created, err := manager.CreateTarget(context.Background(), target, nil)
	require.NoError(t, err)
	assert.True(t, created)
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	assert.Equal(t, uint32(os.Getuid()), stat.Uid)

	created, err = manager.CreateTarget(context.Background(), target, nil)
	require.NoError(t, err)
	assert.False(t, created)
	_, err = manager.CreateTarget(context.Background(), filepath.Join(base, "missing", "nested"), nil)
	assert.Error(t, err)
}

func TestCreateTargetRejectsConflictingInventory(t *testing.T) {
	manager := newPathManager(t)
	target := filepath.Join(t.TempDir(), "target")

	created, err := manager.CreateTarget(context.Background(), target, &TargetInventory{Mounts: []string{target}})
	assert.Error(t, err)
	assert.False(t, created)
	assert.NoDirExists(t, target)
}

func TestRemoveTargetOnlyRemovesManagerCreatedEmptyDirectory(t *testing.T) {
	base := t.TempDir()
	manager := newPathManager(t)

	notCreated := filepath.Join(base, "not-created")
	require.NoError(t, os.Mkdir(notCreated, 0o755))
	require.NoError(t, manager.RemoveTarget(context.Background(), notCreated, false, nil))
	assert.DirExists(t, notCreated)

	nonEmpty := filepath.Join(base, "non-empty")
	require.NoError(t, os.Mkdir(nonEmpty, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nonEmpty, "child"), []byte("x"), 0o600))
	assert.Error(t, manager.RemoveTarget(context.Background(), nonEmpty, true, nil))
	assert.DirExists(t, nonEmpty)

	empty := filepath.Join(base, "empty")
	require.NoError(t, os.Mkdir(empty, 0o755))
	require.NoError(t, manager.RemoveTarget(context.Background(), empty, true, nil))
	assert.NoDirExists(t, empty)
}

func TestRemoveTargetRequiresManifestCreatedFlag(t *testing.T) {
	manager := newPathManager(t)
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.Mkdir(target, 0o755))

	require.NoError(t, manager.RemoveTarget(context.Background(), target, false, nil))
	assert.DirExists(t, target)
}

func TestRemoveTargetRejectsFreshConflictingInventory(t *testing.T) {
	manager := newPathManager(t)
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.Mkdir(target, 0o755))

	assert.Error(t, manager.RemoveTarget(context.Background(), target, true, &TargetInventory{Mounts: []string{target}}))
	assert.DirExists(t, target)
}

type recordingPathFS struct {
	openRootCalls int
}

func (fs *recordingPathFS) close(int) error                                 { return nil }
func (fs *recordingPathFS) empty(int) (bool, error)                         { return true, nil }
func (fs *recordingPathFS) fstat(int, *unix.Stat_t) error                   { return nil }
func (fs *recordingPathFS) mkdirat(int, string, uint32) error               { return nil }
func (fs *recordingPathFS) openat2(int, string, *unix.OpenHow) (int, error) { return 0, nil }
func (fs *recordingPathFS) openRoot() (int, error) {
	fs.openRootCalls++
	return 0, nil
}
func (fs *recordingPathFS) unlinkat(int, string, int) error { return nil }
