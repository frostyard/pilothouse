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
)

// UnitController exposes only the systemd operations remote definitions need.
type UnitController interface {
	DaemonReload(context.Context) error
	Disable(context.Context, string) error
	Enable(context.Context, string) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
}

// ArtifactWriter isolates only the mutations performed by Create. ArtifactStore
// remains the source of paths, reads, and ownership verification.
type ArtifactWriter interface {
	WriteCredentials(string, string, string) error
	WriteMountUnit(Definition) error
	WriteAutomountUnit(Definition) error
	WriteManifest(Definition) error
}

// SystemRemoteManager adds managed remote mount lifecycle operations to a
// read-only storage manager.
type SystemRemoteManager struct {
	artifacts ArtifactStore
	writer    ArtifactWriter
	manager   Manager
	units     UnitController
	mu        sync.Mutex
}

func NewSystemRemoteManager(manager Manager, artifacts ArtifactStore, units UnitController) *SystemRemoteManager {
	return NewSystemRemoteManagerWithWriter(manager, artifacts, artifacts, units)
}

func NewSystemRemoteManagerWithWriter(manager Manager, artifacts ArtifactStore, writer ArtifactWriter, units UnitController) *SystemRemoteManager {
	return &SystemRemoteManager{artifacts: artifacts, writer: writer, manager: manager, units: units}
}

func (manager *SystemRemoteManager) Create(ctx context.Context, request CreateRequest) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	definition, err := manager.definition(request)
	if err != nil {
		return err
	}
	inventory, err := manager.inventory(ctx)
	if err != nil {
		return err
	}
	for _, owner := range inventory.UnitOwners {
		if owner == definition.ID {
			return errArtifactNotManaged
		}
	}
	paths, err := NewPathManager(definition.ID)
	if err != nil {
		return err
	}
	if err := paths.ValidateTarget(ctx, definition.Target, &inventory); err != nil {
		return err
	}
	// Render before any mutation so malformed definitions never leave artifacts.
	if _, err := RenderMountUnit(definition); err != nil {
		return err
	}
	if _, err := RenderAutomountUnit(definition); err != nil {
		return err
	}
	if request.Password != "" {
		if _, err := RenderCredentials(request.Username, request.Password); err != nil {
			return err
		}
	}

	type undo struct {
		operation string
		run       func() error
	}
	undos := make([]undo, 0, 6)
	unitArtifactsWritten := false
	rollback := func(cause error) error {
		failures := make([]string, 0, len(undos))
		for index := len(undos) - 1; index >= 0; index-- {
			if err := undos[index].run(); err != nil {
				failures = append(failures, undos[index].operation)
			}
		}
		if unitArtifactsWritten && manager.units.DaemonReload(ctx) != nil {
			failures = append(failures, "reload systemd")
		}
		if len(failures) > 0 {
			definition.State = "needs-attention"
			if manager.artifacts.UpdateManifest(definition) != nil {
				_ = manager.artifacts.WriteManifest(definition)
			}
			return fmt.Errorf("remote mount needs attention: cleanup failed: %s", strings.Join(failures, ", "))
		}
		return cause
	}
	created, err := paths.CreateTarget(ctx, definition.Target, &inventory)
	if err != nil {
		return err
	}
	definition.CreatedTarget = created
	if created {
		undos = append(undos, undo{operation: "remove target", run: func() error { return paths.RemoveTarget(ctx, definition.Target, true, &inventory) }})
	}
	if request.Password != "" {
		if err := manager.writer.WriteCredentials(definition.ID, request.Username, request.Password); err != nil {
			return rollback(err)
		}
		credential, _ := manager.artifacts.CredentialPath(definition.ID)
		undos = append(undos, undo{operation: "remove credentials", run: func() error { return os.Remove(credential) }})
	}
	if err := manager.writer.WriteMountUnit(definition); err != nil {
		return rollback(err)
	}
	mountPath, _ := manager.artifacts.MountUnitPath(definition)
	undos = append(undos, undo{operation: "remove mount unit", run: func() error { return os.Remove(mountPath) }})
	unitArtifactsWritten = true
	if err := manager.writer.WriteAutomountUnit(definition); err != nil {
		return rollback(err)
	}
	automountPath, _ := manager.artifacts.AutomountUnitPath(definition)
	undos = append(undos, undo{operation: "remove automount unit", run: func() error { return os.Remove(automountPath) }})
	if err := manager.writer.WriteManifest(definition); err != nil {
		return rollback(err)
	}
	manifestPath, _ := manager.artifacts.ManifestPath(definition.ID)
	undos = append(undos, undo{operation: "remove manifest", run: func() error { return os.Remove(manifestPath) }})
	if err := manager.units.DaemonReload(ctx); err != nil {
		return rollback(err)
	}
	undos = append(undos, undo{operation: "reload systemd", run: func() error { return manager.units.DaemonReload(ctx) }})
	if err := manager.units.Enable(ctx, automountUnitName(definition.Target)); err != nil {
		return rollback(err)
	}
	undos = append(undos, undo{operation: "disable automount", run: func() error { return manager.units.Disable(ctx, automountUnitName(definition.Target)) }})
	if err := manager.units.Start(ctx, automountUnitName(definition.Target)); err != nil {
		return rollback(err)
	}
	return nil
}

func (manager *SystemRemoteManager) Mount(ctx context.Context, id string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	definition, err := manager.loadVerified(id)
	if err != nil {
		return err
	}
	return manager.units.Start(ctx, automountUnitName(definition.Target))
}

func (manager *SystemRemoteManager) Unmount(ctx context.Context, id string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	definition, err := manager.loadVerified(id)
	if err != nil {
		return err
	}
	return manager.units.Stop(ctx, definition.UnitName)
}

func (manager *SystemRemoteManager) Delete(ctx context.Context, id string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	definition, err := manager.loadVerified(id)
	if err != nil {
		return err
	}
	if err := manager.units.Stop(ctx, definition.UnitName); err != nil {
		return manager.retainAttention(definition, err)
	}
	if err := manager.units.Stop(ctx, automountUnitName(definition.Target)); err != nil {
		return manager.retainAttention(definition, err)
	}
	if err := manager.units.Disable(ctx, automountUnitName(definition.Target)); err != nil {
		return manager.retainAttention(definition, err)
	}
	mountPath, _ := manager.artifacts.MountUnitPath(definition)
	automountPath, _ := manager.artifacts.AutomountUnitPath(definition)
	mount, err := RenderMountUnit(definition)
	if err != nil {
		return manager.retainAttention(definition, err)
	}
	if err := manager.removeVerifiedArtifact(mountPath, mount, 0o644); err != nil {
		return manager.retainAttention(definition, err)
	}
	automount, err := RenderAutomountUnit(definition)
	if err != nil {
		return manager.retainAttention(definition, err)
	}
	if err := manager.removeVerifiedArtifact(automountPath, automount, 0o644); err != nil {
		return manager.retainAttention(definition, err)
	}
	if definition.Username != "" {
		credential, _ := manager.artifacts.CredentialPath(definition.ID)
		if err := manager.removeVerifiedArtifact(credential, nil, 0o600); err != nil {
			return manager.retainAttention(definition, err)
		}
	}
	if err := manager.units.DaemonReload(ctx); err != nil {
		return manager.retainAttention(definition, err)
	}
	inventory, err := manager.inventory(ctx, id)
	if err != nil {
		return manager.retainAttention(definition, err)
	}
	paths, err := NewPathManager(id)
	if err != nil {
		return manager.retainAttention(definition, err)
	}
	if err := paths.RemoveTarget(ctx, definition.Target, definition.CreatedTarget, &inventory); err != nil {
		return manager.retainAttention(definition, err)
	}
	manifest, _ := manager.artifacts.ManifestPath(id)
	return os.Remove(manifest)
}

// removeVerifiedArtifact accepts an absent canonical artifact during recovery,
// but never removes an existing file until its managed contents or metadata are
// verified.
func (manager *SystemRemoteManager) removeVerifiedArtifact(path string, contents []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errArtifactNotManaged
	}
	if !info.Mode().IsRegular() {
		return errArtifactNotManaged
	}
	if contents == nil {
		if err := manager.artifacts.verifyMetadata(path, mode); err != nil {
			return err
		}
	} else if err := manager.artifacts.verifyFile(path, contents, mode); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (manager *SystemRemoteManager) State(ctx context.Context) (Snapshot, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	snapshot, err := manager.manager.State(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	definitions, err := manager.definitions()
	if err != nil {
		return Snapshot{}, err
	}
	for _, definition := range definitions {
		mount := Mount{ID: "remote:" + definition.ID, Health: HealthHealthy, Managed: true, ReadOnly: definition.ReadOnly, State: definition.State, Target: definition.Target}
		switch definition.Protocol {
		case "nfs":
			mount.Filesystem, mount.Source = "nfs", definition.Host+":"+definition.Export
		case "smb":
			mount.Filesystem, mount.Source = "cifs", "//"+definition.Server+"/"+definition.Share
		}
		if definition.State == "needs-attention" {
			mount.Health = HealthWarning
			snapshot.Findings = append(snapshot.Findings, Finding{ResourceID: mount.ID, Severity: HealthWarning, Title: "Managed remote mount needs attention", Detail: "Review the managed remote mount lifecycle."})
		}
		merged := false
		for index := range snapshot.Mounts {
			if snapshot.Mounts[index].Target == mount.Target && snapshot.Mounts[index].Source == mount.Source {
				snapshot.Mounts[index].Managed = true
				snapshot.Mounts[index].ID = mount.ID
				snapshot.Mounts[index].ReadOnly = mount.ReadOnly
				if definition.State != "active" {
					snapshot.Mounts[index].State = definition.State
				}
				merged = true
				break
			}
		}
		if !merged {
			snapshot.Mounts = append(snapshot.Mounts, mount)
		}
	}
	sortSnapshot(&snapshot)
	recalculateSummary(&snapshot)
	return snapshot, nil
}

func (manager *SystemRemoteManager) loadVerified(id string) (Definition, error) {
	if err := ValidateDefinitionID(id); err != nil {
		return Definition{}, err
	}
	definition, err := manager.artifacts.LoadDefinition(id)
	if err != nil {
		return Definition{}, err
	}
	if definition.State != "needs-attention" {
		if err := manager.artifacts.VerifyOwnedArtifacts(definition); err != nil {
			return Definition{}, err
		}
	}
	return definition, nil
}

func (manager *SystemRemoteManager) definition(request CreateRequest) (Definition, error) {
	if ValidateDefinitionID(request.ID) != nil || ValidateProtocol(request.Protocol) != nil || ValidateTarget(request.Target) != nil {
		return Definition{}, errInvalidManifest
	}
	definition := Definition{CreatedTarget: false, FormatVersion: ManifestFormatVersion, ID: request.ID, Protocol: request.Protocol, ProtocolVersion: request.Version, ReadOnly: request.ReadOnly, State: "active", Target: request.Target, UnitName: mountUnitName(request.Target)}
	switch request.Protocol {
	case "nfs":
		if ValidateNFSHost(request.Host) != nil || ValidateNFSExport(request.Export) != nil || ValidateNFSVersion(request.Version) != nil || request.Password != "" || request.Username != "" {
			return Definition{}, errInvalidManifest
		}
		definition.Host, definition.Export = request.Host, request.Export
	case "smb":
		if ValidateSMBServer(request.Server) != nil || ValidateSMBShare(request.Share) != nil || ValidateSMBVersion(request.Version) != nil || (request.Password == "") != (request.Username == "") || (request.Username != "" && ValidateUsername(request.Username) != nil) || (request.Password != "" && ValidatePassword(request.Password) != nil) {
			return Definition{}, errInvalidManifest
		}
		definition.Server, definition.Share, definition.Username = request.Server, request.Share, request.Username
		if request.Username != "" {
			definition.Credential, _ = manager.artifacts.CredentialPath(request.ID)
		}
	default:
		return Definition{}, errInvalidManifest
	}
	return definition, nil
}

func (manager *SystemRemoteManager) inventory(ctx context.Context, exclude ...string) (TargetInventory, error) {
	snapshot, err := manager.manager.State(ctx)
	if err != nil {
		return TargetInventory{}, err
	}
	inventory := TargetInventory{UnitOwners: map[string]string{}}
	for _, mount := range snapshot.Mounts {
		inventory.Mounts = append(inventory.Mounts, mount.Target)
	}
	definitions, err := manager.definitions()
	if err != nil {
		return TargetInventory{}, err
	}
	for _, definition := range definitions {
		if len(exclude) > 0 && definition.ID == exclude[0] {
			continue
		}
		if definition.State != "needs-attention" {
			if err := manager.artifacts.VerifyOwnedArtifacts(definition); err != nil {
				return TargetInventory{}, err
			}
		}
		inventory.UnitOwners[definition.UnitName] = definition.ID
		inventory.UnitOwners[automountUnitName(definition.Target)] = definition.ID
	}
	return inventory, nil
}

func (manager *SystemRemoteManager) retainAttention(definition Definition, cause error) error {
	definition.State = "needs-attention"
	if manager.artifacts.UpdateManifest(definition) != nil {
		_ = manager.artifacts.WriteManifest(definition)
	}
	return fmt.Errorf("remote mount needs attention: %w", cause)
}

func (manager *SystemRemoteManager) definitions() ([]Definition, error) {
	entries, err := os.ReadDir(manager.artifacts.ManifestRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	definitions := make([]Definition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".json")]
		definition, err := manager.artifacts.LoadDefinition(id)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}
