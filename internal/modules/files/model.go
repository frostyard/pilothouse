package files

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	MaxRoots                = 64
	MaxEntries              = 2_000
	MaxScannedEntries       = 10_000
	MaxPathBytes            = 768
	MaxTransferBytes  int64 = 256 << 20
)

var (
	ErrInvalid     = errors.New("invalid files request")
	ErrNotFound    = errors.New("files resource not found")
	ErrReadOnly    = errors.New("files root is read-only")
	ErrConflict    = errors.New("files conflict")
	ErrTooLarge    = errors.New("files transfer is too large")
	ErrUnavailable = errors.New("files service is unavailable")
)

type EntryType string

const (
	EntryRegular   EntryType = "regular"
	EntryDirectory EntryType = "directory"
	EntrySymlink   EntryType = "symlink"
	EntryOther     EntryType = "other"
)

type RootSpec struct {
	ID       string
	Path     string
	Writable bool
}

type Root struct {
	ID       string
	Path     string
	Writable bool
}

type Entry struct {
	Name       string
	Type       EntryType
	Size       int64
	Modified   time.Time
	UID, GID   uint32
	Owner      string
	Group      string
	Mode       uint32
	LinkTarget string
}

type ListRequest struct {
	Root, Path, Filter, Sort, Direction string
	Hidden                              bool
}

type State struct {
	Roots     []Root
	Active    Root
	Path      string
	Entries   []Entry
	Truncated bool
	Filters   ListRequest
}

type Download struct {
	Body io.ReadCloser
	Name string
	Size int64
}

type Manager interface {
	List(context.Context, ListRequest) (State, error)
	Download(context.Context, string, string) (Download, error)
	Upload(context.Context, string, string, string, io.Reader) error
	Close() error
}
