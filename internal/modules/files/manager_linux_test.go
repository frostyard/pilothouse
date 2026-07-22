//go:build linux

package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
