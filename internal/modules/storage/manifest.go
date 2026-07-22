//go:build linux

package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var errArtifactNotManaged = errors.New("artifact is not managed by Pilothouse")
var errInvalidManifest = errors.New("invalid manifest")
var errManifestNotFound = errors.Join(errInvalidManifest, os.ErrNotExist)

// ArtifactStore persists managed remote-mount artifacts. Production callers use
// NewArtifactStore; tests inject roots and the identity expected on disk.
type ArtifactStore struct {
	ManifestRoot   string
	CredentialRoot string
	UnitRoot       string
	UID            int
	GID            int
	rename         func(string, string) error
	beforeLoad     func()
}

func NewArtifactStore() ArtifactStore {
	return ArtifactStore{
		ManifestRoot:   "/var/lib/pilothouse/storage/mounts",
		CredentialRoot: "/etc/pilothouse/storage/credentials",
		UnitRoot:       "/etc/systemd/system",
	}
}

func (store ArtifactStore) ManifestPath(id string) (string, error) {
	if ValidateDefinitionID(id) != nil {
		return "", errInvalidManifest
	}
	return filepath.Join(store.ManifestRoot, id+".json"), nil
}

func (store ArtifactStore) CredentialPath(id string) (string, error) {
	if ValidateDefinitionID(id) != nil {
		return "", errInvalidManifest
	}
	return filepath.Join(store.CredentialRoot, id), nil
}

func (store ArtifactStore) MountUnitPath(definition Definition) (string, error) {
	if err := validateArtifactDefinition(definition, store); err != nil {
		return "", err
	}
	return filepath.Join(store.UnitRoot, definition.UnitName), nil
}

func (store ArtifactStore) AutomountUnitPath(definition Definition) (string, error) {
	path, err := store.MountUnitPath(definition)
	if err != nil {
		return "", err
	}
	return path[:len(path)-len(".mount")] + ".automount", nil
}

func (store ArtifactStore) WriteManifest(definition Definition) error {
	if err := validateArtifactDefinition(definition, store); err != nil {
		return err
	}
	path, err := store.ManifestPath(definition.ID)
	if err != nil {
		return err
	}
	contents, err := marshalManifest(definition)
	if err != nil {
		return errInvalidManifest
	}
	return store.writeArtifact(path, contents, 0o600)
}

// UpdateManifest replaces a verified managed manifest when lifecycle recovery
// must record its durable state.
func (store ArtifactStore) UpdateManifest(definition Definition) error {
	existing, err := store.LoadDefinition(definition.ID)
	if err != nil {
		return err
	}
	state := existing.State
	existing.State = definition.State
	if existing != definition || state == definition.State {
		return errArtifactNotManaged
	}
	path, _ := store.ManifestPath(definition.ID)
	contents, err := marshalManifest(definition)
	if err != nil {
		return errInvalidManifest
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pilothouse-")
	if err != nil {
		return errInvalidManifest
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if _, err := file.Write(contents); err != nil || unix.Fdatasync(int(file.Fd())) != nil || file.Chmod(0o600) != nil || file.Chown(store.UID, store.GID) != nil || file.Close() != nil {
		_ = file.Close()
		return errInvalidManifest
	}
	if err := os.Rename(temporary, path); err != nil {
		return errInvalidManifest
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return errInvalidManifest
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return errInvalidManifest
	}
	return nil
}

func (store ArtifactStore) LoadDefinition(id string) (Definition, error) {
	path, err := store.ManifestPath(id)
	if err != nil {
		return Definition{}, err
	}
	if store.beforeLoad != nil {
		store.beforeLoad()
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Definition{}, errManifestNotFound
		}
		return Definition{}, errInvalidManifest
	}
	if err := store.verifyFile(path, contents, 0o600); err != nil {
		return Definition{}, errInvalidManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var definition Definition
	if err := decoder.Decode(&definition); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Definition{}, errInvalidManifest
	}
	if definition.ID != id || validateArtifactDefinition(definition, store) != nil {
		return Definition{}, errInvalidManifest
	}
	expected, err := marshalManifest(definition)
	if err != nil || !bytes.Equal(contents, expected) {
		return Definition{}, errInvalidManifest
	}
	return definition, nil
}

func (store ArtifactStore) writeArtifact(path string, contents []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errInvalidManifest
	}
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, contents) && store.verifyFile(path, contents, mode) == nil {
			return nil
		}
		return errArtifactNotManaged
	} else if !errors.Is(err, os.ErrNotExist) {
		return errArtifactNotManaged
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".pilothouse-")
	if err != nil {
		return errInvalidManifest
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return errInvalidManifest
	}
	if err := unix.Fdatasync(int(file.Fd())); err != nil {
		_ = file.Close()
		return errInvalidManifest
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return errInvalidManifest
	}
	if err := file.Chown(store.UID, store.GID); err != nil {
		_ = file.Close()
		return errInvalidManifest
	}
	if err := unix.Fsync(int(file.Fd())); err != nil {
		_ = file.Close()
		return errInvalidManifest
	}
	if err := file.Close(); err != nil {
		return errInvalidManifest
	}
	if err := store.renameNoReplace(temporary, path); err != nil {
		if errors.Is(err, os.ErrExist) || errors.Is(err, unix.EEXIST) {
			return errArtifactNotManaged
		}
		return errInvalidManifest
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return errInvalidManifest
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return errInvalidManifest
	}
	return nil
}

func (store ArtifactStore) renameNoReplace(source, destination string) error {
	if store.rename != nil {
		return store.rename(source, destination)
	}
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

func (store ArtifactStore) verifyFile(path string, expected []byte, mode os.FileMode) error {
	if err := store.verifyMetadata(path, mode); err != nil {
		return err
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, expected) {
		return errArtifactNotManaged
	}
	return nil
}

func (store ArtifactStore) verifyMetadata(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return errArtifactNotManaged
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != store.UID || int(stat.Gid) != store.GID {
		return errArtifactNotManaged
	}
	return nil
}

func marshalManifest(definition Definition) ([]byte, error) {
	contents, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	return append(contents, '\n'), nil
}

func validateArtifactDefinition(definition Definition, store ArtifactStore) error {
	if validateDefinitionOwnership(definition.FormatVersion, definition.Protocol, definition.SMBOwnership) != nil || ValidateDefinitionID(definition.ID) != nil || ValidateProtocol(definition.Protocol) != nil || ValidateTarget(definition.Target) != nil || definition.State == "" {
		return errInvalidManifest
	}
	if definition.UnitName != mountUnitName(definition.Target) {
		return errInvalidManifest
	}
	switch definition.Protocol {
	case "nfs":
		if ValidateNFSHost(definition.Host) != nil || ValidateNFSExport(definition.Export) != nil || ValidateNFSVersion(definition.ProtocolVersion) != nil || definition.Server != "" || definition.Share != "" || definition.Username != "" || definition.Credential != "" {
			return errInvalidManifest
		}
	case "smb":
		if ValidateSMBServer(definition.Server) != nil || ValidateSMBShare(definition.Share) != nil || ValidateSMBVersion(definition.ProtocolVersion) != nil || definition.Host != "" || definition.Export != "" {
			return errInvalidManifest
		}
		if definition.Username == "" {
			if definition.Credential != "" {
				return errInvalidManifest
			}
		} else {
			credential, err := store.CredentialPath(definition.ID)
			if err != nil || ValidateUsername(definition.Username) != nil || definition.Credential != credential {
				return errInvalidManifest
			}
		}
	default:
		return errInvalidManifest
	}
	return nil
}

func artifactMarker(definition Definition) string {
	return "# Managed by Pilothouse; definition=" + definition.ID
}
