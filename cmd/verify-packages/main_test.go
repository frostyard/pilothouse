package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/packaging"
)

// Every test in this file drives run, the function whose return value becomes
// the process exit status — never a reporting or dispatch helper
// (docs/agents/skills/test-the-composing-function-not-its-merge-helper.md).
//
// Every test but TestRunExitStatusIsDerivedFromResults drives it through
// defaultDeps(), the production wiring, so discovery, dispatch by extension and
// real extraction failures are proven against the code main runs. That one test
// injects substitutes, and only for the outcomes no artifact available here can
// produce: a successful extraction and a clean verification
// (docs/agents/skills/exercise-the-actual-boundary-not-a-precomputed-shim.md).
//
// None of these tests needs a packaging tool. dpkg-deb is present on the
// development host and rpm is not, and every assertion below holds either way:
// the artifacts are random bytes, so a resolvable tool rejects them and an
// unresolvable one is reported missing, and both failures carry the same
// "packaging/extract: <tool>: " prefix.

// execRun runs the command with args and returns its exit status and the two
// streams it wrote.
func execRun(t *testing.T, d deps, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := run(context.Background(), d, args, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

// artifactBlock returns the region of out that belongs to path: the line naming
// that artifact plus the indented finding lines under it, stopping before the
// next artifact's line and before the summary.
//
// Substring assertions over a run that reports several artifacts are applied to
// this slice, never to the whole capture, because a whole-capture check cannot
// tell "the deb block named dpkg-deb" from "some other block did"
// (docs/agents/skills/scope-html-assertions-to-the-region-under-test.md, which
// generalizes to any multi-region output).
func artifactBlock(t *testing.T, out, path string) string {
	t.Helper()

	lines := strings.Split(out, "\n")

	start := -1

	for i, line := range lines {
		if strings.HasPrefix(line, path+" ") || strings.HasPrefix(line, path+":") {
			start = i

			break
		}
	}

	if start < 0 {
		t.Fatalf("no block for %s in output:\n%s", path, out)
	}

	end := start + 1
	for end < len(lines) && strings.HasPrefix(lines[end], "\t") {
		end++
	}

	return strings.Join(lines[start:end], "\n")
}

// findingLines counts the indented per-finding lines in out.
func findingLines(out string) int {
	count := 0

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "\t") {
			count++
		}
	}

	return count
}

// writeArtifact stages a file of arbitrary bytes under dir. The bytes are not a
// package: every test using one expects extraction to fail, and what is being
// proven is which backend failed, not what it parsed.
func writeArtifact(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not a package: "+name), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

// TestRunWithNoArtifactsExplainsWhereTheyComeFrom pins the message an empty
// search prints. That is the expected outcome on this host and in the
// development image, so this message is the command's most-seen output.
//
// It has to be accurate at this commit: the two workflows named are the only
// producers that publish, and `make package` is the local producer that really
// exists but requires goreleaser Pro, so the message has to carry that
// requirement rather than reading as an instruction that would simply fail.
//
// The target check is deliberately not a hardcoded name list: every `make
// <target>` the message names is looked up in the repository's own Makefile,
// so the message can never point a reader at a target that does not exist.
func TestRunWithNoArtifactsExplainsWhereTheyComeFrom(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	code, stdout, stderr := execRun(t, defaultDeps(), "-dir", dir)
	if code != 1 {
		t.Fatalf("exit status = %d, want 1", code)
	}

	out := stdout + stderr

	for _, want := range []string{
		dir,
		".github/workflows/release.yml",
		".github/workflows/snapshot.yml",
		"make package",
		"goreleaser Pro",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}

	// No issue reference belongs in this message any more: `make package`
	// exists, so the message states what it needs rather than pointing at an
	// open issue. The check is on the shape, not on one issue number, so a
	// different one cannot creep back in.
	if issue := regexp.MustCompile(`#\d+`).FindString(out); issue != "" {
		t.Errorf("output still frames packaging as future work, referencing issue %s:\n%s", issue, out)
	}

	// The companion half of the same requirement: the message may only name a
	// make target that is really defined, checked against the live Makefile
	// rather than against a second copy of the expected target list.
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	named := namedMakeTargets(out)
	if len(named) == 0 {
		t.Fatalf("output names no make target, so the target check proves nothing:\n%s", out)
	}

	for _, target := range named {
		if !makeRuleDefined(string(makefile), target) {
			t.Errorf("message names `make %s`, which the repository Makefile does not define:\n%s", target, out)
		}
	}
}

// namedMakeTargets extracts every target named as `make <target>` in out.
func namedMakeTargets(out string) []string {
	targets := make([]string, 0, 1)

	for _, field := range strings.Split(out, "make ")[1:] {
		end := strings.IndexFunc(field, func(r rune) bool { return !isTargetRune(r) })
		if end < 0 {
			end = len(field)
		}

		if end > 0 {
			targets = append(targets, field[:end])
		}
	}

	return targets
}

// isTargetRune reports whether r may appear in a make target name.
func isTargetRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '_', r == '.':
		return true
	default:
		return false
	}
}

// makeRuleDefined reports whether makefile carries a `<target>:` rule.
func makeRuleDefined(makefile, target string) bool {
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, target+":") {
			return true
		}
	}

	return false
}

// TestRunDiscoversAndDispatchesBothExtensions is the discovery and dispatch
// proof, and it runs on every host because it needs no packaging tool.
//
// Discovery: both globs run, so an .rpm is found as well as a .deb, and a file
// that is neither is not treated as an artifact.
//
// Dispatch: the assertion is on the "packaging/extract: <tool>: " prefix the
// extract package puts on every tool error. A bare tool-name check would be
// vacuous — the filename b.rpm contains "rpm" all by itself — while that prefix
// is a token no filename can forge. It also holds in both environments: where
// the tool is missing the error is ErrToolUnavailable, and where it is present
// the random bytes make it a parse failure, and both carry the prefix.
func TestRunDiscoversAndDispatchesBothExtensions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deb := writeArtifact(t, dir, "a.deb")
	rpm := writeArtifact(t, dir, "b.rpm")
	decoy := writeArtifact(t, dir, "c.txt")

	code, stdout, stderr := execRun(t, defaultDeps(), "-dir", dir)
	if code != 1 {
		t.Fatalf("exit status = %d, want 1", code)
	}

	out := stdout + stderr

	if strings.Contains(out, decoy) {
		t.Errorf("output reports the non-artifact %s:\n%s", decoy, out)
	}

	if !strings.Contains(out, "2 artifacts checked") {
		t.Errorf("summary does not report exactly 2 artifacts:\n%s", out)
	}

	// Each artifact's presence is proved by locating its own report block, not
	// by a substring check against the whole capture: the other artifact's
	// block could satisfy a page-wide Contains without this one being reported
	// at all. artifactBlock fails the test when no block starts with the path.
	debBlock := artifactBlock(t, out, deb)
	if !strings.Contains(debBlock, "packaging/extract: dpkg-deb: ") {
		t.Errorf("the %s block does not name the deb backend's tool:\n%s", deb, debBlock)
	}

	if strings.Contains(debBlock, "packaging/extract: rpm") {
		t.Errorf("the %s block names an rpm-family tool:\n%s", deb, debBlock)
	}

	rpmBlock := artifactBlock(t, out, rpm)
	if !strings.Contains(rpmBlock, "packaging/extract: rpm") {
		t.Errorf("the %s block does not name an rpm-family tool:\n%s", rpm, rpmBlock)
	}

	if strings.Contains(rpmBlock, "packaging/extract: dpkg-deb") {
		t.Errorf("the %s block names the deb backend's tool:\n%s", rpm, rpmBlock)
	}
}

// TestRunReportsDiscoveredArtifactsInGlobOrder pins the order discovery
// produces: each glob sorted, the deb glob before the rpm glob, because
// deps.backends is an ordered slice. Every other output assertion in this
// package depends on that order being fixed rather than incidental.
func TestRunReportsDiscoveredArtifactsInGlobOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeArtifact(t, dir, "z.deb")
	writeArtifact(t, dir, "a.deb")
	writeArtifact(t, dir, "b.rpm")

	code, stdout, stderr := execRun(t, defaultDeps(), "-dir", dir)
	if code != 1 {
		t.Fatalf("exit status = %d, want 1", code)
	}

	out := stdout + stderr

	want := []string{
		filepath.Join(dir, "a.deb"),
		filepath.Join(dir, "z.deb"),
		filepath.Join(dir, "b.rpm"),
	}

	previous := -1

	for _, path := range want {
		at := strings.Index(out, path)
		if at < 0 {
			t.Fatalf("output does not report %s:\n%s", path, out)
		}

		if at <= previous {
			t.Fatalf("artifacts are not reported in the order %v:\n%s", want, out)
		}

		previous = at
	}
}

// TestRunRejectsAnUnsupportedExtension proves an extension no backend claims is
// an error naming the path, not a silently skipped file and not a finding.
func TestRunRejectsAnUnsupportedExtension(t *testing.T) {
	t.Parallel()

	path := writeArtifact(t, t.TempDir(), "x.tar.gz")

	code, stdout, stderr := execRun(t, defaultDeps(), path)
	if code != 1 {
		t.Fatalf("exit status = %d, want 1", code)
	}

	out := stdout + stderr

	if !strings.Contains(out, path) {
		t.Errorf("output does not name %s:\n%s", path, out)
	}

	if got := findingLines(out); got != 0 {
		t.Errorf("finding lines = %d, want 0:\n%s", got, out)
	}
}

// TestRunReportsAMissingArtifactPath covers both backends: an explicit path
// that does not exist is an error naming it, in either format, and produces no
// findings. The path appears because the command puts it there, so the
// assertion does not depend on whether the backend's tool is installed.
func TestRunReportsAMissingArtifactPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"absent.deb", "absent.rpm"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), name)

			code, stdout, stderr := execRun(t, defaultDeps(), path)
			if code != 1 {
				t.Fatalf("exit status = %d, want 1", code)
			}

			out := stdout + stderr

			if !strings.Contains(out, path) {
				t.Errorf("output does not name %s:\n%s", path, out)
			}

			if got := findingLines(out); got != 0 {
				t.Errorf("finding lines = %d, want 0:\n%s", got, out)
			}
		})
	}
}

// stubBackend is the injected extraction seam: it records every path it is
// handed and returns whatever the table declared for that path.
//
// The recording is what makes the clean rows meaningful. Without it a run that
// reported success while never invoking a backend at all would pass them.
type stubBackend struct {
	calls  []string
	models map[string]packaging.Model
	errs   map[string]error
}

func (s *stubBackend) extract(_ context.Context, path string) (packaging.Model, error) {
	s.calls = append(s.calls, path)

	if err, ok := s.errs[path]; ok {
		return packaging.Model{}, err
	}

	return s.models[path], nil
}

// placeholderModel is a hand-built model with the same /opt/phx placeholder
// destinations this phase's fixtures use. It carries no contract knowledge: it
// is not a conforming package and is not meant to be one — the injected verify
// decides what is reported about it.
func placeholderModel(format packaging.Format) packaging.Model {
	return packaging.Model{
		Format: format,
		Entries: []packaging.Entry{
			{Dest: "/opt/phx", Mode: 0o755},
			{Dest: "/opt/phx/notes.txt", Mode: 0o644, Content: []byte("placeholder")},
		},
		Dependencies: []string{"placeholder-dependency"},
		Postinstall:  &packaging.Scriptlet{Content: []byte("#!/bin/sh\nexit 0")},
	}
}

// TestRunExitStatusIsDerivedFromResults drives run itself through the outcomes
// that decide its return value, with the extraction and verification results
// injected.
//
// Injection is confined to this test, and to these rows, for one reason: exit 0
// is correct only for a package that satisfies the artifact contract, and no
// such package can be built here — a synthetic one would have to restate the
// contract table inside this file, which is exactly what the extraction phase
// forbids. So the clean path is unreachable with a real artifact, and asserting
// it one level down on a reporting helper would leave run's own return value
// asserted only on failures, where a run that returned 1 unconditionally would
// pass. The rows below fail such an implementation, and rows 3 and 4 fail one
// that always returns 0.
//
// The doubles know nothing about the contract: the models are placeholders and
// the findings are hand-written, with invented codes that are deliberately none
// of the real vocabulary.
func TestRunExitStatusIsDerivedFromResults(t *testing.T) {
	t.Parallel()

	const (
		cleanDeb = "/opt/dist/placeholder-clean.deb"
		cleanRPM = "/opt/dist/placeholder-clean.rpm"
		firstDeb = "/opt/dist/placeholder-first.deb"
		nextDeb  = "/opt/dist/placeholder-next.deb"
	)

	noFindings := func(packaging.Model) []packaging.Finding { return nil }

	twoFindings := func(packaging.Model) []packaging.Finding {
		return []packaging.Finding{
			{
				Code:    "placeholder_scoped",
				Path:    "/opt/phx/notes.txt",
				Message: "placeholder finding about a destination",
			},
			{
				Code:    "placeholder_unscoped",
				Message: "placeholder finding about nothing path-scoped",
			},
		}
	}

	extractionFailure := errors.New("placeholder extraction failure")

	tests := []struct {
		name      string
		args      []string
		models    map[string]packaging.Model
		errs      map[string]error
		verify    func(packaging.Model) []packaging.Finding
		wantExit  int
		wantCalls []string
		check     func(t *testing.T, out string)
	}{
		{
			name:      "a clean deb exits zero",
			args:      []string{cleanDeb},
			models:    map[string]packaging.Model{cleanDeb: placeholderModel(packaging.FormatDeb)},
			verify:    noFindings,
			wantExit:  0,
			wantCalls: []string{cleanDeb},
			check: func(t *testing.T, out string) {
				t.Helper()

				block := artifactBlock(t, out, cleanDeb)
				if !strings.Contains(block, "(deb): 0 findings") {
					t.Errorf("the %s block is not labeled deb with no findings:\n%s", cleanDeb, block)
				}

				if !strings.Contains(out, "1 artifact checked, 0 findings, 0 extraction failures") {
					t.Errorf("no zero-finding summary line:\n%s", out)
				}

				if strings.Contains(out, "extraction failed") {
					t.Errorf("output reports an extraction failure:\n%s", out)
				}
			},
		},
		{
			name:      "a clean rpm exits zero",
			args:      []string{cleanRPM},
			models:    map[string]packaging.Model{cleanRPM: placeholderModel(packaging.FormatRPM)},
			verify:    noFindings,
			wantExit:  0,
			wantCalls: []string{cleanRPM},
			check: func(t *testing.T, out string) {
				t.Helper()

				block := artifactBlock(t, out, cleanRPM)
				if !strings.Contains(block, "(rpm): 0 findings") {
					t.Errorf("the %s block is not labeled rpm with no findings:\n%s", cleanRPM, block)
				}

				if !strings.Contains(out, "1 artifact checked, 0 findings, 0 extraction failures") {
					t.Errorf("no zero-finding summary line:\n%s", out)
				}

				if strings.Contains(out, "extraction failed") {
					t.Errorf("output reports an extraction failure:\n%s", out)
				}
			},
		},
		{
			name:      "findings exit one and render code, path and message",
			args:      []string{cleanDeb},
			models:    map[string]packaging.Model{cleanDeb: placeholderModel(packaging.FormatDeb)},
			verify:    twoFindings,
			wantExit:  1,
			wantCalls: []string{cleanDeb},
			check: func(t *testing.T, out string) {
				t.Helper()

				block := artifactBlock(t, out, cleanDeb)
				if !strings.Contains(block, "(deb): 2 findings") {
					t.Errorf("the %s block does not report 2 findings:\n%s", cleanDeb, block)
				}

				want := []string{
					"\tplaceholder_scoped\t/opt/phx/notes.txt\tplaceholder finding about a destination",
					"\tplaceholder_unscoped\t-\tplaceholder finding about nothing path-scoped",
				}
				for _, line := range want {
					if !strings.Contains(block, line) {
						t.Errorf("the %s block does not render %q:\n%s", cleanDeb, line, block)
					}
				}
			},
		},
		{
			name:   "an extraction failure exits one and does not stop the run",
			args:   []string{firstDeb, nextDeb},
			models: map[string]packaging.Model{nextDeb: placeholderModel(packaging.FormatDeb)},
			errs:   map[string]error{firstDeb: extractionFailure},
			verify: noFindings,

			wantExit:  1,
			wantCalls: []string{firstDeb, nextDeb},
			check: func(t *testing.T, out string) {
				t.Helper()

				first := artifactBlock(t, out, firstDeb)
				if !strings.Contains(first, "extraction failed: placeholder extraction failure") {
					t.Errorf("the %s block does not report its failure:\n%s", firstDeb, first)
				}

				next := artifactBlock(t, out, nextDeb)
				if !strings.Contains(next, "(deb): 0 findings") {
					t.Errorf("the artifact after a failure was not processed:\n%s", out)
				}

				if !strings.Contains(out, "2 artifacts checked, 0 findings, 1 extraction failure") {
					t.Errorf("summary does not account for both artifacts:\n%s", out)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubBackend{models: tc.models, errs: tc.errs}

			injected := deps{
				backends: []backend{
					{ext: ".deb", label: "deb", extract: stub.extract},
					{ext: ".rpm", label: "rpm", extract: stub.extract},
				},
				verify: tc.verify,
			}

			code, stdout, stderr := execRun(t, injected, tc.args...)
			if code != tc.wantExit {
				t.Fatalf("exit status = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.wantExit, stdout, stderr)
			}

			if strings.Join(stub.calls, "\n") != strings.Join(tc.wantCalls, "\n") {
				t.Fatalf("backend received %v, want %v", stub.calls, tc.wantCalls)
			}

			tc.check(t, stdout+stderr)
		})
	}
}
