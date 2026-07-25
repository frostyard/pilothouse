package packagingtest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/packagingtest"
)

// recordingT records what packagingtest.LookTool did to it instead of aborting
// the test, so both of the gate's branches can be observed from a test that
// itself passes. This is the whole reason packagingtest declares its own
// TestingT interface rather than using testing.TB, which cannot be implemented
// outside the testing package.
type recordingT struct {
	helpers int
	skips   []string
	fatals  []string
}

func (r *recordingT) Helper() { r.helpers++ }

func (r *recordingT) Skipf(format string, args ...any) {
	r.skips = append(r.skips, fmt.Sprintf(format, args...))
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

// unsetRequireEnv clears RequireEnv for the duration of the test, restoring
// whatever the environment had before. t.Setenv registers that restoration,
// so unsetting straight afterwards is still undone at cleanup. The dev image
// sets RequireEnv=1, so the unset branch has to be established rather than
// assumed.
func unsetRequireEnv(t *testing.T) {
	t.Helper()

	t.Setenv(packagingtest.RequireEnv, "1")

	if err := os.Unsetenv(packagingtest.RequireEnv); err != nil {
		t.Fatalf("unset %s: %v", packagingtest.RequireEnv, err)
	}
}

// TestLookToolBranches drives every cell of the gate's matrix: two ways for a
// tool to be unresolvable (a name that exists nowhere, and a name that exists
// but is hidden by an empty PATH) crossed with RequireEnv unset and set, plus
// the resolvable case. It needs no external tool and passes on every host.
func TestLookToolBranches(t *testing.T) {
	const absent = "packagingtest-tool-that-does-not-exist"

	t.Run("absent tool with the flag unset skips", func(t *testing.T) {
		unsetRequireEnv(t)

		var rec recordingT

		got := packagingtest.LookTool(&rec, absent)

		requireBranch(t, &rec, got, "skip", absent)
		requireContains(t, rec.skips[0], "make docker-ci")
	})

	t.Run("absent tool with the flag set fails", func(t *testing.T) {
		t.Setenv(packagingtest.RequireEnv, "1")

		var rec recordingT

		got := packagingtest.LookTool(&rec, absent)

		requireBranch(t, &rec, got, "fatal", absent)
	})

	// A tool that does resolve on this host, hidden behind an empty PATH:
	// the same two branches must fire for a name that is only unresolvable
	// because of where the test is running.
	t.Run("hidden tool with the flag unset skips", func(t *testing.T) {
		unsetRequireEnv(t)
		t.Setenv("PATH", "")

		var rec recordingT

		got := packagingtest.LookTool(&rec, "sh")

		requireBranch(t, &rec, got, "skip", "sh")
		requireContains(t, rec.skips[0], "make docker-ci")
	})

	t.Run("hidden tool with the flag set fails", func(t *testing.T) {
		t.Setenv(packagingtest.RequireEnv, "1")
		t.Setenv("PATH", "")

		var rec recordingT

		got := packagingtest.LookTool(&rec, "sh")

		requireBranch(t, &rec, got, "fatal", "sh")
	})
}

// TestLookToolReturnsThePathOfAResolvableTool covers the third branch: a tool
// that resolves everywhere yields an absolute path and touches neither Skipf
// nor Fatalf.
func TestLookToolReturnsThePathOfAResolvableTool(t *testing.T) {
	// The standard directories are put back on PATH first, so this test states
	// something about LookTool rather than about the PATH the test binary
	// happened to be launched with. /bin/sh is required of every POSIX host.
	t.Setenv("PATH", "/bin:/usr/bin:"+os.Getenv("PATH"))

	var rec recordingT

	got := packagingtest.LookTool(&rec, "sh")

	if len(rec.skips) != 0 || len(rec.fatals) != 0 {
		t.Fatalf("LookTool(sh) recorded skips %q and fatals %q, want neither", rec.skips, rec.fatals)
	}

	if got == "" {
		t.Fatal("LookTool(sh) returned an empty path")
	}

	if !filepath.IsAbs(got) {
		t.Fatalf("LookTool(sh) returned %q, want an absolute path", got)
	}

	if rec.helpers == 0 {
		t.Error("LookTool did not call Helper")
	}
}

// requireBranch asserts that exactly one of the gate's two failure branches
// fired, that the other did not, that the recorded message names the tool, and
// that the returned path is empty.
func requireBranch(t *testing.T, rec *recordingT, got, want, tool string) {
	t.Helper()

	wantSkips, wantFatals := 1, 0
	if want == "fatal" {
		wantSkips, wantFatals = 0, 1
	}

	if len(rec.skips) != wantSkips || len(rec.fatals) != wantFatals {
		t.Fatalf("LookTool(%s) recorded %d skips and %d fatals, want %d and %d (skips %q, fatals %q)",
			tool, len(rec.skips), len(rec.fatals), wantSkips, wantFatals, rec.skips, rec.fatals)
	}

	msg := rec.skips
	if want == "fatal" {
		msg = rec.fatals
	}

	requireContains(t, msg[0], tool)

	if got != "" {
		t.Errorf("LookTool(%s) returned %q, want an empty path so the branch is observable", tool, got)
	}

	if rec.helpers == 0 {
		t.Error("LookTool did not call Helper")
	}
}

func requireContains(t *testing.T, got, want string) {
	t.Helper()

	if !strings.Contains(got, want) {
		t.Errorf("message %q does not name %q", got, want)
	}
}
