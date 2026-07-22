//go:build linux

package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-systemd/v22/unit"
	"golang.org/x/sys/unix"
)

var errUnsafeTarget = errors.New("unsafe target path")
var errInvalidTargetInventory = errors.New("invalid target inventory")

var protectedTargetRoots = []string{
	"/proc", "/sys", "/dev", "/run", "/boot", "/etc", "/usr", "/var/lib/pilothouse",
}

// TargetInventory contains only data collected through trusted, fixed queries.
type TargetInventory struct {
	Mounts     []string
	UnitOwners map[string]string
}

// PathManager validates and changes mount target directories without resolving
// a path separately from the operation that uses it.
type PathManager struct {
	DefinitionID string
	fs           pathFS
}

type pathFS interface {
	close(int) error
	empty(int) (bool, error)
	fstat(int, *unix.Stat_t) error
	mkdirat(int, string, uint32) error
	openat2(int, string, *unix.OpenHow) (int, error)
	openRoot() (int, error)
	unlinkat(int, string, int) error
}

type linuxPathFS struct{}

func NewPathManager(definitionID string) (PathManager, error) {
	if err := ValidateDefinitionID(definitionID); err != nil {
		return PathManager{}, err
	}
	return PathManager{DefinitionID: definitionID, fs: linuxPathFS{}}, nil
}

func (linuxPathFS) close(fd int) error { return unix.Close(fd) }

func (linuxPathFS) empty(fd int) (bool, error) {
	buffer := make([]byte, 4096)
	read, err := unix.Getdents(fd, buffer)
	if err != nil {
		return false, err
	}
	_, count, _ := unix.ParseDirent(buffer[:read], 1, nil)
	return count == 0, nil
}

func (linuxPathFS) fstat(fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) }
func (linuxPathFS) mkdirat(fd int, name string, mode uint32) error {
	return unix.Mkdirat(fd, name, mode)
}
func (linuxPathFS) openat2(fd int, name string, how *unix.OpenHow) (int, error) {
	return unix.Openat2(fd, name, how)
}
func (linuxPathFS) openRoot() (int, error) {
	return unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}
func (linuxPathFS) unlinkat(fd int, name string, flags int) error {
	return unix.Unlinkat(fd, name, flags)
}

func (manager PathManager) ValidateTarget(ctx context.Context, target string, inventory *TargetInventory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := manager.validatePolicy(target, inventory); err != nil {
		return err
	}

	parent, leaf, exists, err := manager.walk(target)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer func() { _ = manager.fs.close(parent) }()
	if !exists {
		return nil
	}
	return manager.validateDirectory(parent, leaf)
}

func (manager PathManager) validatePolicy(target string, inventory *TargetInventory) error {
	if ValidateTarget(target) != nil || protectedTarget(target) {
		return errUnsafeTarget
	}
	if err := validateTargetInventory(inventory); err != nil {
		return err
	}
	if manager.hasConflict(target, inventory) {
		return errUnsafeTarget
	}
	return nil
}

func (manager PathManager) CreateTarget(ctx context.Context, target string, inventory *TargetInventory) (bool, error) {
	// Recheck trusted policy and filesystem state immediately before mkdirat.
	if err := manager.ValidateTarget(ctx, target, inventory); err != nil {
		return false, err
	}

	parent, leaf, exists, err := manager.walk(target)
	if err != nil {
		return false, fmt.Errorf("open target parent: %w", err)
	}
	defer func() { _ = manager.fs.close(parent) }()
	if exists {
		return false, manager.validateDirectory(parent, leaf)
	}
	if err := manager.fs.mkdirat(parent, leaf, 0o755); err != nil {
		return false, fmt.Errorf("create target: %w", err)
	}
	if err := manager.validateDirectory(parent, leaf); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveTarget removes a target only when created came from the root-owned manifest.
func (manager PathManager) RemoveTarget(ctx context.Context, target string, created bool, inventory *TargetInventory) error {
	if !created {
		return nil
	}
	// Recheck trusted policy and filesystem state immediately before unlinkat.
	if err := manager.ValidateTarget(ctx, target, inventory); err != nil {
		return err
	}

	parent, leaf, exists, err := manager.walk(target)
	if err != nil {
		return fmt.Errorf("open target parent: %w", err)
	}
	defer func() { _ = manager.fs.close(parent) }()
	if !exists {
		return nil
	}
	if err := manager.validateDirectory(parent, leaf); err != nil {
		return err
	}
	if err := manager.fs.unlinkat(parent, leaf, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove target: %w", err)
	}
	return nil
}

func (manager PathManager) walk(target string) (parent int, leaf string, exists bool, err error) {
	parent, err = manager.fs.openRoot()
	if err != nil {
		return -1, "", false, err
	}

	components := strings.Split(strings.TrimPrefix(target, "/"), "/")
	for index, component := range components {
		if index == len(components)-1 {
			fd, err := manager.open(parent, component, unix.O_PATH)
			if errors.Is(err, unix.ENOENT) {
				return parent, component, false, nil
			}
			if err != nil {
				_ = manager.fs.close(parent)
				return -1, "", false, err
			}
			_ = manager.fs.close(fd)
			return parent, component, true, nil
		}

		next, err := manager.open(parent, component, unix.O_PATH|unix.O_DIRECTORY)
		if err != nil {
			_ = manager.fs.close(parent)
			return -1, "", false, err
		}
		_ = manager.fs.close(parent)
		parent = next
	}
	_ = manager.fs.close(parent)
	return -1, "", false, errUnsafeTarget
}

func (manager PathManager) validateDirectory(parent int, leaf string) error {
	fd, err := manager.open(parent, leaf, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer func() { _ = manager.fs.close(fd) }()

	var stat unix.Stat_t
	if err := manager.fs.fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat target: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errUnsafeTarget
	}
	empty, err := manager.fs.empty(fd)
	if err != nil {
		return fmt.Errorf("read target: %w", err)
	}
	if !empty {
		return errUnsafeTarget
	}
	return nil
}

func (manager PathManager) open(parent int, name string, flags uint64) (int, error) {
	return manager.fs.openat2(parent, name, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

func (manager PathManager) hasConflict(target string, inventory *TargetInventory) bool {
	if inventory == nil {
		return false
	}
	for _, mount := range inventory.Mounts {
		if mount == target || strings.HasPrefix(mount, target+"/") || strings.HasPrefix(target, mount+"/") {
			return true
		}
	}
	for _, name := range []string{mountUnitName(target), automountUnitName(target)} {
		if owner, exists := inventory.UnitOwners[name]; exists && owner != manager.DefinitionID {
			return true
		}
	}
	return false
}

func validateTargetInventory(inventory *TargetInventory) error {
	if inventory == nil {
		return nil
	}
	for _, mount := range inventory.Mounts {
		if ValidateTarget(mount) != nil {
			return errInvalidTargetInventory
		}
	}
	for name, owner := range inventory.UnitOwners {
		if !validUnitName(name) || ValidateDefinitionID(owner) != nil {
			return errInvalidTargetInventory
		}
	}
	return nil
}

func validUnitName(name string) bool {
	if len(name) == 0 || len(name) > 255 || (!strings.HasSuffix(name, ".mount") && !strings.HasSuffix(name, ".automount")) {
		return false
	}
	if strings.TrimSuffix(strings.TrimSuffix(name, ".automount"), ".mount") == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		byteValue := name[index]
		if (byteValue >= 'a' && byteValue <= 'z') || (byteValue >= 'A' && byteValue <= 'Z') || (byteValue >= '0' && byteValue <= '9') || byteValue == ':' || byteValue == '_' || byteValue == '.' || byteValue == '-' {
			continue
		}
		if byteValue != '\\' || index+3 >= len(name) || name[index+1] != 'x' || !hexByte(name[index+2]) || !hexByte(name[index+3]) {
			return false
		}
		index += 3
	}
	return true
}

func hexByte(value byte) bool {
	return ('0' <= value && value <= '9') || ('a' <= value && value <= 'f')
}

func protectedTarget(target string) bool {
	if target == "/" {
		return true
	}
	for _, root := range protectedTargetRoots {
		if target == root || strings.HasPrefix(target, root+"/") {
			return true
		}
	}
	return false
}

func mountUnitName(target string) string     { return systemdEscapePath(target) + ".mount" }
func automountUnitName(target string) string { return systemdEscapePath(target) + ".automount" }

func systemdEscapePath(target string) string { return unit.UnitNamePathEscape(target) }
