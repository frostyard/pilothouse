//go:build linux

package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const directoryBatchSize = 128

func ParseListParameters(parameters map[string]string) (ListRequest, error) {
	const expected = 6
	if len(parameters) != expected {
		return ListRequest{}, fmt.Errorf("%w: unexpected list parameters", ErrInvalid)
	}
	for _, key := range []string{"root", "path", "filter", "sort", "direction", "hidden"} {
		if _, ok := parameters[key]; !ok {
			return ListRequest{}, fmt.Errorf("%w: missing list parameter %q", ErrInvalid, key)
		}
	}

	request := ListRequest{
		Root:      parameters["root"],
		Path:      parameters["path"],
		Filter:    strings.TrimSpace(parameters["filter"]),
		Sort:      parameters["sort"],
		Direction: parameters["direction"],
	}
	if request.Sort == "" {
		request.Sort = "name"
	}
	if request.Direction == "" {
		request.Direction = "asc"
	}
	switch hidden := parameters["hidden"]; hidden {
	case "", "false":
		request.Hidden = false
	case "true":
		request.Hidden = true
	default:
		return ListRequest{}, fmt.Errorf("%w: invalid hidden value", ErrInvalid)
	}
	if request.Root == "" && request.Path != "" {
		return ListRequest{}, fmt.Errorf("%w: root summary has a path", ErrInvalid)
	}
	if err := validateListRequest(request); err != nil {
		return ListRequest{}, err
	}
	return request, nil
}

func validateListRequest(request ListRequest) error {
	if err := validateRelativePath(request.Path); err != nil {
		return err
	}
	if len(request.Filter) > 1024 || utf8.RuneCountInString(request.Filter) > 200 {
		return fmt.Errorf("%w: filter too long", ErrInvalid)
	}
	if !slices.Contains([]string{"name", "size", "modified", "owner", "permissions"}, request.Sort) {
		return fmt.Errorf("%w: invalid sort", ErrInvalid)
	}
	if request.Direction != "asc" && request.Direction != "desc" {
		return fmt.Errorf("%w: invalid direction", ErrInvalid)
	}
	return nil
}

func validateRelativePath(path string) error {
	if len(path) > MaxPathBytes || strings.HasPrefix(path, "/") {
		return fmt.Errorf("%w: invalid relative path", ErrInvalid)
	}
	if path == "" {
		return nil
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) > unix.NAME_MAX {
			return fmt.Errorf("%w: invalid relative path", ErrInvalid)
		}
		for _, r := range segment {
			if r == 0 || r < 0x20 || r == 0x7f {
				return fmt.Errorf("%w: invalid relative path", ErrInvalid)
			}
		}
	}
	return nil
}

func (m *SystemManager) List(ctx context.Context, request ListRequest) (State, error) {
	if request.Sort == "" {
		request.Sort = "name"
	}
	if request.Direction == "" {
		request.Direction = "asc"
	}
	if err := validateListRequest(request); err != nil {
		return State{}, err
	}
	roots := m.listRoots()
	if request.Root == "" {
		if request.Path != "" {
			return State{}, fmt.Errorf("%w: root summary has a path", ErrInvalid)
		}
		return State{Roots: roots, Filters: request}, nil
	}
	root, ok := m.roots[request.Root]
	if !ok {
		return State{}, ErrNotFound
	}
	fd, err := openBeneath(root.fd, request.Path, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = m.closeFD(fd) }()

	entries, truncated, err := m.readDirectory(ctx, fd, request)
	if err != nil {
		return State{}, err
	}
	return State{Roots: roots, Active: root.Root, Path: request.Path, Entries: entries, Truncated: truncated, Filters: request}, nil
}

func (m *SystemManager) listRoots() []Root {
	roots := make([]Root, 0, len(m.roots))
	for _, root := range m.roots {
		roots = append(roots, root.Root)
	}
	slices.SortFunc(roots, func(a, b Root) int { return strings.Compare(a.ID, b.ID) })
	return roots
}

func openBeneath(rootFD int, path string, flags int) (int, error) {
	if path == "" {
		fd, err := unix.FcntlInt(uintptr(rootFD), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return -1, fmt.Errorf("duplicate root descriptor: %w", err)
		}
		defer func() { _ = unix.Close(fd) }()
		rootFD = fd
		path = "."
	}
	fd, err := unix.Openat2(rootFD, path, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err == nil {
		return fd, nil
	}
	if errors.Is(err, unix.ENOSYS) {
		return -1, fmt.Errorf("%w: openat2 unsupported", ErrUnavailable)
	}
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
		return -1, ErrNotFound
	}
	return -1, fmt.Errorf("open beneath root: %w", err)
}

func (m *SystemManager) readDirectory(ctx context.Context, fd int, request ListRequest) ([]Entry, bool, error) {
	directoryFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, false, fmt.Errorf("duplicate directory descriptor: %w", err)
	}
	directory := os.NewFile(uintptr(directoryFD), "directory")
	defer func() { _ = directory.Close() }()
	var entries []Entry
	scanned := 0
	truncated := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		batch, err := directory.ReadDir(directoryBatchSize)
		for _, item := range batch {
			scanned++
			if scanned > m.maxScanned {
				truncated = true
				break
			}
			name := item.Name()
			if (!request.Hidden && strings.HasPrefix(name, ".")) || !strings.Contains(strings.ToLower(name), strings.ToLower(request.Filter)) {
				continue
			}
			entry, entryErr := entryAt(fd, name)
			if errors.Is(entryErr, unix.ENOENT) {
				continue
			}
			if entryErr != nil {
				return nil, false, fmt.Errorf("read directory entry: %w", entryErr)
			}
			entries = append(entries, entry)
		}
		if truncated || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("read directory: %w", err)
		}
	}
	sortEntries(entries, request.Sort, request.Direction)
	returned := make([]Entry, 0, min(len(entries), m.maxEntries))
	encoded := 0
	for _, entry := range entries {
		if len(returned) == m.maxEntries {
			truncated = true
			break
		}
		bytes, err := json.Marshal(entry)
		if err != nil {
			return nil, false, fmt.Errorf("encode directory entry: %w", err)
		}
		if encoded+len(bytes) > m.maxJSONBytes {
			truncated = true
			break
		}
		encoded += len(bytes)
		returned = append(returned, entry)
	}
	return returned, truncated, nil
}

func entryAt(fd int, name string) (Entry, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Entry{}, err
	}
	entry := Entry{
		Name:     name,
		Size:     stat.Size,
		Modified: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec),
		UID:      stat.Uid,
		GID:      stat.Gid,
		Owner:    lookupUser(stat.Uid),
		Group:    lookupGroup(stat.Gid),
		Mode:     stat.Mode,
		Type:     entryType(stat.Mode),
	}
	if entry.Type == EntrySymlink {
		buffer := make([]byte, 4096)
		n, err := unix.Readlinkat(fd, name, buffer)
		if err != nil {
			return Entry{}, err
		}
		entry.LinkTarget = string(buffer[:n])
	}
	return entry, nil
}

func entryType(mode uint32) EntryType {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return EntryRegular
	case unix.S_IFDIR:
		return EntryDirectory
	case unix.S_IFLNK:
		return EntrySymlink
	default:
		return EntryOther
	}
}

func lookupUser(uid uint32) string {
	value := strconv.FormatUint(uint64(uid), 10)
	account, err := user.LookupId(value)
	if err != nil || account.Username == "" {
		return value
	}
	return account.Username
}

func lookupGroup(gid uint32) string {
	value := strconv.FormatUint(uint64(gid), 10)
	group, err := user.LookupGroupId(value)
	if err != nil || group.Name == "" {
		return value
	}
	return group.Name
}

func sortEntries(entries []Entry, field, direction string) {
	slices.SortFunc(entries, func(a, b Entry) int {
		if (a.Type == EntryDirectory) != (b.Type == EntryDirectory) {
			if a.Type == EntryDirectory {
				return -1
			}
			return 1
		}
		comparison := compareEntries(a, b, field)
		if comparison != 0 {
			if direction == "desc" {
				return -comparison
			}
			return comparison
		}
		return strings.Compare(a.Name, b.Name)
	})
}

func compareEntries(a, b Entry, field string) int {
	switch field {
	case "size":
		return compareInt64(a.Size, b.Size)
	case "modified":
		return a.Modified.Compare(b.Modified)
	case "owner":
		return strings.Compare(a.Owner, b.Owner)
	case "permissions":
		return compareUint32(a.Mode&0o7777, b.Mode&0o7777)
	default:
		return strings.Compare(a.Name, b.Name)
	}
}

func compareInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareUint32(a, b uint32) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (m *SystemManager) Download(ctx context.Context, rootID, path string) (Download, error) {
	if err := validateRelativePath(path); err != nil || path == "" {
		return Download{}, fmt.Errorf("%w: invalid download path", ErrInvalid)
	}
	root, ok := m.roots[rootID]
	if !ok {
		return Download{}, ErrNotFound
	}
	fd, err := openBeneath(root.fd, path, unix.O_RDONLY|unix.O_NOFOLLOW)
	if err != nil {
		return Download{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	closeFile := true
	defer func() {
		if closeFile {
			_ = file.Close()
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return Download{}, fmt.Errorf("%w: stat download: %v", ErrUnavailable, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return Download{}, ErrNotFound
	}
	if stat.Size > m.maxTransfer {
		return Download{}, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return Download{}, err
	}
	closeFile = false
	return Download{Body: file, Name: path, Size: stat.Size}, nil
}

func (m *SystemManager) Upload(ctx context.Context, rootID, path, name string, reader io.Reader) error {
	if err := validateRelativePath(path); err != nil || !validBaseName(name) {
		return fmt.Errorf("%w: invalid upload path", ErrInvalid)
	}
	root, ok := m.roots[rootID]
	if !ok {
		return ErrNotFound
	}
	if !root.Writable {
		return ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dirFD, err := openBeneath(root.fd, path, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = m.closeFD(dirFD) }()
	fd, err := m.openTmpfile(dirFD)
	if err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w: anonymous temporary files unsupported", ErrUnavailable)
		}
		return fmt.Errorf("%w: create anonymous temporary file: %v", ErrUnavailable, err)
	}
	defer func() { _ = m.closeFD(fd) }()
	if err := m.copyUpload(ctx, fd, reader); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Fchown(fd, 0, 0); err != nil {
		return fmt.Errorf("%w: set upload ownership: %v", ErrUnavailable, err)
	}
	if err := unix.Fchmod(fd, 0o640); err != nil {
		return fmt.Errorf("%w: set upload permissions: %v", ErrUnavailable, err)
	}
	if err := m.syncFD(fd); err != nil {
		return fmt.Errorf("%w: sync upload: %v", ErrUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.linkat(fd, "", dirFD, name, unix.AT_EMPTY_PATH); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrConflict
		}
		return fmt.Errorf("%w: publish upload: %v", ErrUnavailable, err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("%w: sync upload directory: %v", ErrUnavailable, err)
	}
	return nil
}

func validBaseName(name string) bool {
	return name != "" && !strings.Contains(name, "/") && validateRelativePath(name) == nil
}

func (m *SystemManager) copyUpload(ctx context.Context, fd int, reader io.Reader) error {
	buffer := make([]byte, 32<<10)
	reader = io.LimitReader(reader, m.maxTransfer+1)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if written+int64(n) > m.maxTransfer {
				return ErrTooLarge
			}
			for data := buffer[:n]; len(data) > 0; {
				if err := ctx.Err(); err != nil {
					return err
				}
				count, err := m.writeFD(fd, data)
				if err != nil {
					return fmt.Errorf("%w: write upload: %v", ErrUnavailable, err)
				}
				if count <= 0 {
					return fmt.Errorf("%w: write upload: %v", ErrUnavailable, io.ErrShortWrite)
				}
				data = data[count:]
				written += int64(count)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				return readErr
			}
			return fmt.Errorf("%w: read upload: %v", ErrUnavailable, readErr)
		}
		if n == 0 {
			return fmt.Errorf("%w: read upload: %v", ErrUnavailable, io.ErrNoProgress)
		}
	}
}

var _ Manager = (*SystemManager)(nil)
