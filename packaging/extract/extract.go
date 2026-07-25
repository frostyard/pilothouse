// Package extract reads a real operating-system package off disk and produces
// the model that describes it.
//
// Deb returns a packaging.Model: a packaging.Format, one packaging.Entry per
// installed file, the dependency strings the package declares, and an optional
// packaging.Scriptlet. That is the whole job. Nothing here decides whether a
// model is acceptable — the artifact contract lives in the parent packaging
// package and is checked there, and moving one of its assertions down here
// would be a defect, because it is exactly that separation that keeps every
// contract assertion provable on a host with no packaging tool installed.
//
// Why this is a subpackage of packaging rather than more files in packaging
// itself:
//
//   - The parent package is documented as inert at run time. yeti/OVERVIEW.md
//     pins the mechanical form of that guarantee as
//     `grep -lE 'os/exec|exec\.Command' packaging/*.go` listing exactly
//     units_test.go and postinstall_test.go, and that glob is NOT recursive, so
//     every exec.Command added here leaves the guarantee exactly true.
//   - The parent package's contract tables and embedded repository sources are
//     unexported. From here they are not merely off limits, they are invisible,
//     so contract logic cannot leak into an extractor by accident.
//   - packaging/drift_test.go already declares usrBinDir and underUsrBin in
//     the parent package itself. Those belong to the drift guard and stay
//     exactly where they are; in this package the names are free.
//
// The parent package itself cannot move in the other direction: a //go:embed
// pattern may not name a parent directory, so only a package rooted at
// packaging/ can embed packaging/*.
package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrToolUnavailable is wrapped into every error caused by an external
// packaging tool that is not on PATH.
//
// It separates an environmental fault — this host has no dpkg-deb — from a
// definitive one, such as a tool that ran and rejected the artifact. errors.Is
// reports true for the first and false for the second; collapsing the two would
// leave a caller unable to tell "not installed" from "not a package".
var ErrToolUnavailable = errors.New("packaging tool unavailable")

// lookTool resolves an external packaging tool on PATH and returns its path.
//
// Every error it returns carries the literal prefix
// "packaging/extract: <tool>: " and wraps ErrToolUnavailable, so
// "packaging/extract: dpkg-deb: packaging tool unavailable" is the shape a
// caller sees. The prefix is produced here rather than at the call sites so
// that no path can return a tool error without it, and because it is a token an
// artifact's filename cannot forge: matching on the bare tool name would be
// satisfied by any file called b.rpm.
func lookTool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("packaging/extract: %s: %w: %v", name, ErrToolUnavailable, err)
	}

	return path, nil
}

// runTool resolves name, runs it with args and returns its standard output.
//
// The subprocess runs under the caller's context via exec.CommandContext and
// never falls back to a background one, so cancelling the caller cancels the
// tool. On failure the tool's standard error is folded into the returned error,
// which carries the same "packaging/extract: <tool>: " prefix lookTool
// produces. Folding standard error in is also what puts the offending
// artifact's path into the message: dpkg-deb names the file it could not read
// there (`dpkg-deb: error: 'junk.deb' is not a Debian format archive`).
func runTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := lookTool(name)
	if err != nil {
		return nil, err
	}

	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}

	if detail := strings.TrimSpace(stderr.String()); detail != "" {
		return nil, fmt.Errorf("packaging/extract: %s: %w: %s", name, err, detail)
	}

	return nil, fmt.Errorf("packaging/extract: %s: %w", name, err)
}

// usrBinDir is the directory nFPM installs a build's own binary into. Entries
// there carry no Content: their bytes are compared against nothing, and each is
// roughly 20 MB, so reading them in would be pure waste. The rule is stated in
// terms of the payload, not of any requirement table, so it leaks no contract
// knowledge into this package.
//
// This deliberately duplicates the identically named constant and helper in
// packaging/drift_test.go. Those two are test-only and belong to the drift
// guard; a name declared in a _test.go file is unreachable from another
// package, so this phase declares its own rather than moving or exporting
// theirs.
const usrBinDir = "/usr/bin"

// underUsrBin reports whether dest is usrBinDir itself or is nested under it.
func underUsrBin(dest string) bool {
	return dest == usrBinDir || strings.HasPrefix(dest, usrBinDir+"/")
}
