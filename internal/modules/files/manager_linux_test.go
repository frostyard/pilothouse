//go:build linux

package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestParseListParameters(t *testing.T) {
	request, err := ParseListParameters(map[string]string{
		"root": "safe", "path": "logs", "filter": "Error", "sort": "", "direction": "", "hidden": "",
	})
	require.NoError(t, err)
	assert.Equal(t, ListRequest{Root: "safe", Path: "logs", Filter: "Error", Sort: "name", Direction: "asc"}, request)

	for _, parameters := range []map[string]string{
		{"root": "safe"},
		{"root": "safe", "path": "", "filter": "", "sort": "name", "direction": "asc", "hidden": "false", "extra": "value"},
		{"root": "safe", "path": "", "filter": "", "sort": "invalid", "direction": "asc", "hidden": "false"},
		{"root": "safe", "path": "", "filter": "", "sort": "name", "direction": "up", "hidden": "false"},
		{"root": "safe", "path": "", "filter": "", "sort": "name", "direction": "asc", "hidden": "yes"},
		{"root": "safe", "path": "", "filter": strings.Repeat("x", 201), "sort": "name", "direction": "asc", "hidden": "false"},
		{"root": "safe", "path": "", "filter": strings.Repeat("x", 1025), "sort": "name", "direction": "asc", "hidden": "false"},
		{"root": "", "path": "logs", "filter": "", "sort": "name", "direction": "asc", "hidden": "false"},
	} {
		_, err := ParseListParameters(parameters)
		assert.Error(t, err)
	}
}

func TestValidateRelativePath(t *testing.T) {
	for _, path := range []string{"", "nested/file", "unicode/naïve"} {
		assert.NoError(t, validateRelativePath(path), path)
	}
	for _, path := range []string{"/absolute", "trailing/", "double//slash", ".", "..", "nested/../file", "nul\x00byte", "line\nbreak", strings.Repeat("x", 256), strings.Repeat("x", MaxPathBytes+1)} {
		assert.ErrorIs(t, validateRelativePath(path), ErrInvalid, path)
	}
}

func TestListShowsButNeverTraversesSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.Symlink("nested", filepath.Join(root, "inside")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})

	state, err := manager.List(context.Background(), ListRequest{Root: "safe", Hidden: true})
	require.NoError(t, err)
	assert.Equal(t, EntrySymlink, entryNamed(t, state, "escape").Type)
	assert.Equal(t, outside, entryNamed(t, state, "escape").LinkTarget)
	_, err = manager.List(context.Background(), ListRequest{Root: "safe", Path: "inside"})
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = manager.List(context.Background(), ListRequest{Root: "safe", Path: "escape"})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestListRootNestedFiltersAndMetadata(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "directory"), 0o750))
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "Alpha.log"), []byte("a"), 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(root, "naïve.txt"), []byte("text"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".hidden"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested", "child"), nil, 0o600))
	require.NoError(t, unix.Mkfifo(filepath.Join(root, "pipe"), 0o600))
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})

	state, err := manager.List(context.Background(), ListRequest{Root: "safe", Filter: "ALP", Sort: "name", Direction: "asc"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Alpha.log"}, entryNames(state))
	assert.Equal(t, uint32(0o640), entryNamed(t, state, "Alpha.log").Mode&0o777)
	assert.NotEmpty(t, entryNamed(t, state, "Alpha.log").Owner)
	assert.NotEmpty(t, entryNamed(t, state, "Alpha.log").Group)

	state, err = manager.List(context.Background(), ListRequest{Root: "safe", Hidden: true, Sort: "name", Direction: "asc"})
	require.NoError(t, err)
	assert.Equal(t, []string{"directory", "nested", ".hidden", "Alpha.log", "naïve.txt", "pipe"}, entryNames(state))
	assert.Equal(t, EntryOther, entryNamed(t, state, "pipe").Type)

	state, err = manager.List(context.Background(), ListRequest{Root: "safe", Path: "nested", Sort: "name", Direction: "asc"})
	require.NoError(t, err)
	assert.Equal(t, "nested", state.Path)
	assert.Equal(t, []string{"child"}, entryNames(state))
}

func TestListSortsDirectoriesFirstAndBreaksTiesByName(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z-dir", "a-dir"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o755))
	}
	for _, name := range []string{"z-file", "a-file"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("same"), 0o600))
	}
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})

	for _, sort := range []string{"name", "size", "modified", "owner", "permissions"} {
		for _, direction := range []string{"asc", "desc"} {
			state, err := manager.List(context.Background(), ListRequest{Root: "safe", Sort: sort, Direction: direction})
			require.NoError(t, err, "%s %s", sort, direction)
			expected := []string{"a-dir", "z-dir", "a-file", "z-file"}
			if sort == "name" && direction == "desc" {
				expected = []string{"z-dir", "a-dir", "z-file", "a-file"}
			}
			assert.Equal(t, expected, entryNames(state), "%s %s", sort, direction)
		}
	}
}

func TestListRootSummaryAndUnavailablePaths(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	manager := newTestManager(t, RootSpec{ID: "zulu", Path: first}, RootSpec{ID: "alpha", Path: second, Writable: true})

	state, err := manager.List(context.Background(), ListRequest{})
	require.NoError(t, err)
	assert.Equal(t, []Root{{ID: "alpha", Path: second, Writable: true}, {ID: "zulu", Path: first}}, state.Roots)
	_, err = manager.List(context.Background(), ListRequest{Root: "missing"})
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = manager.List(context.Background(), ListRequest{Root: "alpha", Path: "missing"})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestListAppliesScanEntryAndJSONBounds(t *testing.T) {
	t.Run("scan", func(t *testing.T) {
		root := t.TempDir()
		for i := range 3 {
			require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("%d", i)), nil, 0o600))
		}
		manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
		manager.maxScanned = 2
		state, err := manager.List(context.Background(), ListRequest{Root: "safe"})
		require.NoError(t, err)
		assert.True(t, state.Truncated)
		assert.Len(t, state.Entries, 2)
	})
	t.Run("entries", func(t *testing.T) {
		root := t.TempDir()
		for i := range 3 {
			require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("%d", i)), nil, 0o600))
		}
		manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
		manager.maxEntries = 2
		state, err := manager.List(context.Background(), ListRequest{Root: "safe"})
		require.NoError(t, err)
		assert.True(t, state.Truncated)
		assert.Len(t, state.Entries, 2)
	})
	t.Run("json", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "first"), nil, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(root, "second"), nil, 0o600))
		manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
		manager.maxJSONBytes = 1
		state, err := manager.List(context.Background(), ListRequest{Root: "safe"})
		require.NoError(t, err)
		assert.True(t, state.Truncated)
		assert.Empty(t, state.Entries)
	})
}

func TestListClosesResolvedDescriptorExactlyOnce(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "entry"), nil, 0o600))
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
	var closed []int
	manager.closeFD = func(fd int) error {
		closed = append(closed, fd)
		return unix.Close(fd)
	}

	_, err := manager.List(context.Background(), ListRequest{Root: "safe"})

	require.NoError(t, err)
	assert.Len(t, closed, 1)
}

func TestListProductionJSONBudgetLeavesBrokerEnvelopeHeadroom(t *testing.T) {
	root := t.TempDir()
	for i := range 400 {
		require.NoError(t, os.Symlink(strings.Repeat("x", 4000), filepath.Join(root, fmt.Sprintf("link-%03d", i))))
	}
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})

	assert.Equal(t, 1536<<10, manager.maxJSONBytes)
	assert.Less(t, manager.maxJSONBytes, maxBrokerJSONBytes)
	state, err := manager.List(context.Background(), ListRequest{Root: "safe"})
	require.NoError(t, err)
	assert.True(t, state.Truncated)
	assert.Less(t, len(state.Entries), 400)
}

func TestDownloadRegularFilesAndLimits(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("data"), 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(root, "empty"), nil, 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(root, "exact"), nil, 0o640))
	require.NoError(t, os.Truncate(filepath.Join(root, "exact"), 8))
	require.NoError(t, os.WriteFile(filepath.Join(root, "large"), nil, 0o640))
	require.NoError(t, os.Truncate(filepath.Join(root, "large"), 9))
	require.NoError(t, os.Mkdir(filepath.Join(root, "directory"), 0o755))
	require.NoError(t, os.Symlink("file", filepath.Join(root, "link")))
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.Symlink("nested", filepath.Join(root, "through-link")))
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
	manager.maxTransfer = 8

	for _, tc := range []struct {
		path string
		want string
		err  error
	}{
		{path: "file", want: "data"},
		{path: "empty", want: ""},
		{path: "exact", want: strings.Repeat("\x00", 8)},
		{path: "large", err: ErrTooLarge},
		{path: "directory", err: ErrNotFound},
		{path: "link", err: ErrNotFound},
		{path: "through-link/file", err: ErrNotFound},
		{path: "missing", err: ErrNotFound},
		{path: "../file", err: ErrInvalid},
	} {
		t.Run(tc.path, func(t *testing.T) {
			download, err := manager.Download(context.Background(), "safe", tc.path)
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			defer func() { assert.NoError(t, download.Body.Close()) }()
			data, err := io.ReadAll(download.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(data))
			assert.Equal(t, int64(len(data)), download.Size)
		})
	}
	assert.Equal(t, int64(256<<20), MaxTransferBytes)
}

func TestDownloadKeepsValidatedDescriptorAfterRename(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "item"), []byte("original"), 0o640))
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})

	download, err := manager.Download(context.Background(), "safe", "item")
	require.NoError(t, err)
	defer func() { assert.NoError(t, download.Body.Close()) }()
	require.NoError(t, os.Rename(filepath.Join(root, "item"), filepath.Join(root, "moved")))
	data, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	assert.Equal(t, "original", string(data))
}

func TestDownloadRejectsCancelledContext(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "item"), []byte("data"), 0o640))
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := manager.Download(ctx, "safe", "item")

	assert.ErrorIs(t, err, context.Canceled)
}

func TestDownloadRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, unix.Mkfifo(filepath.Join(root, "pipe"), 0o600))
	manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
	done := make(chan error, 1)

	go func() {
		_, err := manager.Download(context.Background(), "safe", "pipe")
		done <- err
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrNotFound)
	case <-time.After(time.Second):
		t.Fatal("download blocked while opening a FIFO")
	}
}

func TestUploadPublishesCompleteFilesWithRestrictedMetadata(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0o755))
	manager := newTestManager(t, RootSpec{ID: "write", Path: root, Writable: true})
	permitTestChown(manager)
	manager.maxTransfer = 8

	require.NoError(t, manager.Upload(context.Background(), "write", "nested", "file", strings.NewReader("data")))
	require.NoError(t, manager.Upload(context.Background(), "write", "", "empty", strings.NewReader("")))
	data, err := os.ReadFile(filepath.Join(root, "nested", "file"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(data))
	info, err := os.Stat(filepath.Join(root, "nested", "file"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	if os.Geteuid() == 0 {
		stat := info.Sys().(*unix.Stat_t)
		assert.Equal(t, uint32(0), stat.Uid)
		assert.Equal(t, uint32(0), stat.Gid)
	}
}

func TestUploadRejectsUnsafeRequestsAndOversizedStreams(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "directory"), 0o755))
	require.NoError(t, os.Symlink("directory", filepath.Join(root, "link")))
	readonly := newTestManager(t, RootSpec{ID: "read", Path: root})
	writable := newTestManager(t, RootSpec{ID: "write", Path: root, Writable: true})
	permitTestChown(writable)
	writable.maxTransfer = 8
	require.NoError(t, os.WriteFile(filepath.Join(root, "exists"), []byte("old"), 0o640))

	assert.ErrorIs(t, readonly.Upload(context.Background(), "read", "", "new", strings.NewReader("data")), ErrReadOnly)
	for _, name := range []string{"", ".", "..", "nested/name", "\x00"} {
		assert.ErrorIs(t, writable.Upload(context.Background(), "write", "", name, strings.NewReader("data")), ErrInvalid, name)
	}
	assert.ErrorIs(t, writable.Upload(context.Background(), "write", "link", "new", strings.NewReader("data")), ErrNotFound)
	assert.ErrorIs(t, writable.Upload(context.Background(), "write", "", "large", strings.NewReader(strings.Repeat("x", 9))), ErrTooLarge)
	_, err := os.Stat(filepath.Join(root, "large"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.ErrorIs(t, writable.Upload(context.Background(), "write", "", "exists", strings.NewReader("data")), ErrConflict)
	data, err := os.ReadFile(filepath.Join(root, "exists"))
	require.NoError(t, err)
	assert.Equal(t, "old", string(data))
}

func TestUploadNeverPublishesPartialFile(t *testing.T) {
	root := t.TempDir()
	manager := newTestManager(t, RootSpec{ID: "write", Path: root, Writable: true})
	permitTestChown(manager)
	reader := newBlockingReader([]byte("partial"))
	done := make(chan error, 1)
	go func() { done <- manager.Upload(context.Background(), "write", "", "new.txt", reader) }()
	reader.WaitUntilRead(t)
	_, err := os.Stat(filepath.Join(root, "new.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	reader.Finish()
	require.NoError(t, <-done)
}

func TestUploadReturnsReaderCancellationWithoutPublication(t *testing.T) {
	root := t.TempDir()
	manager := newTestManager(t, RootSpec{ID: "write", Path: root, Writable: true})

	err := manager.Upload(context.Background(), "write", "", "new", errorReader{err: context.Canceled})

	assert.ErrorIs(t, err, context.Canceled)
	_, statErr := os.Stat(filepath.Join(root, "new"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestUploadCleansUpOnCancellationAndSyscallFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*SystemManager)
		err   error
	}{
		{name: "cancelled", setup: func(m *SystemManager) {}, err: context.Canceled},
		{name: "write", setup: func(m *SystemManager) {
			m.writeFD = func(int, []byte) (int, error) { return 0, errors.New("write failed") }
		}, err: ErrUnavailable},
		{name: "sync", setup: func(m *SystemManager) { m.syncFD = func(int) error { return errors.New("sync failed") } }, err: ErrUnavailable},
		{name: "link", setup: func(m *SystemManager) {
			m.linkat = func(int, string, int, string, int) error { return errors.New("link failed") }
		}, err: ErrUnavailable},
		{name: "tmpfile unsupported", setup: func(m *SystemManager) { m.openTmpfile = func(int) (int, error) { return -1, unix.EOPNOTSUPP } }, err: ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			manager := newTestManager(t, RootSpec{ID: "write", Path: root, Writable: true})
			permitTestChown(manager)
			tc.setup(manager)
			ctx := context.Background()
			if tc.name == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			err := manager.Upload(ctx, "write", "", "new", strings.NewReader("data"))

			assert.ErrorIs(t, err, tc.err)
			_, statErr := os.Stat(filepath.Join(root, "new"))
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestUploadReportsDirectorySyncFailureAfterPublication(t *testing.T) {
	root := t.TempDir()
	manager := newTestManager(t, RootSpec{ID: "write", Path: root, Writable: true})
	permitTestChown(manager)
	manager.syncDirFD = func(int) error { return errors.New("directory sync failed") }

	err := manager.Upload(context.Background(), "write", "", "new", strings.NewReader("data"))

	assert.ErrorIs(t, err, ErrUnavailable)
	data, readErr := os.ReadFile(filepath.Join(root, "new"))
	require.NoError(t, readErr)
	assert.Equal(t, "data", string(data), "the destination is published despite uncertain directory durability")
}

type blockingReader struct {
	data    []byte
	started chan struct{}
	finish  chan struct{}
	once    sync.Once
	sent    bool
}

func newBlockingReader(data []byte) *blockingReader {
	return &blockingReader{data: data, started: make(chan struct{}), finish: make(chan struct{})}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	if r.sent {
		return 0, io.EOF
	}
	<-r.finish
	r.sent = true
	return bytes.NewReader(r.data).Read(p)
}

func (r *blockingReader) WaitUntilRead(t *testing.T) {
	t.Helper()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("reader was not read")
	}
}

func (r *blockingReader) Finish() { close(r.finish) }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func permitTestChown(manager *SystemManager) {
	if os.Geteuid() != 0 {
		manager.chownFD = func(int, int, int) error { return nil }
	}
}

func newTestManager(t *testing.T, specs ...RootSpec) *SystemManager {
	t.Helper()
	manager, err := NewSystemManager(specs)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	return manager
}

func entryNames(state State) []string {
	names := make([]string, len(state.Entries))
	for i, entry := range state.Entries {
		names[i] = entry.Name
	}
	return names
}

func entryNamed(t *testing.T, state State, name string) Entry {
	t.Helper()
	for _, entry := range state.Entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("entry %q not found", name)
	return Entry{}
}
