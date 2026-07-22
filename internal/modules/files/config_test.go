package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRootFlagsAcceptsReadOnlyAndWritableRoots(t *testing.T) {
	readOnly := t.TempDir()
	writable := t.TempDir()
	var roots RootFlags

	require.NoError(t, roots.Add("logs="+readOnly, false))
	require.NoError(t, roots.Flag(true).Set("uploads="+writable))

	assert.Equal(t, []RootSpec{
		{ID: "logs", Path: readOnly},
		{ID: "uploads", Path: writable, Writable: true},
	}, roots.Specs())
}

func TestRootFlagsAcceptsIDLengthBoundsAndEqualsInPath(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "with=equals")
	require.NoError(t, os.Mkdir(path, 0o755))
	var roots RootFlags

	require.NoError(t, roots.Add("a="+base, false))
	require.NoError(t, roots.Add(strings.Repeat("a", 32)+"="+path, true))

	assert.Equal(t, []RootSpec{
		{ID: "a", Path: base},
		{ID: strings.Repeat("a", 32), Path: path, Writable: true},
	}, roots.Specs())
}

func TestRootFlagsRejectInvalidConfiguration(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	for _, value := range []string{
		"=" + directory,
		"A=" + directory,
		"a_=" + directory,
		strings.Repeat("a", 33) + "=" + directory,
		"logs=relative",
		"host=/",
		"missing=" + filepath.Join(directory, "missing"),
		"file=" + file,
		"missing-separator",
		"long=" + filepath.Join("/", strings.Repeat("a", 4096)),
	} {
		t.Run(value, func(t *testing.T) {
			var roots RootFlags
			assert.Error(t, roots.Add(value, false))
		})
	}
}

func TestRootFlagsRejectDuplicateAcrossModes(t *testing.T) {
	var roots RootFlags
	require.NoError(t, roots.Add("logs="+t.TempDir(), false))

	err := roots.Add("logs="+t.TempDir(), true)

	assert.ErrorContains(t, err, "duplicate root id")
}

func TestRootFlagsRejectFilesystemRoot(t *testing.T) {
	var roots RootFlags

	assert.ErrorContains(t, roots.Add("host=/", false), "filesystem root")
}

func TestRootFlagsRejectsFinalSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "root")
	require.NoError(t, os.Symlink(target, link))
	var roots RootFlags

	assert.Error(t, roots.Add("root="+link, false))
}

func TestRootFlagsRejectsSixtyFifthRoot(t *testing.T) {
	base := t.TempDir()
	var roots RootFlags
	for i := range MaxRoots {
		id := fmt.Sprintf("root-%d", i)
		path := filepath.Join(base, id)
		require.NoError(t, os.Mkdir(path, 0o755))
		require.NoError(t, roots.Add(id+"="+path, false))
	}

	err := roots.Add("last="+base, false)

	assert.ErrorContains(t, err, "maximum number of roots")
}

func TestRootFlagsSpecsAreDeterministic(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	var roots RootFlags
	require.NoError(t, roots.Add("zulu="+first, false))
	require.NoError(t, roots.Add("alpha="+second, true))

	assert.Equal(t, []RootSpec{
		{ID: "alpha", Path: second, Writable: true},
		{ID: "zulu", Path: first},
	}, roots.Specs())
}

func TestRootManagerAllowsNoRoots(t *testing.T) {
	manager, err := NewSystemManager(nil)
	require.NoError(t, err)

	assert.Equal(t, MaxTransferBytes, manager.maxTransfer)
	assert.Equal(t, MaxEntries, manager.maxEntries)
	assert.Equal(t, MaxScannedEntries, manager.maxScanned)
	assert.Equal(t, maxJSONBytes, manager.maxJSONBytes)
	assert.NoError(t, manager.Close())
	assert.NoError(t, manager.Close())
}

func TestRootManagerRejectsDuplicateSpecs(t *testing.T) {
	path := t.TempDir()

	manager, err := NewSystemManager([]RootSpec{
		{ID: "root", Path: path},
		{ID: "root", Path: path, Writable: true},
	})

	assert.Nil(t, manager)
	assert.ErrorContains(t, err, "duplicate root id")
}

func TestRootManagerRejectsFinalSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "root")
	require.NoError(t, os.Symlink(target, link))

	manager, err := NewSystemManager([]RootSpec{{ID: "root", Path: link}})

	assert.Nil(t, manager)
	assert.Error(t, err)
}

func TestRootManagerClosesDescriptorsExactlyOnce(t *testing.T) {
	path := t.TempDir()
	manager, err := NewSystemManager([]RootSpec{{ID: "root", Path: path}})
	require.NoError(t, err)
	fd := manager.roots["root"].fd

	require.NoError(t, manager.Close())
	assert.ErrorIs(t, unix.Close(fd), unix.EBADF)
	assert.NoError(t, manager.Close())
}
