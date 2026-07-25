// Package packagingtest holds test-support helpers for building throwaway
// operating-system packages and for gating the tests that need the packaging
// tools.
//
// It is an ordinary (non-`_test.go`) package because the same helpers are
// needed by the tests of more than one package, and an unexported helper
// living in a `_test.go` file is unreachable from another package. It ships in
// no binary — `internal/` states that mechanically — and it imports neither
// `packaging` nor any package derived from it, so it can never acquire
// knowledge of the artifact contract: a fixture built here describes only what
// the caller declared.
package packagingtest

import (
	"os"
	"os/exec"
)

// RequireEnv names the environment variable that declares the packaging tools
// to be present. `.docker/Dockerfile` sets it to "1", so a tool-dependent test
// that would otherwise skip inside `make docker-ci` fails there instead.
const RequireEnv = "PILOTHOUSE_REQUIRE_PACKAGING_TOOLS"

// TestingT is the slice of *testing.T this package uses. *testing.T satisfies
// it.
//
// The interface is declared here rather than reusing testing.TB — which has an
// unexported method and cannot be implemented outside `testing` — precisely so
// that LookTool's own two branches are observable from a recording fake in a
// test that passes, instead of only by failing the test that exercises them.
type TestingT interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// LookTool resolves an external packaging tool and returns its path.
//
// When the tool cannot be resolved the behaviour depends on RequireEnv: if it
// is "1" the calling test fails, because the environment declares the tool
// present and a skip there would silently hide the check; otherwise the
// calling test skips with a reason naming the tool. The wording follows
// packaging/units_test.go's systemd-analyze skip and
// packaging/postinstall_test.go's shellcheck skip.
//
// The returned path is "" after either call, so a recording TestingT (whose
// Skipf and Fatalf return rather than aborting) can observe which branch ran.
// This is the only place in this package that resolves a tool on PATH: every
// tool-dependent test goes through here, so none can reach a tool around the
// gate.
func LookTool(t TestingT, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}

	if os.Getenv(RequireEnv) == "1" {
		t.Fatalf("%s is not on PATH (%v), but %s=1 declares the packaging tools present (see `.docker/Dockerfile`), so this check must not skip under `make docker-ci`",
			name, err, RequireEnv)

		return ""
	}

	t.Skipf("skipping: %s is not on PATH (%v); `.docker/Dockerfile` installs it so this check runs under `make docker-ci`", name, err)

	return ""
}
