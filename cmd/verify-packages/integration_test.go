package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/packagingtest"
)

// The real-artifact half of this command's tests. Every test in this file
// builds an actual .deb or .rpm with internal/packagingtest and drives
// run(ctx, defaultDeps(), …) — the production wiring — so what is proven here
// is the whole boundary end to end: a real artifact on disk goes in,
// extract.Deb or extract.RPM turns it into a packaging.Model, packaging.Verify
// decides what is wrong with it, and the command prints that verdict and exits
// non-zero. No test here injects a backend or a verification result; that is
// confined to main_test.go's exit-semantics table, and only for the outcomes a
// real artifact cannot produce here
// (docs/agents/skills/exercise-the-actual-boundary-not-a-precomputed-shim.md).
//
// Every fixture is a placeholder package, so it cannot satisfy #66's artifact
// contract and Verify always has something to say about it. That is the point:
// the findings printed are real Verify output, not a rehearsal of it. Nothing
// below asserts the WORDING of any finding's text — the contract assertions
// belong to packaging's own tests. What is asserted is the per-artifact block
// structure and that each printed code is one of packaging's nine, checked
// against a literal written out by hand here rather than read from packaging,
// so an invented code fails.
//
// Unlike main_test.go, these tests need external tools, and every one of them
// is resolved through packagingtest.LookTool before any fixture is built. On a
// host missing a tool the test skips naming it; inside the development image,
// which sets PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1, the same call fails instead,
// so a green `make docker-ci` is itself proof these tests ran.

// debTools are the external tools a real .deb cell needs: dpkg-deb builds the
// fixture and dpkg-deb reads it back in packaging/extract.
var debTools = []string{"dpkg-deb"}

// rpmTools are the external tools a real .rpm cell needs: rpmbuild builds the
// fixture, and rpm, rpm2cpio and cpio are what packaging/extract's rpm backend
// runs. All four are resolved up front so the skip names the tool that is
// actually missing rather than failing later inside the extractor.
var rpmTools = []string{"rpmbuild", "rpm", "rpm2cpio", "cpio"}

// closedCodes is packaging's finding vocabulary, written out by hand.
//
// It is a literal rather than a slice read from the packaging package on
// purpose: a membership check against packaging's own constants would pass for
// any code that package chose to add, while this one fails the moment a run
// prints something outside the nine.
var closedCodes = map[string]struct{}{
	"missing_path":        {},
	"wrong_mode":          {},
	"wrong_content":       {},
	"missing_config_flag": {},
	"dependency_mismatch": {},
	"forbidden_path":      {},
	"duplicate_entry":     {},
	"missing_scriptlet":   {},
	"unknown_format":      {},
}

// requireTools resolves each tool through packagingtest.LookTool, which skips
// the calling test on a host without it and fails where
// PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1 declares the tools present.
func requireTools(t *testing.T, tools ...string) {
	t.Helper()

	for _, tool := range tools {
		if packagingtest.LookTool(t, tool) == "" {
			return
		}
	}
}

// fixtureSpec declares a throwaway package with placeholder destinations.
//
// Its Depends entries are plain names, carrying neither a Debian alternative
// (`alpha | beta`) nor a version constraint (`gamma >= 1`): one Spec feeds both
// builders, each writes the strings into its own format's metadata verbatim,
// and either syntax would be recorded by the other format as a dependency whose
// literal name is the whole string.
//
// No payload file is called notes.txt, because the mixed-directory cell stages
// a decoy by that name and asserts it is named nowhere in the output.
func fixtureSpec(name string) packagingtest.Spec {
	postinstall := "#!/bin/sh\nexit 0"

	return packagingtest.Spec{
		Name:    name,
		Version: "1.0.0",
		Dirs: []packagingtest.Dir{
			{Dest: "/opt/phx/data", Mode: 0o750},
		},
		Files: []packagingtest.File{
			{
				Dest:    "/opt/phx/etc/fixture.conf",
				Mode:    0o640,
				Content: []byte("key = value\n"),
				Config:  true,
			},
			{
				Dest:    "/opt/phx/share/payload.txt",
				Mode:    0o644,
				Content: []byte("plain fixture payload\n"),
			},
		},
		Depends:     []string{"alpha", "gamma"},
		Postinstall: &postinstall,
	}
}

// assertVerifiedBlock asserts everything a real artifact's block must show: it
// names the file, carries that artifact's format label, lists at least one
// finding, reports no extraction failure, and prints only codes from the closed
// nine. It returns the block so a caller can add its own assertions to it.
//
// Every assertion is scoped to the block, never to the whole capture, so a run
// reporting several artifacts cannot satisfy one artifact's assertion with
// another's output
// (docs/agents/skills/scope-html-assertions-to-the-region-under-test.md).
func assertVerifiedBlock(t *testing.T, out, path, label string) string {
	t.Helper()

	block := artifactBlock(t, out, path)

	if !strings.Contains(block, "("+label+"): ") {
		t.Errorf("the %s block is not labeled %s:\n%s", path, label, block)
	}

	if strings.Contains(block, "extraction failed") {
		t.Errorf("the %s block reports an extraction failure:\n%s", path, block)
	}

	if got := findingLines(block); got < 1 {
		t.Errorf("the %s block lists %d finding lines, want at least 1:\n%s", path, got, block)
	}

	assertClosedCodes(t, path, block)

	return block
}

// assertClosedCodes checks the Code column of every finding line in block
// against closedCodes.
func assertClosedCodes(t *testing.T, path, block string) {
	t.Helper()

	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(line, "\t") {
			continue
		}

		// The line is "\t<code>\t<path>\t<message>", so the code is the field
		// after the leading empty one.
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("the %s block has an unparsable finding line %q", path, line)
		}

		if _, ok := closedCodes[fields[1]]; !ok {
			t.Errorf("the %s block prints code %q, which is none of packaging's nine:\n%s", path, fields[1], block)
		}
	}
}

// artifactHeaderLines returns the report's per-artifact lines: everything that
// is neither an indented finding line nor a line the command emits on its own
// behalf. Counting them is how a test asserts how many artifacts a run
// reported, independently of the summary line's own arithmetic
// (docs/agents/skills/dont-use-the-gate-under-test-as-the-test-oracle.md).
func artifactHeaderLines(out string) []string {
	var headers []string

	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, toolName+":") {
			continue
		}

		headers = append(headers, line)
	}

	return headers
}

// TestRunVerifiesARealDeb drives a real synthetic .deb through the production
// wiring as an explicit positional argument: extract.Deb reads it and
// packaging.Verify judges it, so the block carries real findings and no
// extraction failure.
func TestRunVerifiesARealDeb(t *testing.T) {
	t.Parallel()

	requireTools(t, debTools...)

	artifact := packagingtest.BuildDeb(t, t.TempDir(), fixtureSpec("verifypackagesdeb"))

	code, stdout, stderr := execRun(t, defaultDeps(), artifact)
	if code != 1 {
		t.Fatalf("exit status = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	assertVerifiedBlock(t, stdout+stderr, artifact, "deb")
}

// TestRunVerifiesARealRPM is the concrete rpm dispatch proof. A real synthetic
// .rpm passed as a positional argument must reach extract.RPM: an
// implementation routing it to the Debian extractor produces an extraction
// failure naming dpkg-deb in place of a findings block, and fails both the
// no-failure assertion and the no-dpkg-deb one.
func TestRunVerifiesARealRPM(t *testing.T) {
	t.Parallel()

	requireTools(t, rpmTools...)

	artifact := packagingtest.BuildRPM(t, t.TempDir(), fixtureSpec("verifypackagesrpm"))

	code, stdout, stderr := execRun(t, defaultDeps(), artifact)
	if code != 1 {
		t.Fatalf("exit status = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	block := assertVerifiedBlock(t, stdout+stderr, artifact, "rpm")
	if strings.Contains(block, "dpkg-deb") {
		t.Errorf("the %s block names the deb backend's tool:\n%s", artifact, block)
	}
}

// TestRunDiscoversAndVerifiesAMixedDirectory is the discovery cell over real
// artifacts: one .deb and one .rpm built from a SINGLE Spec into one directory,
// beside a decoy that is not an artifact. `run` with -dir and no positional
// arguments must find exactly the two packages, dispatch each to its own
// backend, and never treat the decoy as an artifact.
func TestRunDiscoversAndVerifiesAMixedDirectory(t *testing.T) {
	t.Parallel()

	requireTools(t, debTools...)
	requireTools(t, rpmTools...)

	dir := t.TempDir()

	// One declaration, both builders — which is why its dependencies are plain
	// names; see fixtureSpec.
	spec := fixtureSpec("verifypackagesmixed")

	deb := packagingtest.BuildDeb(t, dir, spec)
	rpm := packagingtest.BuildRPM(t, dir, spec)

	decoy := writeArtifact(t, dir, "notes.txt")

	code, stdout, stderr := execRun(t, defaultDeps(), "-dir", dir)
	if code != 1 {
		t.Fatalf("exit status = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	out := stdout + stderr

	if headers := artifactHeaderLines(out); len(headers) != 2 {
		t.Fatalf("run reported %d artifacts, want exactly 2: %q\n%s", len(headers), headers, out)
	}

	debBlock := assertVerifiedBlock(t, out, deb, "deb")
	if strings.Contains(debBlock, "(rpm): ") {
		t.Errorf("the %s block is labeled rpm:\n%s", deb, debBlock)
	}

	rpmBlock := assertVerifiedBlock(t, out, rpm, "rpm")
	if strings.Contains(rpmBlock, "dpkg-deb") {
		t.Errorf("the %s block names the deb backend's tool:\n%s", rpm, rpmBlock)
	}

	// The decoy is asserted by base name, not by full path: the directory is a
	// temporary one whose name appears in every artifact line, so only the file
	// name distinguishes "the decoy was reported" from "its directory was".
	if strings.Contains(out, filepath.Base(decoy)) {
		t.Errorf("output names the non-artifact %s:\n%s", decoy, out)
	}
}
