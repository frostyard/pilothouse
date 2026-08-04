package incus

import (
	"context"
	"errors"
	"slices"

	"github.com/lxc/incus/v7/shared/api"
)

// snapshotNameLimit matches the length bound Incus applies to instance
// names; snapshots are addressed as "instance/snapshot", so keeping the
// same bound leaves the composed name well within any path limit.
const snapshotNameLimit = 63

// validSnapshotName constrains the names Pilothouse will *create*. It is
// deliberately stricter than Incus itself: a snapshot Pilothouse creates
// should be addressable without quoting anywhere it later appears (a URL
// path segment, an audit resource string), and the "/" delimiter Incus uses
// to separate instance from snapshot must never appear inside one.
//
// Restore and delete do not use this: they resolve the name against the
// instance's live snapshot list instead, so a snapshot created outside
// Pilothouse under a laxer name stays manageable.
func validSnapshotName(name string) bool {
	return validResourceName(name)
}

// validResourceName is the shared name gate for identifiers that reach the
// Incus API in a URL path segment — snapshots, networks, profiles. It
// admits the characters real Incus names use and nothing that could change
// the shape of a path: no "/", no spaces, no leading or trailing "-"/".".
func validResourceName(name string) bool {
	if len(name) == 0 || len(name) > snapshotNameLimit {
		return false
	}
	if name[0] == '-' || name[0] == '.' || name[len(name)-1] == '-' || name[len(name)-1] == '.' {
		return false
	}
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-' || character == '.' || character == '_':
		default:
			return false
		}
	}
	return true
}

// CreateSnapshot takes a non-stateful snapshot of one instance. The name is
// validated against validSnapshotName and rejected if the instance already
// carries it, so a create can never silently overwrite.
func (m *SystemManager) CreateSnapshot(ctx context.Context, requestedProject, instance, name string) error {
	if !validSnapshotName(name) {
		return errors.New("invalid snapshot name")
	}
	item, project, err := m.fetch(ctx, requestedProject, instance)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(item.Snapshots, func(existing api.InstanceSnapshot) bool {
		return snapshotName(existing.Name) == name
	}) {
		return errors.New("a snapshot with that name already exists")
	}
	return m.client.CreateSnapshot(ctx, project, instance, name)
}

// DeleteSnapshot removes one existing snapshot.
func (m *SystemManager) DeleteSnapshot(ctx context.Context, requestedProject, instance, name string) error {
	project, err := m.resolveSnapshot(ctx, requestedProject, instance, name)
	if err != nil {
		return err
	}
	return m.client.DeleteSnapshot(ctx, project, instance, name)
}

// RestoreSnapshot rolls an instance back to one of its snapshots. Incus
// refuses to restore a running instance from a non-stateful snapshot, and
// Pilothouse only ever creates non-stateful ones, so the running case is
// rejected here with an actionable message rather than passed through to
// surface as an opaque API error.
func (m *SystemManager) RestoreSnapshot(ctx context.Context, requestedProject, instance, name string) error {
	item, project, err := m.fetch(ctx, requestedProject, instance)
	if err != nil {
		return err
	}
	if item.StatusCode == api.Running {
		return errors.New("stop the instance before restoring a snapshot")
	}
	if !hasSnapshot(item, name) {
		return errors.New("snapshot no longer exists")
	}
	return m.client.RestoreSnapshot(ctx, project, instance, name)
}

// resolveSnapshot validates the instance and project, then rediscovers the
// snapshot on the live instance before any mutation, so a crafted name
// cannot reach the Incus API.
func (m *SystemManager) resolveSnapshot(ctx context.Context, requestedProject, instance, name string) (string, error) {
	item, project, err := m.fetch(ctx, requestedProject, instance)
	if err != nil {
		return "", err
	}
	if !hasSnapshot(item, name) {
		return "", errors.New("snapshot no longer exists")
	}
	return project, nil
}

func hasSnapshot(item *api.InstanceFull, name string) bool {
	if name == "" {
		return false
	}
	return slices.ContainsFunc(item.Snapshots, func(existing api.InstanceSnapshot) bool {
		return snapshotName(existing.Name) == name
	})
}
