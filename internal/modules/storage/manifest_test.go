//go:build linux

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newArtifactStore(t *testing.T) ArtifactStore {
	t.Helper()
	root := t.TempDir()
	return ArtifactStore{
		ManifestRoot:   filepath.Join(root, "manifests"),
		CredentialRoot: filepath.Join(root, "credentials"),
		UnitRoot:       filepath.Join(root, "units"),
		UID:            os.Getuid(),
		GID:            os.Getgid(),
	}
}

func testDefinition() Definition {
	return Definition{
		FormatVersion:   ManifestFormatVersion,
		ID:              testDefinitionID,
		Protocol:        "nfs",
		ProtocolVersion: "4",
		Host:            "nas.example",
		Export:          "/exports/media",
		Target:          "/mnt/media",
		UnitName:        "mnt-media.mount",
		State:           "active",
	}
}

func TestManifestWriteAndLoadAreDeterministicAndSecretFree(t *testing.T) {
	store := newArtifactStore(t)
	definition := testDefinition()

	require.NoError(t, store.WriteManifest(definition))
	path := filepath.Join(store.ManifestRoot, definition.ID+".json")
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, store.WriteManifest(definition))
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.NotContains(t, string(first), "never-record-this-secret")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	loaded, err := store.LoadDefinition(definition.ID)
	require.NoError(t, err)
	assert.Equal(t, definition, loaded)
}

func TestLoadDefinitionRejectsUnknownOrInvalidDerivedFields(t *testing.T) {
	store := newArtifactStore(t)
	require.NoError(t, os.MkdirAll(store.ManifestRoot, 0o700))
	path := filepath.Join(store.ManifestRoot, testDefinitionID+".json")

	require.NoError(t, os.WriteFile(path, []byte(`{"format_version":1,"id":"0123456789abcdef0123456789abcdef","protocol":"nfs","protocol_version":"4","host":"nas.example","export":"/exports/media","target":"/mnt/media","unit_name":"wrong.mount","state":"active","unknown":true}`), 0o600))
	_, err := store.LoadDefinition(testDefinitionID)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "unknown")

	require.NoError(t, os.WriteFile(path, []byte(`{"format_version":2,"id":"0123456789abcdef0123456789abcdef","protocol":"nfs","protocol_version":"4","host":"nas.example","export":"/exports/media","target":"/mnt/media","unit_name":"mnt-media.mount","state":"active"}`), 0o600))
	_, err = store.LoadDefinition(testDefinitionID)
	require.Error(t, err)
}

func TestManifestRefusesToReplaceUnmanagedFile(t *testing.T) {
	store := newArtifactStore(t)
	definition := testDefinition()
	require.NoError(t, os.MkdirAll(store.ManifestRoot, 0o700))
	path := filepath.Join(store.ManifestRoot, definition.ID+".json")
	require.NoError(t, os.WriteFile(path, []byte("unmanaged"), 0o600))

	err := store.WriteManifest(definition)
	require.Error(t, err)
	assert.EqualError(t, err, "artifact is not managed by Pilothouse")
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "unmanaged", string(content))
	entries, readErr := os.ReadDir(store.ManifestRoot)
	require.NoError(t, readErr)
	assert.Len(t, entries, 1)
}

func TestAtomicManifestWriteDoesNotReplaceRacingUnmanagedFile(t *testing.T) {
	store := newArtifactStore(t)
	definition := testDefinition()
	store.rename = func(_, destination string) error {
		require.NoError(t, os.WriteFile(destination, []byte("unmanaged"), 0o600))
		return os.ErrExist
	}

	err := store.WriteManifest(definition)
	assert.EqualError(t, err, "artifact is not managed by Pilothouse")
	path := filepath.Join(store.ManifestRoot, definition.ID+".json")
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "unmanaged", string(content))
	entries, readErr := os.ReadDir(store.ManifestRoot)
	require.NoError(t, readErr)
	assert.Len(t, entries, 1)
}

func TestManifestErrorsDoNotDisclosePassword(t *testing.T) {
	store := newArtifactStore(t)
	definition := testDefinition()
	definition.Target = "/mnt/invalid\nnever-record-this-secret"
	err := store.WriteManifest(definition)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "never-record-this-secret")
	assert.NotContains(t, strings.ToLower(err.Error()), "password")
}
