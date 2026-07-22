package storage

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveToolRequiresRootOwnedSafeRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lsblk")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))

	_, err := resolveTool([]string{path}, func(string) (fileIdentity, error) {
		return fileIdentity{Mode: 0o755, UID: 1000, Regular: true}, nil
	})

	assert.ErrorContains(t, err, "root-owned")
}

func TestResolveToolRejectsNonRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lsblk")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))

	_, err := resolveTool([]string{path}, func(string) (fileIdentity, error) {
		return fileIdentity{Mode: 0o755, UID: 0}, nil
	})

	assert.ErrorContains(t, err, "regular")
}

func TestResolveToolRejectsGroupWritableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lsblk")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))

	_, err := resolveTool([]string{path}, func(string) (fileIdentity, error) {
		return fileIdentity{Mode: 0o775, UID: 0, Regular: true}, nil
	})

	assert.ErrorContains(t, err, "writable")
}

func TestResolveToolAcceptsRootOwnedSafeRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lsblk")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))

	resolved, err := resolveTool([]string{path}, func(string) (fileIdentity, error) {
		return fileIdentity{Mode: 0o755, UID: 0, Regular: true}, nil
	})

	require.NoError(t, err)
	assert.Equal(t, path, resolved)
}

func TestResolveOptionalToolReturnsUnsupportedWithoutError(t *testing.T) {
	path, supported, err := resolveOptionalTool([]string{"/does/not/exist"}, os.Lstat, os.Stat)

	require.NoError(t, err)
	assert.False(t, supported)
	assert.Empty(t, path)
}

func TestResolveOptionalToolAcceptsSymlinkToSafeTarget(t *testing.T) {
	link := optionalToolFileInfo{mode: os.ModeSymlink | 0o777, uid: 0}
	target := optionalToolFileInfo{mode: 0o755, uid: 0}

	path, supported, err := resolveOptionalTool(
		[]string{"/usr/sbin/tool"},
		func(string) (os.FileInfo, error) { return link, nil },
		func(string) (os.FileInfo, error) { return target, nil },
	)

	require.NoError(t, err)
	assert.True(t, supported)
	assert.Equal(t, "/usr/sbin/tool", path)
}

func TestResolveOptionalToolRejectsBrokenSymlink(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.Symlink(filepath.Join(directory, "missing"), link))

	_, supported, err := resolveOptionalTool([]string{link}, os.Lstat, os.Stat)

	assert.ErrorContains(t, err, "resolve optional tool")
	assert.False(t, supported)
}

func TestResolveOptionalToolRejectsUnsafeResolvedTarget(t *testing.T) {
	link := optionalToolFileInfo{mode: os.ModeSymlink | 0o777, uid: 0}
	target := optionalToolFileInfo{mode: 0o775, uid: 0}

	_, supported, err := resolveOptionalTool(
		[]string{"/usr/sbin/tool"},
		func(string) (os.FileInfo, error) { return link, nil },
		func(string) (os.FileInfo, error) { return target, nil },
	)

	assert.ErrorContains(t, err, "writable")
	assert.False(t, supported)
}

func TestResolveOptionalToolRejectsNonRootOwnedResolvedTarget(t *testing.T) {
	link := optionalToolFileInfo{mode: os.ModeSymlink | 0o777, uid: 0}
	target := optionalToolFileInfo{mode: 0o755, uid: 1000}

	_, supported, err := resolveOptionalTool(
		[]string{"/usr/sbin/tool"},
		func(string) (os.FileInfo, error) { return link, nil },
		func(string) (os.FileInfo, error) { return target, nil },
	)

	assert.ErrorContains(t, err, "not root-owned")
	assert.False(t, supported)
}

func TestResolveOptionalToolRejectsNonRegularResolvedTarget(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.Symlink(directory, link))

	_, supported, err := resolveOptionalTool([]string{link}, os.Lstat, os.Stat)

	assert.ErrorContains(t, err, "not a regular file")
	assert.False(t, supported)
}

type optionalToolFileInfo struct {
	mode os.FileMode
	uid  uint32
}

func (info optionalToolFileInfo) Name() string       { return "tool" }
func (info optionalToolFileInfo) Size() int64        { return 0 }
func (info optionalToolFileInfo) Mode() os.FileMode  { return info.mode }
func (info optionalToolFileInfo) ModTime() time.Time { return time.Time{} }
func (info optionalToolFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info optionalToolFileInfo) Sys() any           { return &syscall.Stat_t{Uid: info.uid} }

func TestResolveOptionalToolRejectsUnsafeCandidateAfterSafeCandidate(t *testing.T) {
	safe := optionalToolFileInfo{mode: 0o755, uid: 0}
	unsafe := optionalToolFileInfo{mode: os.ModeSymlink | 0o777, uid: 0}
	target := optionalToolFileInfo{mode: 0o775, uid: 0}

	_, supported, err := resolveOptionalTool(
		[]string{"/usr/sbin/safe-tool", "/usr/sbin/tool"},
		func(path string) (os.FileInfo, error) {
			if path == "/usr/sbin/safe-tool" {
				return safe, nil
			}
			return unsafe, nil
		},
		func(path string) (os.FileInfo, error) {
			if path == "/usr/sbin/safe-tool" {
				return safe, nil
			}
			return target, nil
		},
	)

	assert.ErrorContains(t, err, "writable")
	assert.False(t, supported)
}

func TestCommandRunnerReturnsCapturedOutputOnExitFailure(t *testing.T) {
	runner := commandRunner{limit: 64}

	output, err := runner.Run(context.Background(), "/bin/sh", "-c", "echo data; exit 4")

	require.Error(t, err)
	assert.Equal(t, "data\n", string(output))
}

func TestBoundedRunnerRejectsOversizedOutput(t *testing.T) {
	runner := commandRunner{limit: 8, run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("123456789"), nil
	}}

	_, err := runner.Run(context.Background(), "/usr/bin/lsblk", "--json")

	assert.ErrorIs(t, err, errOutputTooLarge)
}
