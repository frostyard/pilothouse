package extract

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/frostyard/pilothouse/packaging"
)

// dpkgDeb is the only external tool the deb backend runs.
const dpkgDeb = "dpkg-deb"

// scratchDirMode is the mode of the scratch directories Deb creates for
// dpkg-deb to extract into. dpkg-deb does not create a missing parent, so both
// are created up front.
const scratchDirMode = fs.FileMode(0o755)

// Deb reads the .deb at path and returns the model describing it.
//
// It runs dpkg-deb three separate times under ctx, once per thing it needs,
// and caches nothing between them:
//
//   - `dpkg-deb -x <deb> <dir>` extracts the payload into scratch space. That
//     extraction was measured to reproduce recorded modes exactly (0640, 0700
//     and 0750 under `umask 077`), which is why entry modes are read off the
//     extracted tree rather than parsed out of the `drwxr-x---` strings
//     `dpkg-deb -c` prints.
//   - `dpkg-deb -e <deb> <dir>` extracts the control members. It writes a
//     DIRECTORY and emits no tar stream, so a `-e … | tar -xO postinst`
//     pipeline is not a valid way to read one of them.
//   - `dpkg-deb -f <deb> Depends` reads the dependency field. A package that
//     declares none exits 0 with empty output, so absence is not a failure.
//
// Entries come from walking the extracted payload with the tree root itself
// skipped, so "/" is never an entry, while the intermediate directories
// dpkg-deb synthesizes for a declared path (`./opt/`, `./usr/`, `./usr/bin/`
// for a package rooted at /opt/… and /usr/bin/…) are. Directory entries carry
// Mode and no Content; regular files carry their bytes except under
// usrBinDir; symlinks, devices and fifos are not emitted at all, so a
// destination the contract requires but the package ships as one of those is
// reported by the parent package as absent rather than silently accepted.
//
// Config is true for the destinations the conffiles control member lists.
// Postinstall is a non-nil packaging.Scriptlet exactly when a postinst control
// member exists and nil when it does not: shipping no scriptlet at all and
// shipping one with the wrong bytes are separate faults, and returning an empty
// scriptlet for the first would silently turn it into the second.
//
// Dependencies are split on commas only and trimmed. Alternatives (`a | b`) and
// version constraints (`c (>= 1)`) pass through verbatim and declaration order
// is preserved; normalizing either would hide drift the contract is meant to
// catch, and dropping the alternative half of an expression would make the rule
// against alternatives unfireable.
//
// Owner and Group are left empty for a deb. A dpkg-deb-extracted tree cannot
// recover the archive's recorded ownership, and nFPM's DEB tar records numeric
// UID/GID 0 by construction anyway; both fields are informational and drive no
// assertion (M1).
func Deb(ctx context.Context, path string) (packaging.Model, error) {
	scratch, err := os.MkdirTemp("", "pilothouse-extract-deb-")
	if err != nil {
		return packaging.Model{}, fmt.Errorf("packaging/extract: create scratch directory: %w", err)
	}

	defer func() { _ = os.RemoveAll(scratch) }()

	payloadDir := filepath.Join(scratch, "payload")
	controlDir := filepath.Join(scratch, "control")

	for _, dir := range []string{payloadDir, controlDir} {
		if err := os.Mkdir(dir, scratchDirMode); err != nil {
			return packaging.Model{}, fmt.Errorf("packaging/extract: create scratch directory: %w", err)
		}
	}

	if _, err := runTool(ctx, dpkgDeb, "-x", path, payloadDir); err != nil {
		return packaging.Model{}, err
	}

	if _, err := runTool(ctx, dpkgDeb, "-e", path, controlDir); err != nil {
		return packaging.Model{}, err
	}

	dependsField, err := runTool(ctx, dpkgDeb, "-f", path, "Depends")
	if err != nil {
		return packaging.Model{}, err
	}

	dependencies, err := debDependencies(dependsField)
	if err != nil {
		return packaging.Model{}, err
	}

	conffiles, err := debConffiles(controlDir)
	if err != nil {
		return packaging.Model{}, err
	}

	entries, err := debEntries(payloadDir, conffiles)
	if err != nil {
		return packaging.Model{}, err
	}

	postinstall, err := debPostinstall(controlDir)
	if err != nil {
		return packaging.Model{}, err
	}

	return packaging.Model{
		Format:       packaging.FormatDeb,
		Entries:      entries,
		Dependencies: dependencies,
		Postinstall:  postinstall,
	}, nil
}

// debEntries walks the extracted payload tree and maps it onto entries.
//
// The tree root is skipped, so the payload's own directory never becomes an
// entry for "/". Anything that is neither a directory nor a regular file is
// dropped rather than guessed at.
func debEntries(payloadDir string, conffiles map[string]bool) ([]packaging.Entry, error) {
	var entries []packaging.Entry

	walk := func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if name == payloadDir {
			return nil
		}

		rel, err := filepath.Rel(payloadDir, name)
		if err != nil {
			return err
		}

		dest := "/" + filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		entry := packaging.Entry{
			Dest:   dest,
			Mode:   info.Mode(),
			Config: conffiles[dest],
		}

		switch {
		case d.IsDir():
			entries = append(entries, entry)
		case d.Type().IsRegular():
			if !underUsrBin(dest) {
				content, err := os.ReadFile(name)
				if err != nil {
					return err
				}

				entry.Content = content
			}

			entries = append(entries, entry)
		default:
			// A symlink, device or fifo is not emitted. Recording one as
			// though it were a file would let a destination shipped in the
			// wrong shape pass unnoticed.
		}

		return nil
	}

	if err := filepath.WalkDir(payloadDir, walk); err != nil {
		return nil, fmt.Errorf("packaging/extract: walk extracted payload: %w", err)
	}

	return entries, nil
}

// debConffiles reads the conffiles control member into a set of destinations.
//
// The member is absent from a package that marks nothing a configuration file,
// and that absence means exactly "no configuration entries", not a failure. A
// line that is present but is not an absolute path is malformed and is an
// error rather than a silently dropped row: an artifact read wrong must not
// come back as a confident model.
func debConffiles(controlDir string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(controlDir, "conffiles"))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("packaging/extract: read conffiles: %w", err)
	}

	conffiles := make(map[string]bool)

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "/") {
			return nil, fmt.Errorf("packaging/extract: conffiles entry %q is not an absolute path", line)
		}

		conffiles[line] = true
	}

	return conffiles, nil
}

// debPostinstall reads the postinst control member.
//
// An absent member yields nil, which is the representation of "this package
// ships no postinstall scriptlet"; a present one yields its bytes verbatim,
// including a zero-byte body.
func debPostinstall(controlDir string) (*packaging.Scriptlet, error) {
	raw, err := os.ReadFile(filepath.Join(controlDir, "postinst"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil //nolint:nilnil // a nil Scriptlet is the model's representation of "ships none".
	}

	if err != nil {
		return nil, fmt.Errorf("packaging/extract: read postinst: %w", err)
	}

	return &packaging.Scriptlet{Content: raw}, nil
}

// debDependencies splits a Depends field on commas only.
//
// An empty field yields no dependencies. An empty component between two commas
// is malformed and is an error, for the same reason a malformed conffiles line
// is.
func debDependencies(raw []byte) ([]string, error) {
	field := strings.TrimSpace(string(raw))
	if field == "" {
		return nil, nil
	}

	parts := strings.Split(field, ",")
	dependencies := make([]string, 0, len(parts))

	for _, part := range parts {
		dependency := strings.TrimSpace(part)
		if dependency == "" {
			return nil, fmt.Errorf("packaging/extract: Depends field %q has an empty comma-separated component", field)
		}

		dependencies = append(dependencies, dependency)
	}

	return dependencies, nil
}
