// This file is an INTERNAL test — package extract, not extract_test — because
// the three parsers it tables are unexported: the file-table line parser, the
// dependency-pairing function and the postinstall presence-marker parser. Per
// docs/agents/skills/test-the-composing-function-not-its-merge-helper.md those
// tables SUPPLEMENT the fixture-backed tests below, which drive the same
// parsers through RPM itself against a real .rpm built by
// packagingtest.BuildRPM; they do not replace them, and no table row stands in
// for an end-to-end case a fixture can express.
//
// Every expected value in this file is a hand-written literal. None is produced
// by calling RPM, by the parent packaging package, or by re-running the fixture
// builder's own logic.
//
// The fixture-backed tests resolve every tool they need — rpmbuild, rpm, rpm2cpio
// and cpio — through packagingtest.LookTool, so on a host without them they skip
// with a message naming the missing tool and `make docker-ci`, while inside the
// dev image —
// which sets PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1 — the same call fails instead
// of skipping. The three parser tables need no tool and execute on every host.
package extract

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/packagingtest"
	"github.com/frostyard/pilothouse/packaging"
)

// rpmFixturePostinstall is the %post body the happy-path fixture declares. It
// carries NO trailing newline on purpose: rpm strips every trailing newline
// from a recorded scriptlet, so a body declared without one round-trips byte
// for byte and the assertion below can be plain equality rather than a
// normalized comparison. The extractor adds no newline back — the parent
// package's own trailing-newline allowance is what absorbs the difference for a
// real package, and duplicating it in an extractor would put a byte in the
// model that the artifact does not contain.
const rpmFixturePostinstall = "echo fixture postinstall\necho done"

// rpmHappySpec is the happy-path fixture.
//
// It is deliberately rich enough that no assertion is vacuous: a config file
// AND non-config files, a /usr/bin file AND files outside it, a 0750 directory,
// a 0700 directory and a 0640 regular file, THREE distinct owner/group pairs so
// an extractor that read one and reused it cannot pass, a plain dependency AND
// one carrying a version constraint, and a %post section — which rpm always
// answers with an extra interpreter requirement of its own.
func rpmHappySpec() packagingtest.Spec {
	postinstall := rpmFixturePostinstall

	return packagingtest.Spec{
		Name:    "extractrpmfixture",
		Version: "1.0.0",
		Dirs: []packagingtest.Dir{
			{Dest: "/opt/phx/data", Mode: 0o750, Owner: "alpha", Group: "beta"},
			{Dest: "/opt/phx/secrets", Mode: 0o700, Owner: "carol", Group: "dave"},
		},
		Files: []packagingtest.File{
			{
				Dest:    "/opt/phx/etc/fixture.conf",
				Mode:    0o640,
				Content: []byte("key = value\n"),
				Config:  true,
				Owner:   "erin",
				Group:   "frank",
			},
			{
				Dest:    "/opt/phx/share/notes.txt",
				Mode:    0o644,
				Content: []byte("plain fixture payload\n"),
				Owner:   "alpha",
				Group:   "beta",
			},
			{
				Dest:    "/usr/bin/phx-fixture",
				Mode:    0o755,
				Content: []byte("binary-ish fixture bytes\n"),
				Owner:   "alpha",
				Group:   "beta",
			},
		},
		// rpm's version-constraint syntax needs spaces around the operator.
		Depends:     []string{"alpha", "gamma >= 1"},
		Postinstall: &postinstall,
	}
}

// rpmBulkPayloadSize is the target size of the extra file rpmBulkSpec ships —
// the file is that many bytes rounded DOWN to a whole number of content blocks.
// It is far above Linux's 64 KiB default pipe buffer, and above the 1 MiB
// ceiling an unprivileged F_SETPIPE_SZ can raise one to, so a consumer that
// exits without reading cannot leave the producer's write satisfied by
// buffering alone.
const rpmBulkPayloadSize = 4 << 20

// rpmBulkSpec is rpmHappySpec plus one file large enough to fill any pipe
// buffer several times over.
//
// The content is trivially compressible, which does NOT weaken the test:
// rpm2cpio writes the cpio stream UNCOMPRESSED, so the artifact's own
// compression shrinks the file on disk while the pipe still carries
// rpmBulkPayloadSize bytes.
func rpmBulkSpec() packagingtest.Spec {
	spec := rpmHappySpec()
	spec.Name = "extractrpmbulkfixture"
	spec.Files = append(spec.Files, packagingtest.File{
		Dest:    "/opt/phx/share/bulk.dat",
		Mode:    0o644,
		Content: bytes.Repeat([]byte("pilothouse fixture payload block\n"), rpmBulkPayloadSize/33),
		Owner:   "alpha",
		Group:   "beta",
	})

	return spec
}

// rpmBogusCpioOption is an option no cpio implements. cpio rejects it and exits
// non-zero before reading a byte of its input, which is a failure no privilege
// level can turn into a success — unlike a directory made unwritable with
// chmod, which root and CAP_DAC_OVERRIDE ignore.
const rpmBogusCpioOption = "--pilothouse-not-an-option"

// rpmPipeDeadline bounds how long the pipe-failure rows may take. Measured, the
// row below returns in under a tenth of a second, so this is more than two
// orders of magnitude of headroom: exceeding it means one half is blocked
// rather than slow, and the row then reports a deadlock instead of hanging
// until the whole test binary times out. Measured against a sequential
// implementation, that row does hang and this deadline is what fails it.
const rpmPipeDeadline = 30 * time.Second

// rpmbuildTool builds the fixtures. It is named here rather than imported from
// packagingtest because that package resolves it internally and exports no name
// for it; the extractor itself never runs it.
const rpmbuildTool = "rpmbuild"

// requireRPMTools resolves every tool a fixture-backed test needs BEFORE the
// test builds anything: rpmbuild builds the artifact, and RPM then runs rpm,
// rpm2cpio and cpio over it.
//
// Resolving all four up front is what makes the skip message name the tool that
// is actually missing. A host carrying rpmbuild but no cpio would otherwise
// build a fixture, reach the extractor and fail with a tool error where a skip
// is the honest outcome. Inside the dev image, which sets
// PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1, the same call fails instead of skipping,
// so a green `make docker-ci` is proof these tests ran.
func requireRPMTools(t *testing.T) {
	t.Helper()

	for _, tool := range []string{rpmbuildTool, rpmTool, rpm2cpioTool, cpioTool} {
		if packagingtest.LookTool(t, tool) == "" {
			return
		}
	}
}

// TestRPMMapsFixtureOntoModel builds a throwaway .rpm and asserts every field
// of the model RPM returns against hand-written expectations derived from the
// spec declared right here.
func TestRPMMapsFixtureOntoModel(t *testing.T) {
	requireRPMTools(t)

	artifact := packagingtest.BuildRPM(t, t.TempDir(), rpmHappySpec())

	model, err := RPM(t.Context(), artifact)
	if err != nil {
		t.Fatalf("RPM(%s): %v", artifact, err)
	}

	if model.Format != packaging.FormatRPM {
		t.Errorf("Format = %q, want %q", model.Format, packaging.FormatRPM)
	}

	// The complete destination set, hand-written. rpm owns only what %files
	// lists, so there is NO entry for /opt, /opt/phx, /opt/phx/etc,
	// /opt/phx/share, /usr or /usr/bin — the deliberate opposite of the deb
	// backend, whose archive carries every intermediate directory.
	wantMode := map[string]fs.FileMode{
		"/opt/phx/data":             0o750,
		"/opt/phx/secrets":          0o700,
		"/opt/phx/etc/fixture.conf": 0o640,
		"/opt/phx/share/notes.txt":  0o644,
		"/usr/bin/phx-fixture":      0o755,
	}

	// The directory entries, listed separately so "every directory entry has
	// fs.ModeDir set and nil Content" is asserted as its own statement rather
	// than inferred from the mode table.
	wantDirs := []string{
		"/opt/phx/data",
		"/opt/phx/secrets",
	}

	// Ownership, per entry. Three distinct pairs: an extractor that read one
	// owner and reused it for every entry fails here, and no assertion can pass
	// against a hardcoded root.
	type ownership struct{ owner, group string }

	wantOwnership := map[string]ownership{
		"/opt/phx/data":             {"alpha", "beta"},
		"/opt/phx/secrets":          {"carol", "dave"},
		"/opt/phx/etc/fixture.conf": {"erin", "frank"},
		"/opt/phx/share/notes.txt":  {"alpha", "beta"},
		"/usr/bin/phx-fixture":      {"alpha", "beta"},
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

	// Set equality in BOTH directions: a one-directional membership check would
	// pass while the extractor invented a synthesized parent or dropped an
	// entry.
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

		want := wantOwnership[dest]
		if entry.Owner != want.owner || entry.Group != want.group {
			t.Errorf("%s: Owner/Group = %q/%q, want %q/%q", dest, entry.Owner, entry.Group, want.owner, want.group)
		}

		if wantBytes, ok := wantContent[dest]; ok {
			if !bytes.Equal(entry.Content, wantBytes) {
				t.Errorf("%s: Content = %q, want %q", dest, entry.Content, wantBytes)
			}

			continue
		}

		if entry.Content != nil {
			t.Errorf("%s: Content = %q, want nil", dest, entry.Content)
		}
	}

	for _, dest := range wantDirs {
		entry, ok := got[dest]
		if !ok {
			continue
		}

		if entry.Mode&fs.ModeDir == 0 {
			t.Errorf("directory %s: Mode = %v, want fs.ModeDir set", dest, entry.Mode)
		}

		if entry.Content != nil {
			t.Errorf("directory %s: Content = %q, want nil", dest, entry.Content)
		}
	}

	// rpm does not preserve declaration order, so the comparison is against a
	// sorted clone of a hand-written literal set.
	wantDependencies := []string{"alpha", "gamma >= 1"}

	gotDependencies := slices.Clone(model.Dependencies)
	slices.Sort(gotDependencies)
	slices.Sort(wantDependencies)

	if !reflect.DeepEqual(gotDependencies, wantDependencies) {
		t.Errorf("Dependencies (sorted) = %q, want %q", gotDependencies, wantDependencies)
	}

	// The two provenance assertions, stated separately from the set comparison
	// so each names the class it rules out. The fixture ships a %post, which
	// rpm always answers with an INTERP-flagged interpreter requirement, and
	// rpm records its payload-format capabilities on every package.
	for _, dependency := range model.Dependencies {
		if strings.HasPrefix(dependency, "rpmlib(") {
			t.Errorf("Dependencies contains the builder-written capability %q", dependency)
		}

		if dependency == "/bin/sh" {
			t.Errorf("Dependencies contains %q, which rpm derives from the fixture's %%post interpreter rather than from anything the fixture declares", dependency)
		}
	}

	if model.Postinstall == nil {
		t.Fatalf("Postinstall = nil, want the fixture's %%post body")
	}

	if !bytes.Equal(model.Postinstall.Content, []byte(rpmFixturePostinstall)) {
		t.Errorf("Postinstall.Content = %q, want %q", model.Postinstall.Content, rpmFixturePostinstall)
	}
}

// rpmHeaderLikePostinstall is a %post body whose second and third lines are
// byte-identical, at column 0, to the headers rpm's human-readable scriptlet
// report prints. Measured on rpm 4.18, that report renders them
// indistinguishably from real headers, with no escaping and no length prefix.
const rpmHeaderLikePostinstall = "echo before\n" +
	"preuninstall scriptlet (using /bin/sh):\n" +
	"postinstall program: /bin/sh\n" +
	"echo after"

// TestRPMPreservesHeaderLikeScriptletBody is the standing guard against
// reintroducing a header-anchored parse of that report.
//
// An implementation that anchored on those headers returns a TRUNCATED body for
// this fixture — and the truncated bytes can still be exactly what a caller
// expected, so a package shipping extra postinstall code would look correct.
// Reading the body out of the POSTIN header tag behind a presence marker cannot
// be fooled this way, because the marker precedes the body at a fixed position
// and rpm emits it from a tag-presence test the body cannot influence.
func TestRPMPreservesHeaderLikeScriptletBody(t *testing.T) {
	requireRPMTools(t)

	postinstall := rpmHeaderLikePostinstall

	spec := rpmHappySpec()
	spec.Name = "extractrpmheaderlike"
	spec.Postinstall = &postinstall

	artifact := packagingtest.BuildRPM(t, t.TempDir(), spec)

	model, err := RPM(t.Context(), artifact)
	if err != nil {
		t.Fatalf("RPM(%s): %v", artifact, err)
	}

	if model.Postinstall == nil {
		t.Fatalf("Postinstall = nil, want the fixture's %%post body")
	}

	if !bytes.Equal(model.Postinstall.Content, []byte(rpmHeaderLikePostinstall)) {
		t.Errorf("Postinstall.Content = %q, want %q", model.Postinstall.Content, rpmHeaderLikePostinstall)
	}
}

// TestRPMPassesRPMsTrailingNewlineBehaviourThrough pins the measured behaviour
// rather than reconciling it: rpm strips ALL trailing newlines from a recorded
// scriptlet, so a body declared without one comes back byte for byte and a body
// declared with three comes back with none. The extractor neither trims nor
// appends — it returns what rpm recorded.
func TestRPMPassesRPMsTrailingNewlineBehaviourThrough(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		declared string
		want     string
	}{
		{
			name:     "body declared without a trailing newline round-trips exactly",
			fixture:  "extractrpmnonewline",
			declared: "echo one",
			want:     "echo one",
		},
		{
			name:     "trailing newlines rpm stripped stay stripped",
			fixture:  "extractrpmtrailingnewlines",
			declared: "echo one\n\n\n",
			want:     "echo one",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireRPMTools(t)

			postinstall := tc.declared

			spec := rpmHappySpec()
			spec.Name = tc.fixture
			spec.Postinstall = &postinstall

			artifact := packagingtest.BuildRPM(t, t.TempDir(), spec)

			model, err := RPM(t.Context(), artifact)
			if err != nil {
				t.Fatalf("RPM(%s): %v", artifact, err)
			}

			if model.Postinstall == nil {
				t.Fatalf("Postinstall = nil, want the fixture's %%post body")
			}

			if !bytes.Equal(model.Postinstall.Content, []byte(tc.want)) {
				t.Errorf("Postinstall.Content = %q, want %q", model.Postinstall.Content, tc.want)
			}
		})
	}
}

// TestRPMPipeReportsFailureInEitherHalf drives the production pipe helper —
// the same one RPM extracts every payload with — and asserts that a failure in
// EITHER half surfaces as an error carrying that half's
// "packaging/extract: <tool>: " prefix. A shell pipeline would report only the
// second status; this one checks both.
//
// Every row fails for a reason the operating system gives the same answer to
// for every user — a file that is not an rpm, a directory that does not exist,
// an option cpio does not implement. None simulates a denial with chmod, which
// root and CAP_DAC_OVERRIDE ignore and which would therefore make the required
// failure a property of who ran the test.
//
// The last row is the deadlock guard. It is the case the earlier sequential
// implementation hung on forever, and the one a happy-path row cannot reach:
// the CONSUMER exits first, while the producer still has more than a pipe
// buffer left to write. See
// docs/agents/skills/piped-subprocess-pairs-need-concurrent-wait.md.
//
// The happy-path test above is the other side of the same contract: cpio writes
// its `2 blocks` progress line to standard error on SUCCESS, so a successful
// extraction proves that stderr output alone is never the verdict.
func TestRPMPipeReportsFailureInEitherHalf(t *testing.T) {
	t.Run("source half exits non-zero", func(t *testing.T) {
		if packagingtest.LookTool(t, rpm2cpioTool) == "" {
			return
		}

		if packagingtest.LookTool(t, cpioTool) == "" {
			return
		}

		artifact := filepath.Join(t.TempDir(), "not-an-rpm.rpm")
		if err := os.WriteFile(artifact, []byte("\x00\x01\x02 these bytes are not an rpm \xff\xfe\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", artifact, err)
		}

		err := rpmExtractPayload(t.Context(), artifact, t.TempDir())
		if err == nil {
			t.Fatalf("rpmExtractPayload returned no error for a file that is not an rpm")
		}

		if want := "packaging/extract: " + rpm2cpioTool + ": "; !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry the prefix %q", err, want)
		}
	})

	t.Run("destination half never starts", func(t *testing.T) {
		requireRPMTools(t)

		artifact := packagingtest.BuildRPM(t, t.TempDir(), rpmHappySpec())

		// A destination directory that does not exist, so cpio's chdir fails
		// and the SECOND half is the only one that failed. ENOENT is the same
		// answer for every user, which a mode-denied directory is not: root and
		// any process holding CAP_DAC_OVERRIDE write into one anyway, so a
		// chmod-simulated denial makes the required failure a property of the
		// runner rather than of the code (see
		// docs/agents/skills/dont-use-chmod-to-simulate-permission-denied-in-tests.md).
		missing := filepath.Join(t.TempDir(), "no-such-directory")

		err := rpmExtractPayload(t.Context(), artifact, missing)
		if err == nil {
			t.Fatalf("rpmExtractPayload returned no error when extracting into %s, which does not exist", missing)
		}

		if want := "packaging/extract: " + cpioTool + ": "; !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry the prefix %q", err, want)
		}
	})

	t.Run("destination half exits before draining a payload larger than the pipe buffer", func(t *testing.T) {
		requireRPMTools(t)

		artifact := packagingtest.BuildRPM(t, t.TempDir(), rpmBulkSpec())

		// cpio rejects the option and exits BEFORE reading a byte, which no
		// privilege level can turn into a success. rpm2cpio meanwhile has a
		// payload far larger than a pipe buffer still to write — rpm2cpio
		// writes the cpio stream UNCOMPRESSED, so the artifact's own
		// compression cannot shrink it below one. That is the shape that used
		// to hang here forever: a producer blocked on a full pipe nobody will
		// read, reaped before the consumer whose status explains it.
		done := make(chan error, 1)

		go func() {
			done <- runPipe(t.Context(), t.TempDir(),
				pipeCommand{name: rpm2cpioTool, args: []string{artifact}},
				pipeCommand{name: cpioTool, args: []string{rpmBogusCpioOption}},
			)
		}()

		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("runPipe returned no error when %s rejected %s", cpioTool, rpmBogusCpioOption)
			}

			// The consumer's status is the root cause: the producer's own
			// failure here is a broken pipe it took because the consumer left.
			if want := "packaging/extract: " + cpioTool + ": "; !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not carry the prefix %q", err, want)
			}
		case <-time.After(rpmPipeDeadline):
			t.Fatalf("runPipe did not return within %s: the producer is wedged writing into a pipe its consumer abandoned, which is the deadlock this test exists to catch",
				rpmPipeDeadline)
		}
	})
}

// TestRPMFileTableEntryParsesLine tables the unexported file-table line parser.
//
// It reaches the shapes a real rpm is awkward or impossible to make emit — a
// wrong field count, a mode that is not octal, an empty owner and group — and
// it executes on every host, with no packaging tool involved.
func TestRPMFileTableEntryParsesLine(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantKeep bool
		want     packaging.Entry
		wantErr  bool
	}{
		{
			name:     "leading-zero octal mode",
			line:     "/opt/phx/share/notes.txt\t0100644\t0\talpha\tbeta",
			wantKeep: true,
			want: packaging.Entry{
				Dest:  "/opt/phx/share/notes.txt",
				Mode:  0o644,
				Owner: "alpha",
				Group: "beta",
			},
		},
		{
			name:     "the same file without the leading zero",
			line:     "/opt/phx/share/notes.txt\t100644\t0\talpha\tbeta",
			wantKeep: true,
			want: packaging.Entry{
				Dest:  "/opt/phx/share/notes.txt",
				Mode:  0o644,
				Owner: "alpha",
				Group: "beta",
			},
		},
		{
			name:     "directory",
			line:     "/opt/phx/data\t40750\t0\tcarol\tdave",
			wantKeep: true,
			want: packaging.Entry{
				Dest:  "/opt/phx/data",
				Mode:  fs.ModeDir | 0o750,
				Owner: "carol",
				Group: "dave",
			},
		},
		{
			name:     "config flag set",
			line:     "/opt/phx/etc/fixture.conf\t100640\t1\terin\tfrank",
			wantKeep: true,
			want: packaging.Entry{
				Dest:   "/opt/phx/etc/fixture.conf",
				Mode:   0o640,
				Config: true,
				Owner:  "erin",
				Group:  "frank",
			},
		},
		{
			// Split preserves empty fields where a whitespace split would
			// collapse them, so an entry rpm recorded with no owner is
			// present-but-empty rather than a row short two fields.
			name:     "empty owner and group",
			line:     "/opt/phx/share/notes.txt\t100644\t0\t\t",
			wantKeep: true,
			want: packaging.Entry{
				Dest: "/opt/phx/share/notes.txt",
				Mode: 0o644,
			},
		},
		{
			// The measured case that breaks every whitespace-positional
			// scheme: the destination is one field, not two.
			name:     "destination containing a space",
			line:     "/opt/phx/with space.txt\t100644\t0\talpha\tbeta",
			wantKeep: true,
			want: packaging.Entry{
				Dest:  "/opt/phx/with space.txt",
				Mode:  0o644,
				Owner: "alpha",
				Group: "beta",
			},
		},
		{
			name:     "symlink is dropped",
			line:     "/opt/phx/link\t120777\t0\talpha\tbeta",
			wantKeep: false,
		},
		{
			name:    "too few fields",
			line:    "/opt/phx/share/notes.txt\t100644\t0\talpha",
			wantErr: true,
		},
		{
			name:    "too many fields",
			line:    "/opt/phx/share/notes.txt\t100644\t0\talpha\tbeta\textra",
			wantErr: true,
		},
		{
			name:    "mode is not a number at all",
			line:    "/opt/phx/share/notes.txt\tabc\t0\talpha\tbeta",
			wantErr: true,
		},
		{
			name:    "mode carries a digit that is out of range for base 8",
			line:    "/opt/phx/share/notes.txt\t0100999\t0\talpha\tbeta",
			wantErr: true,
		},
		{
			name:    "flags field is not decimal",
			line:    "/opt/phx/share/notes.txt\t100644\tnope\talpha\tbeta",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry, keep, err := rpmFileTableEntry(tc.line)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("rpmFileTableEntry(%q) = %+v, %t, nil; want an error", tc.line, entry, keep)
				}

				return
			}

			if err != nil {
				t.Fatalf("rpmFileTableEntry(%q): %v", tc.line, err)
			}

			if keep != tc.wantKeep {
				t.Fatalf("rpmFileTableEntry(%q) kept = %t, want %t", tc.line, keep, tc.wantKeep)
			}

			if !keep {
				return
			}

			if !reflect.DeepEqual(entry, tc.want) {
				t.Errorf("rpmFileTableEntry(%q) = %+v, want %+v", tc.line, entry, tc.want)
			}

			if entry.Content != nil {
				t.Errorf("rpmFileTableEntry(%q) set Content = %q; the header carries no bytes", tc.line, entry.Content)
			}
		})
	}
}

// TestRPMEntriesReadsWholeFileTable tables the whole-output cases the line
// parser cannot express: the two ways rpm says "this package owns no files",
// and a multi-line table that must keep its rows in order.
func TestRPMEntriesReadsWholeFileTable(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		want    []packaging.Entry
		wantErr bool
	}{
		{
			name:   "empty output",
			output: "",
		},
		{
			name:   "the literal none",
			output: "(none)\n",
		},
		{
			name:   "two rows",
			output: "/opt/phx/data\t40750\t0\talpha\tbeta\n/opt/phx/etc/fixture.conf\t100640\t1\terin\tfrank\n",
			want: []packaging.Entry{
				{Dest: "/opt/phx/data", Mode: fs.ModeDir | 0o750, Owner: "alpha", Group: "beta"},
				{Dest: "/opt/phx/etc/fixture.conf", Mode: 0o640, Config: true, Owner: "erin", Group: "frank"},
			},
		},
		{
			name:    "a blank line in the middle is not a row",
			output:  "/opt/phx/data\t40750\t0\talpha\tbeta\n\n",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := rpmEntries([]byte(tc.output))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("rpmEntries(%q) = %+v, nil; want an error", tc.output, entries)
				}

				return
			}

			if err != nil {
				t.Fatalf("rpmEntries(%q): %v", tc.output, err)
			}

			if len(tc.want) == 0 {
				if len(entries) != 0 {
					t.Fatalf("rpmEntries(%q) = %+v, want zero entries", tc.output, entries)
				}

				return
			}

			if !reflect.DeepEqual(entries, tc.want) {
				t.Errorf("rpmEntries(%q) = %+v, want %+v", tc.output, entries, tc.want)
			}
		})
	}
}

// TestRPMDependenciesFiltersByProvenanceFlags tables the unexported
// dependency-pairing function with the MEASURED flag words.
//
// The filter is on PROVENANCE, never on a name: flag word 0 is what rpm records
// for a dependency a packaging author declared — including one naming an
// interpreter — while 1280 (INTERP|SCRIPT_POST) is what rpmbuild derives from a
// %post section's interpreter and 16777226 (RPMLIB plus comparison bits) is a
// payload-format capability the builder writes so an older rpm refuses the
// package. This test executes on every host.
func TestRPMDependenciesFiltersByProvenanceFlags(t *testing.T) {
	cases := []struct {
		name     string
		requires string
		flags    string
		want     []string
		wantErr  bool
	}{
		{
			name:     "flag word 0 is a declared dependency and is kept",
			requires: "alpha\n",
			flags:    "0\n",
			want:     []string{"alpha"},
		},
		{
			name:     "flag word 12 is a declared dependency with comparison bits and is kept verbatim",
			requires: "gamma >= 1\n",
			flags:    "12\n",
			want:     []string{"gamma >= 1"},
		},
		{
			name:     "flag word 1280 is the scriptlet interpreter and is dropped",
			requires: "/bin/sh\n",
			flags:    "1280\n",
		},
		{
			name:     "flag word 16777226 is a builder capability and is dropped",
			requires: "rpmlib(CompressedFileNames) <= 3.0.4-1\n",
			flags:    "16777226\n",
		},
		{
			// The discriminating row: the same name appears twice with
			// different provenance. A name filter returns neither; no filter
			// returns both; only the flag filter returns the declared one.
			name:     "a declared interpreter survives while the generated one does not",
			requires: "alpha\n/bin/sh\ngamma >= 1\n/bin/sh\nrpmlib(CompressedFileNames) <= 3.0.4-1\n",
			flags:    "0\n0\n12\n1280\n16777226\n",
			want:     []string{"alpha", "/bin/sh", "gamma >= 1"},
		},
		{
			name:     "everything filtered out",
			requires: "/bin/sh\nrpmlib(CompressedFileNames) <= 3.0.4-1\n",
			flags:    "1280\n16777226\n",
		},
		{
			name:     "a flags field that is not decimal is an error",
			requires: "alpha\ngamma >= 1\n",
			flags:    "0\nnope\n",
			wantErr:  true,
		},
		{
			name:     "fewer flag words than requires lines is an error, not a truncating merge",
			requires: "alpha\ngamma >= 1\n/bin/sh\n",
			flags:    "0\n12\n",
			wantErr:  true,
		},
		{
			name:     "more flag words than requires lines is an error too",
			requires: "alpha\n",
			flags:    "0\n12\n",
			wantErr:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rpmDependencies([]byte(tc.requires), []byte(tc.flags))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("rpmDependencies = %q, nil; want an error", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("rpmDependencies: %v", err)
			}

			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("rpmDependencies = %q, want none", got)
				}

				return
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rpmDependencies = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRPMPostinstallParsesPresenceMarker tables the unexported postinstall
// parser.
//
// The load-bearing rows are the ones whose body is header-shaped or otherwise
// hostile: the parser splits on the FIRST newline and assigns everything after
// it to Content unmodified, so a body cannot influence the verdict and no byte
// of it is trimmed or added. This test executes on every host.
func TestRPMPostinstallParsesPresenceMarker(t *testing.T) {
	const hostileBody = "postinstall scriptlet (using /bin/sh):\n" +
		"\x01\x02\x7f binary-ish but NUL-free\n" +
		"echo done \n" +
		"trailing space "

	cases := []struct {
		name    string
		output  string
		wantNil bool
		want    string
		wantErr bool
	}{
		{
			name:    "present with a hostile body",
			output:  "HAS\n" + hostileBody,
			wantNil: false,
			want:    hostileBody,
		},
		{
			name:    "present with an empty body",
			output:  "HAS\n",
			wantNil: false,
			want:    "",
		},
		{
			name:    "absent",
			output:  "NONE\n",
			wantNil: true,
		},
		{
			name:    "empty output is an error, not a guess",
			output:  "",
			wantErr: true,
		},
		{
			name:    "a marker with no newline after it is an error",
			output:  "HAS",
			wantErr: true,
		},
		{
			name:    "any other marker is an error",
			output:  "MAYBE\necho one",
			wantErr: true,
		},
		{
			// The body is not consulted, so a body that happens to read like a
			// marker cannot rescue a malformed one.
			name:    "a body-shaped first line is still an unknown marker",
			output:  "echo one\nHAS\n",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rpmPostinstall([]byte(tc.output))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("rpmPostinstall(%q) = %+v, nil; want an error", tc.output, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("rpmPostinstall(%q): %v", tc.output, err)
			}

			if tc.wantNil {
				if got != nil {
					t.Fatalf("rpmPostinstall(%q) = &{%q}, want nil", tc.output, got.Content)
				}

				return
			}

			if got == nil {
				t.Fatalf("rpmPostinstall(%q) = nil, want a non-nil Scriptlet", tc.output)
			}

			if !bytes.Equal(got.Content, []byte(tc.want)) {
				t.Errorf("rpmPostinstall(%q).Content = %q, want %q", tc.output, got.Content, tc.want)
			}
		})
	}
}
