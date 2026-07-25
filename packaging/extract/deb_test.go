package extract_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/packagingtest"
	"github.com/frostyard/pilothouse/packaging"
	"github.com/frostyard/pilothouse/packaging/extract"
)

// toolErrorPrefix is the literal prefix every error caused by an external tool
// carries. It is written out by hand here rather than read from the extractor,
// because it is a contract this test exists to pin: a caller must be able to
// recognise which tool failed from a token no artifact filename can forge.
const toolErrorPrefix = "packaging/extract: dpkg-deb: "

// fixturePostinst is the postinstall body the fixture ships. It is declared
// once and used both to build the fixture and as the expected bytes.
const fixturePostinst = "#!/bin/sh\necho fixture postinstall\n"

// TestDebMapsFixtureOntoModel builds a throwaway .deb and asserts every field
// of the model extract.Deb returns against hand-written expectations derived
// from the spec declared right here.
//
// Every expected value below is a literal. None of them is produced by calling
// extract.Deb, by the parent packaging package, or by re-running the fixture
// builder's own path-synthesis logic — an expectation computed by the code
// under test proves nothing.
func TestDebMapsFixtureOntoModel(t *testing.T) {
	postinst := fixturePostinst

	// The spec is deliberately rich enough that no assertion below is
	// vacuous: a config file AND non-config files, a /usr/bin file AND files
	// outside it, directories at three different modes, and three dependency
	// expressions of three different shapes.
	spec := packagingtest.Spec{
		Name:    "extractfixture",
		Version: "1.0.0",
		Dirs: []packagingtest.Dir{
			{Dest: "/opt/phx/data", Mode: 0o750},
			{Dest: "/opt/phx/secrets", Mode: 0o700},
		},
		Files: []packagingtest.File{
			{
				Dest:    "/opt/phx/etc/fixture.conf",
				Mode:    0o640,
				Content: []byte("key = value\n"),
				Config:  true,
			},
			{
				Dest:    "/opt/phx/share/notes.txt",
				Mode:    0o644,
				Content: []byte("plain fixture payload\n"),
			},
			{
				Dest:    "/usr/bin/phx-fixture",
				Mode:    0o755,
				Content: []byte("binary-ish fixture bytes\n"),
			},
		},
		Depends:     []string{"alpha | beta", "gamma (>= 1)", "delta"},
		Postinstall: &postinst,
	}

	deb := packagingtest.BuildDeb(t, t.TempDir(), spec)

	model, err := extract.Deb(t.Context(), deb)
	if err != nil {
		t.Fatalf("extract.Deb(%s): %v", deb, err)
	}

	if model.Format != packaging.FormatDeb {
		t.Errorf("Format = %q, want %q", model.Format, packaging.FormatDeb)
	}

	// The complete destination set. It includes the intermediate directories
	// dpkg-deb synthesizes for the declared paths — /opt, /opt/phx,
	// /opt/phx/etc, /opt/phx/share, /usr and /usr/bin, none of which the spec
	// declares — and excludes "/", because the payload tree root is not an
	// entry.
	wantMode := map[string]fs.FileMode{
		"/opt":                      0o755,
		"/opt/phx":                  0o755,
		"/opt/phx/data":             0o750,
		"/opt/phx/etc":              0o755,
		"/opt/phx/etc/fixture.conf": 0o640,
		"/opt/phx/secrets":          0o700,
		"/opt/phx/share":            0o755,
		"/opt/phx/share/notes.txt":  0o644,
		"/usr":                      0o755,
		"/usr/bin":                  0o755,
		"/usr/bin/phx-fixture":      0o755,
	}

	// The directory entries, listed separately so "every directory entry has
	// nil Content" is asserted as its own statement rather than inferred.
	wantDirs := []string{
		"/opt",
		"/opt/phx",
		"/opt/phx/data",
		"/opt/phx/etc",
		"/opt/phx/secrets",
		"/opt/phx/share",
		"/usr",
		"/usr/bin",
	}

	// Content is expected only for the regular files outside /usr/bin.
	wantContent := map[string][]byte{
		"/opt/phx/etc/fixture.conf": []byte("key = value\n"),
		"/opt/phx/share/notes.txt":  []byte("plain fixture payload\n"),
	}

	const wantConfigDest = "/opt/phx/etc/fixture.conf"

	got := make(map[string]packaging.Entry, len(model.Entries))

	for _, entry := range model.Entries {
		if _, duplicate := got[entry.Dest]; duplicate {
			t.Errorf("Entries contains %q more than once", entry.Dest)
		}

		got[entry.Dest] = entry
	}

	// Set equality in BOTH directions: a one-directional membership check
	// would pass while the extractor invented "/" or dropped a synthesized
	// parent.
	for dest := range wantMode {
		if _, ok := got[dest]; !ok {
			t.Errorf("Entries is missing destination %q", dest)
		}
	}

	for dest := range got {
		if _, ok := wantMode[dest]; !ok {
			t.Errorf("Entries contains unexpected destination %q", dest)
		}
	}

	for dest, mode := range wantMode {
		entry, ok := got[dest]
		if !ok {
			continue
		}

		if entry.Mode.Perm() != mode {
			t.Errorf("%s: Mode.Perm() = %#o, want %#o", dest, entry.Mode.Perm(), mode)
		}

		wantConfig := dest == wantConfigDest
		if entry.Config != wantConfig {
			t.Errorf("%s: Config = %t, want %t", dest, entry.Config, wantConfig)
		}

		if entry.Owner != "" || entry.Group != "" {
			t.Errorf("%s: Owner/Group = %q/%q, want both empty", dest, entry.Owner, entry.Group)
		}

		if want, ok := wantContent[dest]; ok {
			if !bytes.Equal(entry.Content, want) {
				t.Errorf("%s: Content = %q, want %q", dest, entry.Content, want)
			}

			continue
		}

		if entry.Content != nil {
			t.Errorf("%s: Content = %q, want nil", dest, entry.Content)
		}
	}

	for _, dest := range wantDirs {
		if entry, ok := got[dest]; ok && entry.Content != nil {
			t.Errorf("directory %s: Content = %q, want nil", dest, entry.Content)
		}
	}

	wantDependencies := []string{"alpha | beta", "gamma (>= 1)", "delta"}
	if !reflect.DeepEqual(model.Dependencies, wantDependencies) {
		t.Errorf("Dependencies = %q, want %q", model.Dependencies, wantDependencies)
	}

	if model.Postinstall == nil {
		t.Fatalf("Postinstall = nil, want the fixture's postinst body")
	}

	if !bytes.Equal(model.Postinstall.Content, []byte(fixturePostinst)) {
		t.Errorf("Postinstall.Content = %q, want %q", model.Postinstall.Content, fixturePostinst)
	}
}

// TestDebOnNonArchiveNamesToolAndArtifact pins the error-text contract on the
// path where the tool ran and rejected the file, which is the case that must
// NOT be reported as a missing tool.
func TestDebOnNonArchiveNamesToolAndArtifact(t *testing.T) {
	if packagingtest.LookTool(t, "dpkg-deb") == "" {
		return
	}

	artifact := filepath.Join(t.TempDir(), "not-an-archive.deb")
	if err := os.WriteFile(artifact, []byte("this is not a Debian archive\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", artifact, err)
	}

	_, err := extract.Deb(t.Context(), artifact)
	if err == nil {
		t.Fatalf("extract.Deb(%s) returned no error, want a failure", artifact)
	}

	if !strings.Contains(err.Error(), toolErrorPrefix) {
		t.Errorf("error %q does not contain %q", err, toolErrorPrefix)
	}

	if !strings.Contains(err.Error(), artifact) {
		t.Errorf("error %q does not name the artifact %q", err, artifact)
	}

	if errors.Is(err, extract.ErrToolUnavailable) {
		t.Errorf("error %q reports ErrToolUnavailable, but dpkg-deb ran and rejected the artifact", err)
	}
}

// TestDebWithoutToolWrapsErrToolUnavailable exercises the missing-tool branch.
// It needs no packaging tool of its own, so it runs on every host.
func TestDebWithoutToolWrapsErrToolUnavailable(t *testing.T) {
	t.Setenv("PATH", "")

	artifact := filepath.Join(t.TempDir(), "absent.deb")

	_, err := extract.Deb(t.Context(), artifact)
	if !errors.Is(err, extract.ErrToolUnavailable) {
		t.Fatalf("extract.Deb(%s) = %v, want an error wrapping ErrToolUnavailable", artifact, err)
	}

	if !strings.HasPrefix(err.Error(), toolErrorPrefix) {
		t.Errorf("error %q does not start with %q", err, toolErrorPrefix)
	}
}
