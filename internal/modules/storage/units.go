package storage

import (
	"bytes"
	"sort"
	"strings"
)

func RenderMountUnit(definition Definition) ([]byte, error) {
	if validateArtifactDefinition(definition, NewArtifactStore()) != nil {
		// Credential paths are store-specific, so validate the public fields here.
		if ValidateDefinitionID(definition.ID) != nil || ValidateTarget(definition.Target) != nil || definition.UnitName != mountUnitName(definition.Target) {
			return nil, errInvalidManifest
		}
	}
	what, filesystem, options, err := mountSettings(definition)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join([]string{
		artifactMarker(definition),
		"[Unit]",
		"Description=Pilothouse remote storage " + definition.ID,
		"Wants=network-online.target",
		"After=network-online.target",
		"[Mount]",
		"What=" + escapeSystemdValue(what),
		"Where=" + escapeSystemdValue(definition.Target),
		"Type=" + filesystem,
		"Options=" + strings.Join(options, ","),
		"TimeoutSec=30",
		"",
	}, "\n")), nil
}

func RenderAutomountUnit(definition Definition) ([]byte, error) {
	if ValidateDefinitionID(definition.ID) != nil || ValidateTarget(definition.Target) != nil || definition.UnitName != mountUnitName(definition.Target) {
		return nil, errInvalidManifest
	}
	return []byte(strings.Join([]string{
		artifactMarker(definition),
		"[Unit]",
		"Description=Pilothouse automount " + definition.ID,
		"[Automount]",
		"Where=" + escapeSystemdValue(definition.Target),
		"TimeoutIdleSec=300",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n")), nil
}

func RenderCredentials(username, password string) ([]byte, error) {
	if ValidateUsername(username) != nil || ValidatePassword(password) != nil {
		return nil, errInvalidManifest
	}
	return []byte("username=" + username + "\npassword=" + password + "\n"), nil
}

func (store ArtifactStore) WriteMountUnit(definition Definition) error {
	path, err := store.MountUnitPath(definition)
	if err != nil {
		return err
	}
	contents, err := RenderMountUnit(definition)
	if err != nil {
		return err
	}
	return store.writeArtifact(path, contents, 0o644)
}

func (store ArtifactStore) WriteAutomountUnit(definition Definition) error {
	path, err := store.AutomountUnitPath(definition)
	if err != nil {
		return err
	}
	contents, err := RenderAutomountUnit(definition)
	if err != nil {
		return err
	}
	return store.writeArtifact(path, contents, 0o644)
}

func (store ArtifactStore) WriteCredentials(id, username, password string) error {
	path, err := store.CredentialPath(id)
	if err != nil {
		return err
	}
	contents, err := RenderCredentials(username, password)
	if err != nil {
		return err
	}
	return store.writeArtifact(path, contents, 0o600)
}

func (store ArtifactStore) VerifyOwnedArtifacts(definition Definition) error {
	if validateArtifactDefinition(definition, store) != nil {
		return errArtifactNotManaged
	}
	manifestPath, _ := store.ManifestPath(definition.ID)
	manifest, _ := marshalManifest(definition)
	if err := store.verifyFile(manifestPath, manifest, 0o600); err != nil {
		return err
	}
	mountPath, _ := store.MountUnitPath(definition)
	mount, err := RenderMountUnit(definition)
	if err != nil || store.verifyFile(mountPath, mount, 0o644) != nil {
		return errArtifactNotManaged
	}
	automountPath, _ := store.AutomountUnitPath(definition)
	automount, err := RenderAutomountUnit(definition)
	if err != nil || store.verifyFile(automountPath, automount, 0o644) != nil {
		return errArtifactNotManaged
	}
	return nil
}

func mountSettings(definition Definition) (string, string, []string, error) {
	options := []string{"nodev", "nosuid"}
	if definition.ReadOnly {
		options = append(options, "ro")
	} else {
		options = append(options, "rw")
	}
	switch definition.Protocol {
	case "nfs":
		if ValidateNFSHost(definition.Host) != nil || ValidateNFSExport(definition.Export) != nil || ValidateNFSVersion(definition.ProtocolVersion) != nil {
			return "", "", nil, errInvalidManifest
		}
		sort.Strings(options)
		return definition.Host + ":" + definition.Export, "nfs", options, nil
	case "smb":
		if ValidateSMBServer(definition.Server) != nil || ValidateSMBShare(definition.Share) != nil || ValidateSMBVersion(definition.ProtocolVersion) != nil {
			return "", "", nil, errInvalidManifest
		}
		if definition.Username != "" {
			if ValidateUsername(definition.Username) != nil || definition.Credential == "" {
				return "", "", nil, errInvalidManifest
			}
			options = append(options, "credentials="+escapeSystemdValue(definition.Credential))
		}
		if definition.ProtocolVersion != "auto" {
			options = append(options, "vers="+definition.ProtocolVersion)
		}
		sort.Strings(options)
		return "//" + definition.Server + "/" + definition.Share, "cifs", options, nil
	default:
		return "", "", nil, errInvalidManifest
	}
}

func escapeSystemdValue(value string) string {
	var escaped bytes.Buffer
	for _, byteValue := range []byte(value) {
		switch byteValue {
		case '%':
			escaped.WriteString("%%")
		case '\\', ' ', '\t', '\n', '\r':
			escaped.WriteString("\\x")
			escaped.WriteString("0123456789abcdef"[byteValue>>4 : byteValue>>4+1])
			escaped.WriteString("0123456789abcdef"[byteValue&0x0f : byteValue&0x0f+1])
		default:
			escaped.WriteByte(byteValue)
		}
	}
	return escaped.String()
}
