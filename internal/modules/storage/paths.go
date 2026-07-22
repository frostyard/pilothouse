//go:build linux

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

var errUnsafeTarget = errors.New("unsafe target path")

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
	empty(int, string) (bool, error)
	fstat(int, *unix.Stat_t) error
	mkdirat(int, string, uint32) error
	openat2(int, string, *unix.OpenHow) (int, error)
	openRoot() (int, error)
	unlinkat(int, string, int) error
}

type linuxPathFS struct{}

func NewPathManager() PathManager {
	return PathManager{DefinitionID: "new", fs: linuxPathFS{}}
}

func (linuxPathFS) close(fd int) error { return unix.Close(fd) }

func (linuxPathFS) empty(parent int, name string) (bool, error) {
	fd, err := openTarget(parent, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return false, err
	}

	directory := os.NewFile(uintptr(fd), name)
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(1)
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
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
	if ValidateTarget(target) != nil || protectedTarget(target) || manager.hasConflict(target, inventory) {
		return errUnsafeTarget
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

func (manager PathManager) CreateTarget(ctx context.Context, target string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if ValidateTarget(target) != nil || protectedTarget(target) {
		return false, errUnsafeTarget
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

func (manager PathManager) RemoveTarget(ctx context.Context, target string, created bool) error {
	if !created {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ValidateTarget(target) != nil || protectedTarget(target) {
		return errUnsafeTarget
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
	fd, err := manager.open(parent, leaf, unix.O_PATH|unix.O_DIRECTORY)
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
	empty, err := manager.fs.empty(parent, leaf)
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
		if mount == target || strings.HasPrefix(mount, target+"/") {
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

func systemdEscapePath(target string) string {
	target = strings.TrimPrefix(target, "/")
	if target == "" {
		return "-"
	}
	var escaped strings.Builder
	for index, byteValue := range []byte(target) {
		if byteValue == '/' {
			escaped.WriteByte('-')
			continue
		}
		if (byteValue >= 'a' && byteValue <= 'z') || (byteValue >= 'A' && byteValue <= 'Z') || (byteValue >= '0' && byteValue <= '9') || byteValue == ':' || byteValue == '_' || byteValue == '.' {
			if index != 0 || byteValue != '.' {
				escaped.WriteByte(byteValue)
				continue
			}
		}
		fmt.Fprintf(&escaped, "\\x%02x", byteValue)
	}
	return escaped.String()
}

func openTarget(parent int, name string, flags int) (int, error) {
	return unix.Openat2(parent, name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}
