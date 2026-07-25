package packagingtest

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Dir declares a directory a fixture package installs.
type Dir struct {
	// Dest is the absolute destination path.
	Dest string
	// Mode is the mode the directory is recorded with.
	Mode fs.FileMode
}

// File declares a regular file a fixture package installs.
type File struct {
	// Dest is the absolute destination path.
	Dest string
	// Mode is the mode the file is recorded with.
	Mode fs.FileMode
	// Content is the file's bytes.
	Content []byte
	// Config marks the file as a configuration file in the package metadata.
	Config bool
}

// Spec declares a throwaway fixture package.
//
// Postinstall distinguishes three states, and the distinction is load-bearing:
// nil means the fixture ships no postinstall scriptlet at all, while a non-nil
// pointer — including one to the empty string — means it ships that body.
type Spec struct {
	Name        string
	Version     string
	Dirs        []Dir
	Files       []File
	Depends     []string
	Postinstall *string
}

// controlDirMode is the mode dpkg-deb requires of a DEBIAN control directory.
// `dpkg-deb --build` refuses anything outside 0755-0775 ("control directory
// has bad permissions 700"), and t.TempDir() creates 0700 directories, so
// BuildDeb sets this explicitly rather than inheriting it.
const controlDirMode = fs.FileMode(0o755)

// synthesizedDirMode is the mode BuildDeb gives every intermediate directory
// it creates. dpkg-deb archives the intermediate directories of each declared
// path, so their recorded modes have to be determinate rather than a product
// of the caller's umask.
const synthesizedDirMode = fs.FileMode(0o755)

// BuildDeb builds a .deb from s into outDir and returns the artifact's path.
//
// The staging tree is created in scratch space inside outDir, so outDir ends
// up holding the artifact plus one throwaway staging directory; outDir is
// caller-supplied so a caller can build fixtures directly into a directory it
// later scans for packages. Every declared directory and file is chmodded to
// its declared mode explicitly, so the caller's umask cannot alter what the
// package records.
//
// dpkg-deb is resolved through LookTool, so a host without it skips the
// calling test with an explicit reason and an environment declaring the tools
// present (RequireEnv=1) fails instead.
func BuildDeb(t TestingT, outDir string, s Spec) string {
	t.Helper()

	dpkgDeb := LookTool(t, "dpkg-deb")
	if dpkgDeb == "" {
		return ""
	}

	tree, err := os.MkdirTemp(outDir, "packagingtest-deb-")
	if err != nil {
		t.Fatalf("packagingtest: create staging tree under %s: %v", outDir, err)

		return ""
	}

	if err := stageDeb(tree, s); err != nil {
		t.Fatalf("packagingtest: stage deb fixture %s: %v", s.Name, err)

		return ""
	}

	deb := filepath.Join(outDir, s.Name+"_"+s.Version+"_all.deb")

	out, err := exec.Command(dpkgDeb, "--root-owner-group", "--build", tree, deb).CombinedOutput()
	if err != nil {
		t.Fatalf("packagingtest: dpkg-deb --build %s %s: %v\n%s", tree, deb, err, out)

		return ""
	}

	return deb
}

// stageDeb lays out the DEBIAN control directory and the payload tree.
func stageDeb(tree string, s Spec) error {
	if err := stageDebControl(tree, s); err != nil {
		return err
	}

	return stageDebPayload(tree, s)
}

// stageDebControl writes DEBIAN/control plus the two optional control members.
func stageDebControl(tree string, s Spec) error {
	controlDir := filepath.Join(tree, "DEBIAN")
	if err := os.Mkdir(controlDir, controlDirMode); err != nil {
		return err
	}

	if err := os.Chmod(controlDir, controlDirMode); err != nil {
		return err
	}

	if err := writeFileMode(filepath.Join(controlDir, "control"), []byte(debControl(s)), 0o644); err != nil {
		return err
	}

	// conffiles is omitted entirely when nothing is marked Config, so that a
	// fixture with no configuration files ships no such control member.
	var conffiles []string

	for _, f := range s.Files {
		if f.Config {
			conffiles = append(conffiles, f.Dest)
		}
	}

	if len(conffiles) > 0 {
		body := strings.Join(conffiles, "\n") + "\n"
		if err := writeFileMode(filepath.Join(controlDir, "conffiles"), []byte(body), 0o644); err != nil {
			return err
		}
	}

	// A nil Postinstall ships no postinst member; a pointer to "" ships a
	// zero-byte one.
	if s.Postinstall != nil {
		if err := writeFileMode(filepath.Join(controlDir, "postinst"), []byte(*s.Postinstall), 0o755); err != nil {
			return err
		}
	}

	return nil
}

// stageDebPayload creates the declared directories and files.
//
// Directories are created at synthesizedDirMode first and given their declared
// modes only once every file is written, so a restrictive declared mode cannot
// block the staging of something beneath it. The declared modes are applied
// deepest path first for the same reason.
func stageDebPayload(tree string, s Spec) error {
	for _, d := range s.Dirs {
		if err := makeDirs(tree, d.Dest); err != nil {
			return err
		}
	}

	for _, f := range s.Files {
		if err := makeDirs(tree, filepath.Dir(f.Dest)); err != nil {
			return err
		}
	}

	for _, f := range s.Files {
		if err := writeFileMode(filepath.Join(tree, filepath.FromSlash(f.Dest)), f.Content, f.Mode); err != nil {
			return err
		}
	}

	deepest := 0

	for _, d := range s.Dirs {
		if n := len(splitPath(d.Dest)); n > deepest {
			deepest = n
		}
	}

	for level := deepest; level >= 1; level-- {
		for _, d := range s.Dirs {
			if len(splitPath(d.Dest)) != level {
				continue
			}

			if err := os.Chmod(filepath.Join(tree, filepath.FromSlash(d.Dest)), d.Mode); err != nil {
				return err
			}
		}
	}

	return nil
}

// debControl renders DEBIAN/control. Depends is emitted only when the spec
// declares dependencies, and its value is the declared strings joined with
// ", " — no alternative, version constraint or ordering is rewritten.
func debControl(s Spec) string {
	var b strings.Builder

	b.WriteString("Package: " + s.Name + "\n")
	b.WriteString("Version: " + s.Version + "\n")
	b.WriteString("Architecture: all\n")
	b.WriteString("Maintainer: packagingtest <packagingtest@example.invalid>\n")

	if len(s.Depends) > 0 {
		b.WriteString("Depends: " + strings.Join(s.Depends, ", ") + "\n")
	}

	b.WriteString("Description: throwaway fixture built for tests\n")

	return b.String()
}

// makeDirs creates dest and each of its parents below tree, chmodding every
// one of them to synthesizedDirMode explicitly.
func makeDirs(tree, dest string) error {
	cur := tree

	for _, part := range splitPath(dest) {
		cur = filepath.Join(cur, part)

		if err := os.Mkdir(cur, synthesizedDirMode); err != nil && !os.IsExist(err) {
			return err
		}

		if err := os.Chmod(cur, synthesizedDirMode); err != nil {
			return err
		}
	}

	return nil
}

// writeFileMode writes content to path and chmods it to mode explicitly, so
// the caller's umask cannot mask bits off the declared mode.
func writeFileMode(path string, content []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}

	return os.Chmod(path, mode)
}

// splitPath returns the non-empty components of a slash-separated path.
func splitPath(p string) []string {
	var parts []string

	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}

	return parts
}
