package imagetest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// imageProcessTimeout is the one deadline used by the image-tier test
// process contract. The AST guard in image_process_test.go proves that every
// image-tier child is constructed here with this bound.
const imageProcessTimeout = time.Second
const imageOutputReadLimit = 4 << 20

// imageSandbox is the complete test-owned workspace advertised to an
// image-tier child through its environment and working directory. It is not a
// filesystem or network namespace. One t.TempDir owns every path so the test
// runner performs the only recursive cleanup.
type imageSandbox struct {
	root   string
	cwd    string
	home   string
	runner string
	tmp    string
	bin    string
}

func newImageSandbox(t *testing.T) imageSandbox {
	t.Helper()

	root := t.TempDir()
	sandbox := imageSandbox{
		root:   root,
		cwd:    filepath.Join(root, "cwd"),
		home:   filepath.Join(root, "home"),
		runner: filepath.Join(root, "runner"),
		tmp:    filepath.Join(root, "tmp"),
		bin:    filepath.Join(root, "bin"),
	}

	for _, dir := range sandbox.directories() {
		require.NoErrorf(t, os.Mkdir(dir, 0o700), "create sandbox directory %s", dir)
	}

	return sandbox
}

func (s imageSandbox) directories() []string {
	return []string{s.cwd, s.home, s.runner, s.tmp, s.bin}
}

func (s imageSandbox) environment() []string {
	return []string{
		"HOME=" + s.home,
		"RUNNER_TEMP=" + s.runner,
		"TMPDIR=" + s.tmp,
		"PATH=" + strings.Join([]string{s.bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)),
		"LC_ALL=C",
	}
}

type imageSnapshotEntry struct {
	Type       fs.FileMode
	Perm       fs.FileMode
	Size       int64
	Digest     [sha256.Size]byte
	LinkTarget string
}

type imageSnapshot map[string]imageSnapshotEntry

// imageSnapshotTree records content and metadata without following symlinks.
// Entry classification comes from the stat-backed FileInfo mode, never the
// possibly incomplete type bits on fs.DirEntry.
func imageSnapshotTree(root string) (imageSnapshot, error) {
	snapshot := imageSnapshot{}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if path == root {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}

		record := imageSnapshotEntry{
			Type: info.Mode().Type(),
			Perm: info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky),
			Size: info.Size(),
		}

		switch {
		case info.Mode().IsRegular():
			file, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open %s: %w", path, err)
			}

			digest := sha256.New()
			_, copyErr := io.Copy(digest, file)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("hash %s: %w", path, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", path, closeErr)
			}
			copy(record.Digest[:], digest.Sum(nil))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read symlink %s: %w", path, err)
			}

			record.LinkTarget = target
		}

		snapshot[filepath.ToSlash(relative)] = record

		return nil
	})
	if err != nil {
		return nil, err
	}

	return snapshot, nil
}

type imageChildResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Err      error
}

// imageRunChild is the only child constructor in the image tier. Output goes
// to regular files inside the sandbox: a descendant inheriting the descriptors
// cannot keep Go blocked on an open stdout/stderr pipe after the bounded parent
// exits.
func imageRunChild(t *testing.T, sandbox imageSandbox, tool string, args ...string) imageChildResult {
	t.Helper()

	require.Truef(t, filepath.IsAbs(tool), "child tool %q must be an absolute path", tool)

	stdout, err := os.CreateTemp(sandbox.tmp, "stdout-*")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, stdout.Close())
	}()
	require.NoError(t, os.Remove(stdout.Name()))

	stderr, err := os.CreateTemp(sandbox.tmp, "stderr-*")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, stderr.Close())
	}()
	require.NoError(t, os.Remove(stderr.Name()))

	ctx, cancel := context.WithTimeout(context.Background(), imageProcessTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, tool, args...)
	command.Dir = sandbox.cwd
	command.Env = sandbox.environment()
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var deadlineCancelled atomic.Bool
	command.Cancel = func() error {
		deadlineCancelled.Store(true)

		return imageKillProcessGroup(command.Process.Pid)
	}

	runErr := command.Run()
	if command.Process != nil {
		cleanupErr := imageKillProcessGroup(command.Process.Pid)
		if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
			runErr = errors.Join(runErr, fmt.Errorf("clean child process group: %w", cleanupErr))
		}
	}

	stdoutBytes, err := imageReadCapture(stdout)
	require.NoError(t, err)
	stderrBytes, err := imageReadCapture(stderr)
	require.NoError(t, err)

	exitCode := 0
	if runErr != nil {
		exitCode = -1

		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}

	return imageChildResult{
		Stdout:   string(stdoutBytes),
		Stderr:   string(stderrBytes),
		ExitCode: exitCode,
		TimedOut: deadlineCancelled.Load() || errors.Is(runErr, context.DeadlineExceeded),
		Err:      runErr,
	}
}

func imageKillProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}

	return err
}

func imageReadCapture(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek child output %s: %w", file.Name(), err)
	}

	content, err := io.ReadAll(io.LimitReader(file, imageOutputReadLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read child output %s: %w", file.Name(), err)
	}
	if len(content) > imageOutputReadLimit {
		return nil, fmt.Errorf("child output in %s exceeded the %d-byte read limit",
			file.Name(), imageOutputReadLimit)
	}

	return content, nil
}

func imageLookTool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}

	return filepath.Abs(path)
}

func imageRequireTool(t *testing.T, name string) string {
	t.Helper()

	path, err := imageLookTool(name)
	if err != nil {
		if os.Getenv("PILOTHOUSE_REQUIRE_PACKAGING_TOOLS") == "1" {
			t.Fatalf("%s is not on PATH (%v), but the development image declares required tools present",
				name, err)
		}
		t.Skipf("skipping: %s is not on PATH (%v)", name, err)
	}

	return path
}

func TestImageChildSandboxLayout(t *testing.T) {
	sandbox := newImageSandbox(t)

	var names []string
	for _, dir := range sandbox.directories() {
		require.Equal(t, sandbox.root, filepath.Dir(dir))

		info, err := os.Stat(dir)
		require.NoError(t, err)
		require.True(t, info.IsDir())
		require.Equal(t, fs.FileMode(0o700), info.Mode().Perm())

		names = append(names, filepath.Base(dir))
	}

	sort.Strings(names)
	require.Equal(t, []string{"bin", "cwd", "home", "runner", "tmp"}, names)

	env := strings.Join(sandbox.environment(), "\n")
	for _, path := range []string{sandbox.home, sandbox.runner, sandbox.tmp, sandbox.bin} {
		require.Contains(t, env, path)
		require.True(t, strings.HasPrefix(path, sandbox.root+string(os.PathSeparator)))
	}
}

func TestImageChildSnapshotDetectsChanges(t *testing.T) {
	type snapshotCase struct {
		mutate func(t *testing.T, sandbox imageSandbox, path string)
		assert func(t *testing.T, before, after imageSnapshot)
	}

	testCases := map[string]snapshotCase{
		"content": {
			mutate: func(t *testing.T, _ imageSandbox, path string) {
				t.Helper()
				require.NoError(t, os.WriteFile(path, []byte("modified"), 0o600))
			},
			assert: func(t *testing.T, before, after imageSnapshot) {
				t.Helper()
				require.Equal(t, before["cwd/entry"].Type, after["cwd/entry"].Type)
				require.Equal(t, before["cwd/entry"].Perm, after["cwd/entry"].Perm)
				require.Equal(t, before["cwd/entry"].Size, after["cwd/entry"].Size)
				require.NotEqual(t, before["cwd/entry"].Digest, after["cwd/entry"].Digest)
			},
		},
		"size": {
			mutate: func(t *testing.T, _ imageSandbox, path string) {
				t.Helper()
				require.NoError(t, os.WriteFile(path, []byte("short"), 0o600))
			},
			assert: func(t *testing.T, before, after imageSnapshot) {
				t.Helper()
				require.EqualValues(t, len("original"), before["cwd/entry"].Size)
				require.EqualValues(t, len("short"), after["cwd/entry"].Size)
				require.NotEqual(t, before["cwd/entry"].Size, after["cwd/entry"].Size)
			},
		},
		"permission": {
			mutate: func(t *testing.T, _ imageSandbox, path string) {
				t.Helper()
				require.NoError(t, os.Chmod(path, 0o640))
			},
			assert: func(t *testing.T, before, after imageSnapshot) {
				t.Helper()
				require.Equal(t, before["cwd/entry"].Type, after["cwd/entry"].Type)
				require.NotEqual(t, before["cwd/entry"].Perm, after["cwd/entry"].Perm)
				require.Equal(t, before["cwd/entry"].Size, after["cwd/entry"].Size)
				require.Equal(t, before["cwd/entry"].Digest, after["cwd/entry"].Digest)
			},
		},
		"special permission": {
			mutate: func(t *testing.T, _ imageSandbox, path string) {
				t.Helper()
				require.NoError(t, os.Chmod(path, 0o600|fs.ModeSetuid))
			},
			assert: func(t *testing.T, before, after imageSnapshot) {
				t.Helper()
				require.Equal(t, before["cwd/entry"].Type, after["cwd/entry"].Type)
				require.Equal(t, fs.FileMode(0), before["cwd/entry"].Perm&fs.ModeSetuid)
				require.Equal(t, fs.ModeSetuid, after["cwd/entry"].Perm&fs.ModeSetuid)
				require.Equal(t, before["cwd/entry"].Size, after["cwd/entry"].Size)
				require.Equal(t, before["cwd/entry"].Digest, after["cwd/entry"].Digest)
			},
		},
		"entry type": {
			mutate: func(t *testing.T, _ imageSandbox, path string) {
				t.Helper()
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
			},
			assert: func(t *testing.T, before, after imageSnapshot) {
				t.Helper()
				require.True(t, before["cwd/entry"].Type.IsRegular())
				require.True(t, after["cwd/entry"].Type.IsDir())
			},
		},
		"symlink target": {
			mutate: func(t *testing.T, sandbox imageSandbox, _ string) {
				t.Helper()
				link := filepath.Join(sandbox.cwd, "link")
				require.NoError(t, os.Remove(link))
				require.NoError(t, os.Symlink("other", link))
			},
			assert: func(t *testing.T, before, after imageSnapshot) {
				t.Helper()
				require.Equal(t, before["cwd/link"].Type, after["cwd/link"].Type)
				require.Equal(t, before["cwd/link"].Perm, after["cwd/link"].Perm)
				require.Equal(t, before["cwd/link"].Size, after["cwd/link"].Size)
				require.NotEqual(t, before["cwd/link"].LinkTarget, after["cwd/link"].LinkTarget)
			},
		},
	}

	for name, mutate := range testCases {
		t.Run(name, func(t *testing.T) {
			sandbox := newImageSandbox(t)
			path := filepath.Join(sandbox.cwd, "entry")
			require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))
			require.NoError(t, os.Symlink("entry", filepath.Join(sandbox.cwd, "link")))

			before, err := imageSnapshotTree(sandbox.root)
			require.NoError(t, err)
			unchanged, err := imageSnapshotTree(sandbox.root)
			require.NoError(t, err)
			require.Equal(t, before, unchanged)

			mutate.mutate(t, sandbox, path)

			after, err := imageSnapshotTree(sandbox.root)
			require.NoError(t, err)
			require.NotEqual(t, before, after)
			mutate.assert(t, before, after)
		})
	}
}

func TestImageChildReportsEnvironmentOutputAndStatus(t *testing.T) {
	sandbox := newImageSandbox(t)
	shell := imageRequireTool(t, "bash")
	marker := filepath.Join(sandbox.cwd, "marker")
	t.Setenv("PILOTHOUSE_IMAGE_PARENT_ONLY", "must-not-leak")

	result := imageRunChild(t, sandbox, shell, "-c", `
printf '%s\n' "$PWD" "$HOME" "$RUNNER_TEMP" "$TMPDIR" "$PATH" "$LC_ALL" "${PILOTHOUSE_IMAGE_PARENT_ONLY-unset}"
printf 'stderr-value\n' >&2
printf 'reached\n' > marker
exit 7
`)

	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.Equal(t, 7, result.ExitCode)
	require.Equal(t, "stderr-value\n", result.Stderr)
	require.Equal(t, strings.Join([]string{
		sandbox.cwd,
		sandbox.home,
		sandbox.runner,
		sandbox.tmp,
		strings.Join([]string{sandbox.bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)),
		"C",
		"unset",
		"",
	}, "\n"), result.Stdout)

	content, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "reached\n", string(content))
}

func TestImageChildDeadlineIsBounded(t *testing.T) {
	sandbox := newImageSandbox(t)
	shell := imageRequireTool(t, "bash")
	started := time.Now()

	result := imageRunChild(t, sandbox, shell, "-c", `
sleep 60 &
printf '%s\n' "$!" > "$TMPDIR/descendant.pid"
wait
`)
	elapsed := time.Since(started)

	require.Error(t, result.Err)
	require.True(t, result.TimedOut)
	require.Equal(t, -1, result.ExitCode)
	require.GreaterOrEqual(t, elapsed, imageProcessTimeout)
	require.Less(t, elapsed, imageProcessTimeout+3*time.Second)

	pidBytes, err := os.ReadFile(filepath.Join(sandbox.tmp, "descendant.pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	imageRequireProcessStopped(t, pid)
}

func TestImageChildDirectToolDeadlineIsBounded(t *testing.T) {
	sandbox := newImageSandbox(t)
	sleeper := imageRequireTool(t, "sleep")
	started := time.Now()

	result := imageRunChild(t, sandbox, sleeper, "4")
	elapsed := time.Since(started)

	require.Error(t, result.Err)
	require.True(t, result.TimedOut)
	require.Equal(t, -1, result.ExitCode)
	require.GreaterOrEqual(t, elapsed, imageProcessTimeout)
	require.Less(t, elapsed, imageProcessTimeout+2*time.Second)
}

func TestImageChildCleansDescendantAfterSuccessfulParent(t *testing.T) {
	sandbox := newImageSandbox(t)
	shell := imageRequireTool(t, "bash")

	result := imageRunChild(t, sandbox, shell, "-c", `
sleep 60 >/dev/null 2>&1 &
printf '%s\n' "$!" > "$TMPDIR/descendant.pid"
`)
	require.NoError(t, result.Err)
	require.False(t, result.TimedOut)

	pidBytes, err := os.ReadFile(filepath.Join(sandbox.tmp, "descendant.pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	imageRequireProcessStopped(t, pid)
}

func imageRequireProcessStopped(t *testing.T, pid int) {
	t.Helper()

	require.Eventually(t, func() bool {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		if err != nil {
			return false
		}

		endName := strings.LastIndexByte(string(stat), ')')

		return endName >= 0 && len(stat) > endName+2 && stat[endName+2] == 'Z'
	}, 2*time.Second, 10*time.Millisecond, "descendant process %d is still running", pid)
}

func TestImageChildCaptureReadLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture")
	file, err := os.Create(path)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, file.Close())
	}()

	_, err = file.WriteString(strings.Repeat("x", imageOutputReadLimit+1))
	require.NoError(t, err)
	content, err := imageReadCapture(file)
	require.ErrorContains(t, err, "exceeded")
	require.Nil(t, content)
}

func TestImageChildNoopLeavesSnapshotUnchanged(t *testing.T) {
	sandbox := newImageSandbox(t)
	shell := imageRequireTool(t, "bash")

	before, err := imageSnapshotTree(sandbox.root)
	require.NoError(t, err)
	result := imageRunChild(t, sandbox, shell, "-c", ":")
	require.NoError(t, result.Err)
	require.False(t, result.TimedOut)
	after, err := imageSnapshotTree(sandbox.root)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestImageChildToolLookup(t *testing.T) {
	path, err := imageLookTool("sh")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(path))

	path, err = imageLookTool("pilothouse-deliberately-missing-image-test-tool")
	require.Error(t, err)
	require.Empty(t, path)
}
