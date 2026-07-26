package packagingtest

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// rpmRelease is the Release field every fixture carries. rpmbuild requires the
// field; nothing here varies it, so the artifact's name is a function of the
// Spec's Name and Version alone.
const rpmRelease = "1"

// rpmSpecFileMode and rpmArtifactMode are the modes of the files BuildRPM
// writes itself — the generated spec file and the artifact copied into outDir.
// Neither is recorded in a package; they are ordinary files on disk.
const (
	rpmSpecFileMode = fs.FileMode(0o644)
	rpmArtifactMode = fs.FileMode(0o644)
)

// BuildRPM builds a .rpm from s into outDir and returns the artifact's path.
//
// It takes the same Spec BuildDeb takes, so one fixture declaration can be
// built in both formats. Everything is scoped to scratch space inside outDir: a
// throwaway `_topdir` holding the generated spec file, the staged payload tree
// and rpmbuild's own working directories. outDir ends up holding the artifact
// plus that one directory. Every declared directory and file is chmodded to its
// declared mode while staging, and `%files` records the mode again through
// `%attr`, so the caller's umask cannot alter what the package records.
//
// The generated spec file declares `%global debug_package %{nil}`,
// `AutoReqProv: no` and `BuildArch: noarch`, so the package holds exactly the
// payload the Spec asked for, and every dependency the Spec asked for appears
// verbatim. The requires set is NOT limited to the declared ones, though: rpm
// records entries of its own outside this builder's control — the `rpmlib(...)`
// capabilities rpmbuild writes into every artifact, and `/bin/sh` whenever a
// `%post` section is present, both even under `AutoReqProv: no`. A caller
// asserting on requires therefore checks that each declared entry is present,
// not that the set is exactly the declared one.
//
// Ownership. `%files` emits `%attr(<mode>, <owner>, <group>)` per entry, with
// DefaultOwner and DefaultGroup substituted for an empty Dir.Owner, Dir.Group,
// File.Owner or File.Group. rpm records those names in the header verbatim,
// whether or not the build account or the named accounts exist, so distinct
// per-entry ownership survives into the artifact on any host. This is the
// opposite of BuildDeb, which ignores the fields entirely.
//
// Dependencies. Each Spec.Depends element becomes one `Requires:` line,
// verbatim. rpm's version-constraint syntax needs spaces around the operator:
// measured, `Requires: gamma >= 1` yields the dependency name `gamma`
// constrained to version 1, while `Requires: gamma>=1` is parsed as a
// dependency whose NAME is the whole string `gamma>=1`. A Spec built by both
// builders must therefore use plain names, which both formats parse alike.
//
// Owned paths. rpm owns only what `%files` lists, so the artifact holds exactly
// the declared directories and files and no synthesized parents — unlike a deb,
// where dpkg-deb archives the intermediate directories of every declared path.
//
// Postinstall. A nil Spec.Postinstall emits no `%post` section. A non-nil,
// EMPTY one is a t.Fatalf rather than an empty section: measured, an empty
// `%post` builds but records no body at all — `rpm -qp --scripts` prints only
// `postinstall program: /bin/sh` with no scriptlet header, `%{POSTIN}` is
// `(none)`, and rpm's own tag-presence marker `%|POSTIN?{HASPOST}:{NOPOST}|`
// reads `NOPOST` — so "ships an empty scriptlet" is not a state an rpm artifact
// can represent, let alone one a reader could tell apart from shipping none.
//
// Two measured caveats bind a caller that declares a body:
//
//   - rpm strips ALL trailing newlines from a recorded scriptlet: a `%post`
//     section whose text is "echo one\n\n\n" comes back from `%{POSTIN}` as
//     "echo one". A caller wanting byte-exact equality downstream therefore
//     declares a body with no trailing newline, which round-trips exactly.
//   - a body line beginning with `%` at column 0 would be read by rpmbuild as
//     the start of a new spec section rather than as part of the scriptlet, so
//     a declared body must not contain one.
//
// rpmbuild is resolved through LookTool, so a host without it skips the calling
// test with an explicit reason and an environment declaring the tools present
// (RequireEnv=1) fails instead. The empty-Postinstall check runs before that
// lookup, so it is observable on every host.
func BuildRPM(t TestingT, outDir string, s Spec) string {
	t.Helper()

	if s.Postinstall != nil && *s.Postinstall == "" {
		t.Fatalf("packagingtest: %s declares a non-nil but empty Postinstall, which rpm cannot record: an empty %%post section builds but stores no body — `rpm -qp --scripts` prints only `postinstall program: /bin/sh`, `%%{POSTIN}` is `(none)` and the `%%|POSTIN?{HASPOST}:{NOPOST}|` presence marker reads NOPOST, so the artifact is indistinguishable from one shipping no scriptlet at all. Declare a nil Postinstall for that state, or a non-empty body.", s.Name)

		return ""
	}

	rpmbuild := LookTool(t, "rpmbuild")
	if rpmbuild == "" {
		return ""
	}

	top, err := os.MkdirTemp(outDir, "packagingtest-rpm-")
	if err != nil {
		t.Fatalf("packagingtest: create rpm _topdir under %s: %v", outDir, err)

		return ""
	}

	tree := filepath.Join(top, "tree")
	rpmDir := filepath.Join(top, "RPMS")
	specPath := filepath.Join(top, s.Name+".spec")

	if err := stageRPM(tree, specPath, s); err != nil {
		t.Fatalf("packagingtest: stage rpm fixture %s: %v", s.Name, err)

		return ""
	}

	out, err := exec.Command(rpmbuild, "-bb",
		"--define", "_topdir "+top,
		"--define", "_rpmdir "+rpmDir,
		specPath).CombinedOutput()
	if err != nil {
		t.Fatalf("packagingtest: rpmbuild -bb %s: %v\n%s\nspec file:\n%s", specPath, err, out, rpmSpec(s, tree))

		return ""
	}

	built, err := singleRPM(rpmDir)
	if err != nil {
		t.Fatalf("packagingtest: locate the artifact rpmbuild produced for %s: %v\n%s", s.Name, err, out)

		return ""
	}

	// The artifact is copied out of rpmbuild's own output tree so the returned
	// path sits directly in the caller's outDir, exactly as BuildDeb's does.
	artifact := filepath.Join(outDir, filepath.Base(built))

	body, err := os.ReadFile(built)
	if err != nil {
		t.Fatalf("packagingtest: read built artifact %s: %v", built, err)

		return ""
	}

	if err := writeFileMode(artifact, body, rpmArtifactMode); err != nil {
		t.Fatalf("packagingtest: copy built artifact to %s: %v", artifact, err)

		return ""
	}

	return artifact
}

// stageRPM lays out the payload tree rpmbuild's %install copies and writes the
// generated spec file beside it.
func stageRPM(tree, specPath string, s Spec) error {
	if err := os.Mkdir(tree, synthesizedDirMode); err != nil {
		return err
	}

	if err := os.Chmod(tree, synthesizedDirMode); err != nil {
		return err
	}

	if err := stagePayload(tree, s); err != nil {
		return err
	}

	return writeFileMode(specPath, []byte(rpmSpec(s, tree)), rpmSpecFileMode)
}

// rpmSpec renders the spec file for s, whose %install copies the already-staged
// tree into the build root.
//
// The payload is staged by Go rather than written out as shell commands here,
// so no file's bytes ever have to survive a trip through a spec file's own
// parser.
func rpmSpec(s Spec, tree string) string {
	var b strings.Builder

	// debug_package is disabled because a noarch fixture has no debuginfo to
	// split out and rpmbuild fails rather than producing an empty one.
	b.WriteString("%global debug_package %{nil}\n")
	b.WriteString("Name: " + s.Name + "\n")
	b.WriteString("Version: " + s.Version + "\n")
	b.WriteString("Release: " + rpmRelease + "\n")
	b.WriteString("Summary: throwaway fixture built for tests\n")
	b.WriteString("License: MIT\n")
	b.WriteString("BuildArch: noarch\n")
	// Automatic dependency generation is off so the package's requires are the
	// declared ones and nothing else.
	b.WriteString("AutoReqProv: no\n")

	// One Requires: line per declared dependency, verbatim — no alternative,
	// version constraint or ordering is rewritten.
	for _, dep := range s.Depends {
		b.WriteString("Requires: " + dep + "\n")
	}

	b.WriteString("\n%description\nthrowaway fixture built for tests\n")

	b.WriteString("\n%install\n")
	b.WriteString("rm -rf \"%{buildroot}\"\n")
	b.WriteString("mkdir -p \"%{buildroot}\"\n")
	b.WriteString("cp -a " + rpmShellOperand(tree+"/.") + " \"%{buildroot}/\"\n")

	// A nil Postinstall emits no %post section at all; a declared body is
	// written verbatim. BuildRPM has already rejected an empty one.
	if s.Postinstall != nil {
		b.WriteString("\n%post\n" + *s.Postinstall + "\n")
	}

	b.WriteString("\n%files\n")

	for _, d := range s.Dirs {
		b.WriteString("%dir " + rpmAttr(d.Mode, d.Owner, d.Group) + " " + d.Dest + "\n")
	}

	for _, f := range s.Files {
		line := rpmAttr(f.Mode, f.Owner, f.Group) + " " + f.Dest + "\n"
		if f.Config {
			line = "%config " + line
		}

		b.WriteString(line)
	}

	return b.String()
}

// rpmShellOperand renders p as a single shell word for a spec file's own
// script sections.
//
// Two parsers see the string, so both have to be escaped for. rpmbuild expands
// `%` macros in a script section before the shell ever runs it, and `%%` is the
// spec-file escape for a literal `%`; the shell then sees single quotes, inside
// which every remaining metacharacter — `$`, a backtick, a backslash, a double
// quote, whitespace — is literal. An embedded single quote is the one character
// single quoting cannot carry, so each one is rendered by closing the quoted
// run, emitting a backslash-escaped quote, and reopening — the usual shell
// idiom, spelled out in the replacement below.
func rpmShellOperand(p string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(p, "'", `'\''`), "%", "%%") + "'"
}

// rpmAttr renders one %attr(...) directive, substituting DefaultOwner and
// DefaultGroup for an empty name. Only the permission bits are rendered: the
// type bits of an fs.FileMode are not something %attr expresses.
func rpmAttr(mode fs.FileMode, owner, group string) string {
	if owner == "" {
		owner = DefaultOwner
	}

	if group == "" {
		group = DefaultGroup
	}

	return fmt.Sprintf("%%attr(%04o, %s, %s)", mode.Perm(), owner, group)
}

// singleRPM returns the one artifact below dir, which rpmbuild writes into an
// architecture subdirectory of _rpmdir. More than one, or none, means the build
// did not produce what this builder assumes, so it is an error rather than a
// silent pick.
func singleRPM(dir string) (string, error) {
	var found []string

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() && strings.HasSuffix(path, ".rpm") {
			found = append(found, path)
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	if len(found) != 1 {
		return "", fmt.Errorf("found %d .rpm files under %s, want exactly 1: %q", len(found), dir, found)
	}

	return found[0], nil
}
