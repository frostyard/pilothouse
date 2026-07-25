package packagingtest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/frostyard/pilothouse/internal/packagingtest"
)

// The fixture below and the expectations it is checked against are written out
// by hand, twice: once as a packagingtest.Spec and once as the symbolic modes
// and paths dpkg-deb prints. Nothing in this file derives an expected value
// from the code under test — the oracle is dpkg-deb's own `-c`, `-f` and `-e`
// output.

const fixturePostinst = "#!/bin/sh\nset -e\necho packagingtest\n"

func debFixture() packagingtest.Spec {
	postinst := fixturePostinst

	return packagingtest.Spec{
		Name:    "packagingtest-fixture",
		Version: "1.2.3",
		Dirs: []packagingtest.Dir{
			{Dest: "/etc/phx", Mode: 0o750},
			{Dest: "/etc/phx/secrets", Mode: 0o700},
		},
		Files: []packagingtest.File{
			{Dest: "/etc/phx/phx.conf", Mode: 0o640, Content: []byte("alpha = 1\n"), Config: true},
			{Dest: "/usr/lib/phx/notes.txt", Mode: 0o644, Content: []byte("notes\n")},
			{Dest: "/usr/bin/phx", Mode: 0o755, Content: []byte("#!/bin/sh\nexit 0\n")},
		},
		Depends:     []string{"alpha | beta", "gamma (>= 1)", "delta"},
		Postinstall: &postinst,
	}
}

// declaredModes is the hand-written path/mode table the fixture declares, in
// the symbolic form `dpkg-deb -c` renders. Directories carry the trailing
// slash dpkg-deb prints.
var declaredModes = map[string]string{
	"./etc/phx/":              "drwxr-x---",
	"./etc/phx/secrets/":      "drwx------",
	"./etc/phx/phx.conf":      "-rw-r-----",
	"./usr/lib/phx/notes.txt": "-rw-r--r--",
	"./usr/bin/phx":           "-rwxr-xr-x",
}

// synthesizedModes is the hand-written table of the parent directories
// dpkg-deb synthesizes from the declared paths. They are absent from the Spec,
// and BuildDeb chmods each of them to 0755 so their archived mode does not
// depend on the caller's umask. `./`, the staging tree's own root, is
// deliberately not listed.
var synthesizedModes = map[string]string{
	"./etc/":         "drwxr-xr-x",
	"./usr/":         "drwxr-xr-x",
	"./usr/bin/":     "drwxr-xr-x",
	"./usr/lib/":     "drwxr-xr-x",
	"./usr/lib/phx/": "drwxr-xr-x",
}

// TestBuildDebMatchesDpkgDebOracle builds the full fixture and checks it
// against dpkg-deb itself: `-c` for the path/mode table, `-f` for the
// dependency field, `-e` for the control directory.
func TestBuildDebMatchesDpkgDebOracle(t *testing.T) {
	dir := t.TempDir()
	spec := debFixture()

	deb := packagingtest.BuildDeb(t, dir, spec)

	if want := filepath.Join(dir, "packagingtest-fixture_1.2.3_all.deb"); deb != want {
		t.Fatalf("BuildDeb returned %q, want %q", deb, want)
	}

	if _, err := os.Stat(deb); err != nil {
		t.Fatalf("stat built artifact: %v", err)
	}

	contents := debContents(t, deb)

	for path, mode := range declaredModes {
		got, ok := contents[path]
		if !ok {
			t.Errorf("dpkg-deb -c does not list declared path %s", path)

			continue
		}

		if got != mode {
			t.Errorf("dpkg-deb -c lists %s with mode %s, want %s", path, got, mode)
		}
	}

	for path, mode := range synthesizedModes {
		got, ok := contents[path]
		if !ok {
			t.Errorf("dpkg-deb -c does not list synthesized parent %s", path)

			continue
		}

		if got != mode {
			t.Errorf("dpkg-deb -c lists synthesized parent %s with mode %s, want %s", path, got, mode)
		}
	}

	depends := strings.TrimSpace(string(dpkgDeb(t, "-f", deb, "Depends")))
	if want := "alpha | beta, gamma (>= 1), delta"; depends != want {
		t.Errorf("dpkg-deb -f Depends returned %q, want %q", depends, want)
	}

	control := extractControlDir(t, deb)

	if got, want := controlMembers(t, control), []string{"conffiles", "control", "postinst"}; !equalStrings(got, want) {
		t.Errorf("control directory holds %q, want %q", got, want)
	}

	conffiles := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(control, "conffiles"))), "\n")
	if want := []string{"/etc/phx/phx.conf"}; !equalStrings(conffiles, want) {
		t.Errorf("conffiles lists %q, want %q", conffiles, want)
	}

	if got := readFile(t, filepath.Join(control, "postinst")); got != fixturePostinst {
		t.Errorf("postinst holds %q, want %q", got, fixturePostinst)
	}
}

// TestBuildDebOmitsOptionalControlMembers pins the three states the optional
// control members have to reproduce: no Config file means no conffiles member
// at all, a nil Postinstall means no postinst member at all, and a Postinstall
// pointing at "" means a zero-byte postinst member.
func TestBuildDebOmitsOptionalControlMembers(t *testing.T) {
	empty := ""
	body := fixturePostinst

	cases := []struct {
		name        string
		config      bool
		postinstall *string
		want        []string
		wantPostLen int
	}{
		{
			name:        "no config file and no postinstall",
			postinstall: nil,
			want:        []string{"control"},
		},
		{
			name:        "config file but no postinstall",
			config:      true,
			postinstall: nil,
			want:        []string{"conffiles", "control"},
		},
		{
			name:        "empty postinstall still ships a member",
			postinstall: &empty,
			want:        []string{"control", "postinst"},
			wantPostLen: 0,
		},
		{
			name:        "non-empty postinstall",
			postinstall: &body,
			want:        []string{"control", "postinst"},
			wantPostLen: len(body),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			deb := packagingtest.BuildDeb(t, dir, packagingtest.Spec{
				Name:    "packagingtest-optional",
				Version: "0.0.1",
				Files: []packagingtest.File{
					{Dest: "/etc/phx/phx.conf", Mode: 0o640, Content: []byte("alpha = 1\n"), Config: tc.config},
				},
				Postinstall: tc.postinstall,
			})

			control := extractControlDir(t, deb)

			if got := controlMembers(t, control); !equalStrings(got, tc.want) {
				t.Fatalf("control directory holds %q, want %q", got, tc.want)
			}

			if tc.postinstall == nil {
				return
			}

			if got := len(readFile(t, filepath.Join(control, "postinst"))); got != tc.wantPostLen {
				t.Errorf("postinst is %d bytes, want %d", got, tc.wantPostLen)
			}
		})
	}
}

// TestBuildDebUsesATempDirTree proves the builder copes with dpkg-deb's
// control-directory permission rule. `dpkg-deb --build` rejects a control
// directory outside 0755-0775 ("control directory has bad permissions 700"),
// t.TempDir() hands back a 0700 directory under a restrictive umask, and
// BuildDeb stages inside it with os.MkdirTemp, which is 0700 unconditionally.
// A builder that let DEBIAN inherit either mode would fail here, so a clean
// build plus a 0755 DEBIAN is the proof the chmod is explicit. umask is
// process-global, so this test does not run in parallel.
func TestBuildDebUsesATempDirTree(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	dir := t.TempDir()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}

	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("t.TempDir() is mode %04o, want 0700 — this test's premise no longer holds", got)
	}

	deb := packagingtest.BuildDeb(t, dir, debFixture())

	if _, err := os.Stat(deb); err != nil {
		t.Fatalf("stat built artifact: %v", err)
	}

	staging := stagingTree(t, dir)

	if got := dirMode(t, staging); got != 0o700 {
		t.Errorf("staging tree %s is mode %04o, want 0700 — this test's premise no longer holds", staging, got)
	}

	if got := dirMode(t, filepath.Join(staging, "DEBIAN")); got != 0o755 {
		t.Errorf("staged DEBIAN is mode %04o, want 0755", got)
	}
}

// stagingTree returns the one directory BuildDeb left inside outDir.
func stagingTree(t *testing.T, outDir string) string {
	t.Helper()

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read %s: %v", outDir, err)
	}

	var dirs []string

	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(outDir, entry.Name()))
		}
	}

	if len(dirs) != 1 {
		t.Fatalf("BuildDeb left %d directories in %s, want 1: %q", len(dirs), outDir, dirs)
	}

	return dirs[0]
}

func dirMode(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	return info.Mode().Perm()
}

// TestBuildDebSurvivesARestrictiveUmask proves the declared modes come from an
// explicit chmod rather than from the permission argument of os.MkdirAll or
// os.WriteFile, which the process umask masks bits off. umask is
// process-global, so this test does not run in parallel.
func TestBuildDebSurvivesARestrictiveUmask(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	dir := t.TempDir()
	deb := packagingtest.BuildDeb(t, dir, debFixture())
	contents := debContents(t, deb)

	for path, mode := range declaredModes {
		got, ok := contents[path]
		if !ok {
			t.Errorf("dpkg-deb -c does not list declared path %s", path)

			continue
		}

		if got != mode {
			t.Errorf("under umask 0077 dpkg-deb -c lists %s with mode %s, want %s", path, got, mode)
		}
	}

	for path, mode := range synthesizedModes {
		if got := contents[path]; got != mode {
			t.Errorf("under umask 0077 dpkg-deb -c lists synthesized parent %s with mode %s, want %s", path, got, mode)
		}
	}
}

// debContents maps each archived path to the symbolic mode `dpkg-deb -c`
// prints for it.
func debContents(t *testing.T, deb string) map[string]string {
	t.Helper()

	contents := make(map[string]string)

	for _, line := range strings.Split(string(dpkgDeb(t, "-c", deb)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		contents[fields[len(fields)-1]] = fields[0]
	}

	return contents
}

// extractControlDir runs `dpkg-deb -e`, which writes the control files into a
// directory and emits no archive stream.
func extractControlDir(t *testing.T, deb string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "control")
	dpkgDeb(t, "-e", deb, dir)

	return dir
}

// controlMembers lists the control directory's entries, sorted.
func controlMembers(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read control directory %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	sort.Strings(names)

	return names
}

// dpkgDeb runs the oracle. It resolves the tool through packagingtest.LookTool
// like every other tool-dependent path here, so this file never reaches a tool
// around the gate.
func dpkgDeb(t *testing.T, args ...string) []byte {
	t.Helper()

	tool := packagingtest.LookTool(t, "dpkg-deb")

	out, err := exec.Command(tool, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("dpkg-deb %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}
