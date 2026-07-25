package packagingtest_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/packagingtest"
)

// The fixture below and the expectations it is checked against are written out
// by hand, twice: once as a packagingtest.Spec and once as the paths, names and
// bytes rpm prints. Nothing in this file derives an expected value from the code
// under test, and nothing here reaches for whichever Go code later reads these
// artifacts — the oracle is rpm's own `-qp -l`, `--requires`, `-qpc`,
// `--scripts` and `--qf` output.

// rpmFixturePostinst is the fixture's %post body. It carries NO trailing
// newline, because rpm strips every trailing newline from a recorded scriptlet
// and a body written this way is the one that round-trips byte for byte
// (TestBuildRPMPostinstallNewlineRoundTrip proves both halves of that). No line
// begins with `%`, which rpmbuild would read as the start of a new spec section.
const rpmFixturePostinst = "set -e\necho packagingtest"

// The two ownership pairs the fixture declares. The first directory declares
// neither name, so it exercises the DefaultOwner/DefaultGroup substitution; the
// second declares both, so a builder that recorded one pair everywhere fails.
const (
	rpmSecondOwner = "carol"
	rpmSecondGroup = "dave"
)

// The fixture's declared destinations, written out by hand.
const (
	rpmDirA      = "/etc/phx"
	rpmDirB      = "/etc/phx/secrets"
	rpmConfig    = "/etc/phx/phx.conf"
	rpmPlainFile = "/usr/lib/phx/notes.txt"
	rpmBinFile   = "/usr/bin/phx"
)

func rpmFixture() packagingtest.Spec {
	postinst := rpmFixturePostinst

	return packagingtest.Spec{
		Name:    "packagingtest-rpm-fixture",
		Version: "1.2.3",
		Dirs: []packagingtest.Dir{
			{Dest: rpmDirA, Mode: 0o750},
			{Dest: rpmDirB, Mode: 0o700, Owner: rpmSecondOwner, Group: rpmSecondGroup},
		},
		Files: []packagingtest.File{
			{Dest: rpmConfig, Mode: 0o640, Content: []byte("alpha = 1\n"), Config: true},
			{Dest: rpmPlainFile, Mode: 0o644, Content: []byte("notes\n")},
			{Dest: rpmBinFile, Mode: 0o755, Content: []byte("#!/bin/sh\nexit 0\n")},
		},
		// Plain names and rpm's SPACED constraint form: `Requires: gamma>=1`
		// without the spaces is parsed as a dependency whose name is the whole
		// string, not as a version constraint.
		Depends:     []string{"alpha", "gamma >= 1"},
		Postinstall: &postinst,
	}
}

// TestBuildRPMMatchesRPMOracle builds the full fixture and checks it against rpm
// itself: `-qp -l` for the owned paths, `--requires` for the dependencies,
// `-qpc` for the configuration files, `--scripts` for the postinstall body.
func TestBuildRPMMatchesRPMOracle(t *testing.T) {
	dir := t.TempDir()
	spec := rpmFixture()

	rpm := packagingtest.BuildRPM(t, dir, spec)

	if got := filepath.Dir(rpm); got != dir {
		t.Errorf("BuildRPM returned %q, want an artifact directly inside %q", rpm, dir)
	}

	if want := "packagingtest-rpm-fixture-1.2.3-1.noarch.rpm"; filepath.Base(rpm) != want {
		t.Errorf("BuildRPM returned %q, want a file named %q", rpm, want)
	}

	// rpm owns only what %files lists, so the artifact holds exactly the five
	// declared destinations — no synthesized /etc, /usr or /usr/lib. This is the
	// opposite of a deb, where dpkg-deb archives every intermediate directory.
	// Written out in sorted order, which is what sortedLines compares against.
	wantPaths := []string{rpmDirA, rpmConfig, rpmDirB, rpmBinFile, rpmPlainFile}
	if got := sortedLines(rpmQuery(t, "-qp", "-l", rpm)); !equalStrings(got, wantPaths) {
		t.Errorf("rpm -qp -l lists %q, want exactly %q", got, wantPaths)
	}

	// --requires holds more than the declared lines: a %post makes rpm require
	// /bin/sh even under AutoReqProv: no, and every package carries rpmlib()
	// entries. Both declared dependencies must nonetheless appear verbatim, the
	// spaced constraint included.
	requires := sortedLines(rpmQuery(t, "-qp", "--requires", rpm))
	for _, want := range []string{"alpha", "gamma >= 1"} {
		if !containsString(requires, want) {
			t.Errorf("rpm -qp --requires returned %q, want a line %q", requires, want)
		}
	}

	wantConfig := []string{rpmConfig}
	if got := sortedLines(rpmQuery(t, "-qpc", rpm)); !equalStrings(got, wantConfig) {
		t.Errorf("rpm -qpc lists %q, want exactly %q", got, wantConfig)
	}

	if got := rpmQuery(t, "-qp", "--scripts", rpm); !strings.Contains(got, rpmFixturePostinst) {
		t.Errorf("rpm -qp --scripts returned %q, want output containing the declared %%post body %q", got, rpmFixturePostinst)
	}
}

// TestBuildRPMRecordsPerEntryOwnership proves the %attr ownership reaches the
// header per entry: the first directory declares no names and must come back
// carrying the package's defaults, the second declares its own pair and must
// come back carrying that. A builder that recorded one pair for every entry, or
// that let the build account's own identity through, fails here.
func TestBuildRPMRecordsPerEntryOwnership(t *testing.T) {
	// The defaults are placeholders precisely so this assertion cannot pass
	// against a reader that hardcoded root, which is what a package built by an
	// unprivileged account would otherwise plausibly record.
	if packagingtest.DefaultOwner == "root" || packagingtest.DefaultGroup == "root" {
		t.Fatalf("DefaultOwner/DefaultGroup are %q/%q; neither may be root, or an ownership assertion proves nothing",
			packagingtest.DefaultOwner, packagingtest.DefaultGroup)
	}

	dir := t.TempDir()
	rpm := packagingtest.BuildRPM(t, dir, rpmFixture())

	// path -> "<user> <group>", hand-written from what the Spec declares.
	wantOwnership := map[string]string{
		rpmDirA: packagingtest.DefaultOwner + " " + packagingtest.DefaultGroup,
		rpmDirB: rpmSecondOwner + " " + rpmSecondGroup,
	}

	ownership := make(map[string]string)

	for _, line := range sortedLines(rpmQuery(t, "-qp", "--qf", "[%{FILENAMES} %{FILEUSERNAME} %{FILEGROUPNAME}\n]", rpm)) {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("ownership query returned %q, want three space-separated fields", line)
		}

		ownership[fields[0]] = fields[1] + " " + fields[2]
	}

	for path, want := range wantOwnership {
		got, ok := ownership[path]
		if !ok {
			t.Errorf("ownership query does not list %s (listed %q)", path, ownership)

			continue
		}

		if got != want {
			t.Errorf("rpm records %s as owned by %q, want %q", path, got, want)
		}
	}
}

// TestBuildRPMRejectsAnEmptyPostinstall pins the builder's one refusal. An empty
// %post builds but records no body — `--scripts` prints only `postinstall
// program: /bin/sh`, `%{POSTIN}` is `(none)` and the tag-presence marker reads
// NOPOST — so a fixture asking for one would silently become a fixture shipping
// no scriptlet at all. BuildRPM says so instead.
//
// PATH is emptied and RequireEnv unset for the duration, so a builder that ran
// the check after resolving rpmbuild would record a skip rather than a fatal.
// The absence of a skip is what proves the check never reaches the tool, which
// is why this test executes on every host.
func TestBuildRPMRejectsAnEmptyPostinstall(t *testing.T) {
	unsetRequireEnv(t)
	t.Setenv("PATH", "")

	empty := ""

	var rec recordingT

	got := packagingtest.BuildRPM(&rec, t.TempDir(), packagingtest.Spec{
		Name:        "packagingtest-rpm-empty-post",
		Version:     "0.0.1",
		Files:       []packagingtest.File{{Dest: rpmConfig, Mode: 0o640, Content: []byte("alpha = 1\n")}},
		Postinstall: &empty,
	})

	if len(rec.skips) != 0 || len(rec.fatals) != 1 {
		t.Fatalf("BuildRPM recorded %d skips and %d fatals, want 0 and 1 (skips %q, fatals %q)",
			len(rec.skips), len(rec.fatals), rec.skips, rec.fatals)
	}

	for _, want := range []string{"%post", "%{POSTIN}", "NOPOST"} {
		requireContains(t, rec.fatals[0], want)
	}

	if got != "" {
		t.Errorf("BuildRPM returned %q after refusing to build, want an empty path", got)
	}

	if rec.helpers == 0 {
		t.Error("BuildRPM did not call Helper")
	}
}

// TestBuildRPMPostinstallNewlineRoundTrip pins the measured caveat BuildRPM's
// doc comment records: rpm strips ALL trailing newlines from a recorded
// scriptlet. A body declared without one therefore comes back byte-identical,
// and a body declared with three comes back with all three gone. The oracle is
// rpm's own %{POSTIN} tag.
func TestBuildRPMPostinstallNewlineRoundTrip(t *testing.T) {
	const bare = "set -e\necho packagingtest"

	cases := []struct {
		name     string
		declared string
		want     string
	}{
		{
			name:     "no trailing newline round-trips exactly",
			declared: bare,
			want:     bare,
		},
		{
			name:     "trailing newlines are stripped",
			declared: bare + "\n\n\n",
			want:     bare,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.declared
			dir := t.TempDir()

			rpm := packagingtest.BuildRPM(t, dir, packagingtest.Spec{
				Name:        "packagingtest-rpm-postin",
				Version:     "0.0.1",
				Files:       []packagingtest.File{{Dest: rpmConfig, Mode: 0o640, Content: []byte("alpha = 1\n")}},
				Postinstall: &body,
			})

			if got := rpmQuery(t, "-qp", "--qf", "%{POSTIN}", rpm); got != tc.want {
				t.Errorf("rpm -qp --qf %%{POSTIN} returned %q for a body declared as %q, want %q", got, tc.declared, tc.want)
			}
		})
	}
}

// rpmQuery runs the oracle and returns its standard output. It resolves the
// tool through packagingtest.LookTool like every other tool-dependent path here,
// so this file never reaches a tool around the gate. Standard error is kept
// apart from the parsed output and surfaced only on failure.
func rpmQuery(t *testing.T, args ...string) string {
	t.Helper()

	tool := packagingtest.LookTool(t, "rpm")

	cmd := exec.Command(tool, args...)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rpm %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}

	return string(out)
}

// sortedLines splits rpm's output into its non-empty lines and sorts them, so a
// comparison does not depend on an ordering rpm does not promise.
func sortedLines(out string) []string {
	var lines []string

	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}

	sort.Strings(lines)

	return lines
}

func containsString(haystack []string, needle string) bool {
	for _, got := range haystack {
		if got == needle {
			return true
		}
	}

	return false
}
