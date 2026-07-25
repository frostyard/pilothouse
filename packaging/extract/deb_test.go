package extract_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// degenerateSpec returns the fixture with every optional piece of metadata
// left out: nothing marked Config (so BuildDeb writes no DEBIAN/conffiles
// member at all), no Depends (so DEBIAN/control carries no such field), and
// whatever postinstall state the caller asks for. One of its files is named
// like a configuration file but is deliberately NOT marked Config, so an
// extractor that guessed Config from a path instead of reading conffiles would
// fail rather than pass by luck.
func degenerateSpec(postinstall *string) packagingtest.Spec {
	return packagingtest.Spec{
		Name:    "extractdegenerate",
		Version: "0.1.0",
		Dirs: []packagingtest.Dir{
			{Dest: "/opt/phx/data", Mode: 0o750},
		},
		Files: []packagingtest.File{
			{Dest: "/opt/phx/plain.txt", Mode: 0o644, Content: []byte("plain\n")},
			{Dest: "/opt/phx/etc/other.conf", Mode: 0o640, Content: []byte("key = value\n")},
		},
		Postinstall: postinstall,
	}
}

// degenerateDests is the complete destination set degenerateSpec produces,
// hand-written: the two declared files, the declared directory, and the
// intermediate directories dpkg-deb synthesizes for them. "/" is excluded,
// because the payload tree root is not an entry.
var degenerateDests = []string{
	"/opt",
	"/opt/phx",
	"/opt/phx/data",
	"/opt/phx/etc",
	"/opt/phx/etc/other.conf",
	"/opt/phx/plain.txt",
}

// TestDebOnFixtureWithoutOptionalMetadata covers three degenerate rows at once
// on one fixture, each with its own assertion: a package that ships no
// postinstall yields a nil Postinstall, a package that marks nothing a
// configuration file yields Config false on every entry, and a package
// declaring no dependencies yields no dependencies AND no error.
//
// The last of those is the measured one: `dpkg-deb -f <deb> Depends` on a
// package without that field exits 0 with empty output, so absence must not be
// reported as a tool failure.
func TestDebOnFixtureWithoutOptionalMetadata(t *testing.T) {
	deb := packagingtest.BuildDeb(t, t.TempDir(), degenerateSpec(nil))

	model, err := extract.Deb(t.Context(), deb)
	if err != nil {
		t.Fatalf("extract.Deb(%s) failed for a fixture declaring no Depends field, no config file and no postinstall; `dpkg-deb -f` exits 0 with empty output for a missing field, so absence must not be an error: %v", deb, err)
	}

	// The load-bearing half of the nil-vs-empty distinction: shipping no
	// scriptlet must stay distinguishable from shipping an empty one.
	if model.Postinstall != nil {
		t.Errorf("Postinstall = &{%q}, want nil for a fixture that ships no postinst member", model.Postinstall.Content)
	}

	// A nil slice and an empty one both satisfy this; what matters is that no
	// dependency was invented and that err above was nil.
	if len(model.Dependencies) != 0 {
		t.Errorf("Dependencies = %q, want none for a fixture declaring no Depends field", model.Dependencies)
	}

	// The destination set is pinned in both directions first, so the Config
	// sweep below cannot pass vacuously over an empty or truncated entry list.
	got := make(map[string]packaging.Entry, len(model.Entries))

	for _, entry := range model.Entries {
		got[entry.Dest] = entry
	}

	for _, dest := range degenerateDests {
		if _, ok := got[dest]; !ok {
			t.Errorf("Entries is missing destination %q", dest)
		}
	}

	for dest := range got {
		if !slices.Contains(degenerateDests, dest) {
			t.Errorf("Entries contains unexpected destination %q", dest)
		}
	}

	// With no conffiles control member, nothing is a configuration file — not
	// even the entry whose name ends in .conf.
	for _, entry := range model.Entries {
		if entry.Config {
			t.Errorf("%s: Config = true, want false for a fixture with no conffiles member", entry.Dest)
		}
	}
}

// TestDebOnFixtureWithEmptyPostinstall is the other side of that distinction,
// and is a separate test from the nil case on purpose: a fixture that ships a
// zero-byte postinst member must come back as a non-nil Scriptlet with empty
// Content. An extractor that collapsed "ships none" into "ships an empty one"
// would pass the nil test and fail here.
func TestDebOnFixtureWithEmptyPostinstall(t *testing.T) {
	empty := ""

	deb := packagingtest.BuildDeb(t, t.TempDir(), degenerateSpec(&empty))

	model, err := extract.Deb(t.Context(), deb)
	if err != nil {
		t.Fatalf("extract.Deb(%s): %v", deb, err)
	}

	if model.Postinstall == nil {
		t.Fatalf("Postinstall = nil, want a non-nil Scriptlet for a fixture shipping a zero-byte postinst member")
	}

	if len(model.Postinstall.Content) != 0 {
		t.Errorf("Postinstall.Content = %q, want empty", model.Postinstall.Content)
	}
}

// TestDebOnMalformedConffilesFails covers the row an ordinary builder cannot
// produce: a conffiles control member holding a line that is not an absolute
// path. dpkg-deb's own `--build` rejects such a tree, so the fixture is packed
// with packagingtest.BuildDebRaw, which passes --nocheck.
//
// Malformed external metadata must be an error rather than defaulting to "not a
// configuration file": a package whose config flags were silently dropped would
// otherwise come back as a confident model.
func TestDebOnMalformedConffilesFails(t *testing.T) {
	const (
		control   = "Package: extractmalformed\nVersion: 0.0.1\nArchitecture: all\nMaintainer: packagingtest <packagingtest@example.invalid>\nDescription: fixture whose conffiles member is malformed\n"
		conffiles = "opt/phx/relative.conf\n"
	)

	deb := packagingtest.BuildDebRaw(t, t.TempDir(),
		map[string][]byte{
			"DEBIAN/control":        []byte(control),
			"DEBIAN/conffiles":      []byte(conffiles),
			"opt/phx/relative.conf": []byte("key = value\n"),
		},
		map[string]fs.FileMode{
			"opt/phx/relative.conf": 0o640,
			"opt/phx":               0o750,
		},
	)

	model, err := extract.Deb(t.Context(), deb)
	if err == nil {
		t.Fatalf("extract.Deb(%s) returned no error for a conffiles line that is not an absolute path", deb)
	}

	if !reflect.DeepEqual(model, packaging.Model{}) {
		t.Errorf("model = %+v, want the zero value", model)
	}

	// The offending line has to reach the message, and the failure has to be
	// attributable to the parse rather than to a missing tool: dpkg-deb ran
	// here, and it succeeded.
	if want := strings.TrimSuffix(conffiles, "\n"); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the offending line %q", err, want)
	}

	if errors.Is(err, extract.ErrToolUnavailable) {
		t.Errorf("error %q reports ErrToolUnavailable, but dpkg-deb ran and the conffiles member is what was rejected", err)
	}
}

// TestDebOnUnreadableArtifact pins the error-text contract on the two ways an
// artifact can be unreadable, and pins the model each returns.
//
// Both rows are cases where the tool ran and rejected the file, so neither may
// report a missing tool, and neither may come back as a confidently empty
// model: a zero-value Model that verified as a pile of absent paths would be
// indistinguishable from a genuinely broken package.
func TestDebOnUnreadableArtifact(t *testing.T) {
	if packagingtest.LookTool(t, "dpkg-deb") == "" {
		return
	}

	cases := []struct {
		name string
		// write is nil for the row whose artifact must not exist.
		write []byte
	}{
		{
			name:  "path does not exist",
			write: nil,
		},
		{
			// Arbitrary bytes carrying none of ar's "!<arch>\n" magic. They are
			// a fixed literal rather than drawn from a random source, so a
			// failure here reproduces byte for byte on the next run.
			name:  "not an ar archive",
			write: []byte("\x00\x01\x02 these bytes are not a Debian archive \xff\xfe\n"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact := filepath.Join(t.TempDir(), "artifact.deb")

			if tc.write != nil {
				if err := os.WriteFile(artifact, tc.write, 0o644); err != nil {
					t.Fatalf("write %s: %v", artifact, err)
				}
			}

			model, err := extract.Deb(t.Context(), artifact)
			if err == nil {
				t.Fatalf("extract.Deb(%s) returned no error, want a failure", artifact)
			}

			if !reflect.DeepEqual(model, packaging.Model{}) {
				t.Errorf("model = %+v, want the zero value", model)
			}

			if !strings.Contains(err.Error(), artifact) {
				t.Errorf("error %q does not name the artifact %q", err, artifact)
			}

			// runTool adds this prefix to every tool failure unconditionally, so
			// both rows carry it. It is the token no artifact filename can
			// forge.
			if !strings.Contains(err.Error(), toolErrorPrefix) {
				t.Errorf("error %q does not contain %q", err, toolErrorPrefix)
			}

			if errors.Is(err, extract.ErrToolUnavailable) {
				t.Errorf("error %q reports ErrToolUnavailable, but dpkg-deb ran and rejected the artifact", err)
			}
		})
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
