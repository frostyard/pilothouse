package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	path, supported, err := resolveOptionalTool([]string{"/does/not/exist"}, os.Lstat)

	require.NoError(t, err)
	assert.False(t, supported)
	assert.Empty(t, path)
}

func TestResolveOptionalToolRejectsExistingSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.WriteFile(target, []byte("tool"), 0o755))
	require.NoError(t, os.Symlink(target, link))

	_, supported, err := resolveOptionalTool([]string{link}, os.Lstat)

	assert.Error(t, err)
	assert.False(t, supported)
}

func TestResolveOptionalToolRejectsUnsafeCandidateAfterSafeCandidate(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.WriteFile(target, []byte("tool"), 0o755))
	require.NoError(t, os.Symlink(target, link))

	_, supported, err := resolveOptionalTool([]string{"/usr/bin/lsblk", link}, os.Lstat)

	assert.Error(t, err)
	assert.False(t, supported)
}

func TestBoundedRunnerRejectsOversizedOutput(t *testing.T) {
	runner := commandRunner{limit: 8, run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("123456789"), nil
	}}

	_, err := runner.Run(context.Background(), "/usr/bin/lsblk", "--json")

	assert.ErrorIs(t, err, errOutputTooLarge)
}
