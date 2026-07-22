//go:build linux

package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMountUnit(t *testing.T) {
	for _, test := range []struct {
		name    string
		version string
		options string
	}{
		{"explicit version", "4", "nfsvers=4,nodev,nosuid,rw"},
		{"automatic version", "auto", "nodev,nosuid,rw"},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition()
			definition.ProtocolVersion = test.version
			actual, err := RenderMountUnit(definition)
			require.NoError(t, err)
			assert.Equal(t, "# Managed by Pilothouse; definition=0123456789abcdef0123456789abcdef\n[Unit]\nDescription=Pilothouse remote storage 0123456789abcdef0123456789abcdef\nWants=network-online.target\nAfter=network-online.target\n[Mount]\nWhat=nas.example:/exports/media\nWhere=/mnt/media\nType=nfs\nOptions="+test.options+"\nTimeoutSec=30\n", string(actual))
		})
	}
}

func TestRenderAutomountUnit(t *testing.T) {
	actual, err := RenderAutomountUnit(testDefinition())
	require.NoError(t, err)
	assert.Equal(t, "# Managed by Pilothouse; definition=0123456789abcdef0123456789abcdef\n[Unit]\nDescription=Pilothouse automount 0123456789abcdef0123456789abcdef\n[Automount]\nWhere=/mnt/media\nTimeoutIdleSec=300\n[Install]\nWantedBy=multi-user.target\n", string(actual))
}

func TestRenderEscapesSystemdValuesAndNeverIncludesPassword(t *testing.T) {
	definition := testDefinition()
	definition.Target = "/mnt/media archive%"
	definition.UnitName = "mnt-media\\x20archive\\x25.mount"
	definition.Host = "nas.example"
	definition.Export = "/exports/media"
	mount, err := RenderMountUnit(definition)
	require.NoError(t, err)
	assert.Contains(t, string(mount), "Where=/mnt/media\\x20archive%%")

	credentials, err := RenderCredentials("mount user", "never-record-this-secret")
	require.NoError(t, err)
	assert.Equal(t, "username=mount user\npassword=never-record-this-secret\n", string(credentials))
	assert.NotContains(t, string(mount), "never-record-this-secret")
}

func TestVerifyOwnedArtifactsRejectsModifiedArtifact(t *testing.T) {
	store := newArtifactStore(t)
	definition := testDefinition()
	require.NoError(t, store.WriteManifest(definition))
	require.NoError(t, store.WriteMountUnit(definition))
	require.NoError(t, store.WriteAutomountUnit(definition))
	require.NoError(t, store.VerifyOwnedArtifacts(definition))

	path := filepath.Join(store.UnitRoot, definition.UnitName)
	require.NoError(t, os.WriteFile(path, []byte("modified"), 0o644))
	err := store.VerifyOwnedArtifacts(definition)
	assert.EqualError(t, err, "artifact is not managed by Pilothouse")
}

func TestVerifyOwnedArtifactsRejectsCredentialMetadataTampering(t *testing.T) {
	store := newArtifactStore(t)
	definition := Definition{
		FormatVersion:   ManifestFormatVersion,
		ID:              testDefinitionID,
		Protocol:        "smb",
		ProtocolVersion: "3.1.1",
		Server:          "nas.example",
		Share:           "media",
		Target:          "/mnt/media",
		UnitName:        "mnt-media.mount",
		State:           "active",
		Username:        "mount-user",
	}
	credential, err := store.CredentialPath(definition.ID)
	require.NoError(t, err)
	definition.Credential = credential
	require.NoError(t, store.WriteManifest(definition))
	require.NoError(t, store.WriteMountUnit(definition))
	require.NoError(t, store.WriteAutomountUnit(definition))
	require.NoError(t, store.WriteCredentials(definition.ID, definition.Username, "never-record-this-secret"))
	require.NoError(t, store.VerifyOwnedArtifacts(definition))

	require.NoError(t, os.Chmod(credential, 0o644))
	assert.EqualError(t, store.VerifyOwnedArtifacts(definition), "artifact is not managed by Pilothouse")
	require.NoError(t, os.Chmod(credential, 0o600))
	store.UID++
	assert.EqualError(t, store.VerifyOwnedArtifacts(definition), "artifact is not managed by Pilothouse")
}

func TestCredentialAndUnitFilesUseExactModes(t *testing.T) {
	store := newArtifactStore(t)
	definition := testDefinition()
	require.NoError(t, store.WriteCredentials(definition.ID, "mount-user", "never-record-this-secret"))
	require.NoError(t, store.WriteMountUnit(definition))

	credential, err := os.Stat(filepath.Join(store.CredentialRoot, definition.ID))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), credential.Mode().Perm())
	unit, err := os.Stat(filepath.Join(store.UnitRoot, definition.UnitName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), unit.Mode().Perm())
	contents, err := os.ReadFile(filepath.Join(store.UnitRoot, definition.UnitName))
	require.NoError(t, err)
	assert.NotContains(t, string(contents), "never-record-this-secret")
}
