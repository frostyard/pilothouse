package files

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	maxRootPathBytes = 4 << 10
	maxJSONBytes     = 2 << 20
)

var rootIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type RootFlags struct {
	specs map[string]RootSpec
}

func (r *RootFlags) Add(value string, writable bool) error {
	id, path, ok := strings.Cut(value, "=")
	if !ok || !rootIDPattern.MatchString(id) {
		return errors.New("invalid root id")
	}
	if len(r.specs) >= MaxRoots {
		return errors.New("maximum number of roots exceeded")
	}
	if _, exists := r.specs[id]; exists {
		return fmt.Errorf("duplicate root id %q", id)
	}

	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || len(path) > maxRootPathBytes {
		return errors.New("invalid root path")
	}
	if path == string(filepath.Separator) {
		return errors.New("filesystem root cannot be a files root")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat root path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("root path is not a directory")
	}
	if r.specs == nil {
		r.specs = make(map[string]RootSpec)
	}
	r.specs[id] = RootSpec{ID: id, Path: path, Writable: writable}
	return nil
}

func (r *RootFlags) Flag(writable bool) flag.Value {
	return rootFlag{roots: r, writable: writable}
}

func (r *RootFlags) Specs() []RootSpec {
	specs := make([]RootSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		specs = append(specs, spec)
	}
	slices.SortFunc(specs, func(a, b RootSpec) int {
		return strings.Compare(a.ID, b.ID)
	})
	return specs
}

type rootFlag struct {
	roots    *RootFlags
	writable bool
}

func (f rootFlag) String() string {
	return ""
}

func (f rootFlag) Set(value string) error {
	return f.roots.Add(value, f.writable)
}

type rootDescriptor struct {
	Root
	fd int
}

type SystemManager struct {
	roots        map[string]rootDescriptor
	maxTransfer  int64
	maxEntries   int
	maxScanned   int
	maxJSONBytes int
	closeOnce    sync.Once
	closeErr     error
}

func NewSystemManager(specs []RootSpec) (*SystemManager, error) {
	if len(specs) > MaxRoots {
		return nil, errors.New("maximum number of roots exceeded")
	}
	ids := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if !rootIDPattern.MatchString(spec.ID) {
			return nil, errors.New("invalid root id")
		}
		if _, exists := ids[spec.ID]; exists {
			return nil, fmt.Errorf("duplicate root id %q", spec.ID)
		}
		ids[spec.ID] = struct{}{}
	}

	manager := &SystemManager{
		roots:        make(map[string]rootDescriptor, len(specs)),
		maxTransfer:  MaxTransferBytes,
		maxEntries:   MaxEntries,
		maxScanned:   MaxScannedEntries,
		maxJSONBytes: maxJSONBytes,
	}
	for _, spec := range specs {
		fd, err := unix.Open(spec.Path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			_ = manager.Close()
			return nil, fmt.Errorf("open root %q: %w", spec.ID, err)
		}
		manager.roots[spec.ID] = rootDescriptor{Root: Root(spec), fd: fd}
	}
	return manager, nil
}

func (m *SystemManager) Close() error {
	m.closeOnce.Do(func() {
		for _, root := range m.roots {
			if err := unix.Close(root.fd); err != nil {
				m.closeErr = errors.Join(m.closeErr, err)
			}
		}
	})
	return m.closeErr
}
