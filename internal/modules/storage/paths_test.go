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

func TestValidateTargetRejectsProtectedTrees(t *testing.T) {
	manager := NewPathManager()
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

func TestValidateTargetRejectsPolicyBeforeFilesystemAccess(t *testing.T) {
	fs := &recordingPathFS{}
	manager := PathManager{DefinitionID: "definition", fs: fs}

	assert.Error(t, manager.ValidateTarget(context.Background(), "/etc/pilothouse", nil))
	assert.Zero(t, fs.openRootCalls)
	assert.Error(t, manager.ValidateTarget(context.Background(), "/safe", &TargetInventory{UnitOwners: map[string]string{mountUnitName("/safe"): "other"}}))
	assert.Zero(t, fs.openRootCalls)
}

func TestValidateTargetRejectsUnsafePaths(t *testing.T) {
	manager := NewPathManager()
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

	manager := NewPathManager()
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

	manager := NewPathManager()
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
	manager := NewPathManager()

	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{Mounts: []string{target}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{Mounts: []string{filepath.Join(target, "nested")}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{mountUnitName(target): "other"}}))
	assert.Error(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{automountUnitName(target): "other"}}))
	assert.NoError(t, manager.ValidateTarget(context.Background(), target, &TargetInventory{UnitOwners: map[string]string{mountUnitName(target): manager.DefinitionID}}))
}

func TestMountUnitNameEscapesLiteralHyphens(t *testing.T) {
	assert.Equal(t, "var-lib-media\\x2darchive.mount", mountUnitName("/var/lib/media-archive"))
}

func TestCreateTargetCreatesOnlyMissingLeafWithExpectedMode(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	manager := NewPathManager()

	created, err := manager.CreateTarget(context.Background(), target)
	require.NoError(t, err)
	assert.True(t, created)
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	assert.Equal(t, uint32(os.Getuid()), stat.Uid)

	created, err = manager.CreateTarget(context.Background(), target)
	require.NoError(t, err)
	assert.False(t, created)
	_, err = manager.CreateTarget(context.Background(), filepath.Join(base, "missing", "nested"))
	assert.Error(t, err)
}

func TestRemoveTargetOnlyRemovesManagerCreatedEmptyDirectory(t *testing.T) {
	base := t.TempDir()
	manager := NewPathManager()

	notCreated := filepath.Join(base, "not-created")
	require.NoError(t, os.Mkdir(notCreated, 0o755))
	require.NoError(t, manager.RemoveTarget(context.Background(), notCreated, false))
	assert.DirExists(t, notCreated)

	nonEmpty := filepath.Join(base, "non-empty")
	require.NoError(t, os.Mkdir(nonEmpty, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nonEmpty, "child"), []byte("x"), 0o600))
	assert.Error(t, manager.RemoveTarget(context.Background(), nonEmpty, true))
	assert.DirExists(t, nonEmpty)

	empty := filepath.Join(base, "empty")
	require.NoError(t, os.Mkdir(empty, 0o755))
	require.NoError(t, manager.RemoveTarget(context.Background(), empty, true))
	assert.NoDirExists(t, empty)
}

type recordingPathFS struct {
	openRootCalls int
}

func (fs *recordingPathFS) close(int) error                                 { return nil }
func (fs *recordingPathFS) empty(int, string) (bool, error)                 { return true, nil }
func (fs *recordingPathFS) fstat(int, *unix.Stat_t) error                   { return nil }
func (fs *recordingPathFS) mkdirat(int, string, uint32) error               { return nil }
func (fs *recordingPathFS) openat2(int, string, *unix.OpenHow) (int, error) { return 0, nil }
func (fs *recordingPathFS) openRoot() (int, error) {
	fs.openRootCalls++
	return 0, nil
}
func (fs *recordingPathFS) unlinkat(int, string, int) error { return nil }
