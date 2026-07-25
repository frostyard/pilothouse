package packaging

import (
	"embed"
	"fmt"
	"io/fs"
)

// sources holds the repository files the built packages ship, compiled into
// the binary at build time.
//
// This is why the artifact-contract code lives in packaging/ rather than under
// internal/: a //go:embed pattern may not name a parent directory, so only a
// package in this directory can embed these files. Verify's signature is at
// func(Model) []Finding, so there is no seam through which an fs.FS could be
// injected instead. Because the bytes are compiled in, nothing here opens a
// file at run time.
//
//go:embed pilothouse.service pilothouse.pam pilothouse.sysusers
//go:embed pilothouse.env pilothoused.env postinstall.sh
//go:embed deb/pilothoused.service rpm/pilothouse.pam rpm/pilothoused.service
var sources embed.FS

// sourceNames lists every path embedded in sources, in the order the //go:embed
// directives above name them.
var sourceNames = []string{
	"pilothouse.service",
	"pilothouse.pam",
	"pilothouse.sysusers",
	"pilothouse.env",
	"pilothoused.env",
	"postinstall.sh",
	"deb/pilothoused.service",
	"rpm/pilothouse.pam",
	"rpm/pilothoused.service",
}

// sourceBytes returns the embedded bytes of the named repository source.
//
// A name absent from the embed is a programming error, not runtime input — the
// set of embedded files is fixed at compile time and never derived from a
// Model — so it panics rather than returning an error no caller could handle.
func sourceBytes(name string) []byte {
	b, err := sources.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("packaging: %q is not an embedded repository source: %v", name, err))
	}

	return b
}

// requirement is one thing the contract demands of a package.
//
// It carries only the fields the checks in verify.go read; a field is added by
// the change that first asserts on it, so no field is ever dead.
type requirement struct {
	// dest is the absolute destination path the package must install to.
	dest string
	// mode is the permission bits the contract pins for dest.
	//
	// The zero value means the contract states no mode for this destination
	// and Verify asserts none. That is deliberate: .goreleaser.yaml sets
	// file_info on exactly the four destinations that carry a non-zero mode
	// below, so inventing a default for the unit files, the PAM policy, the
	// sysusers file or the binaries would pin something the source of truth
	// does not state.
	//
	// Only permission bits are ever compared, so a row may not carry type
	// bits; see Verify for why.
	mode fs.FileMode
	// config reports whether the packaging metadata must designate dest a
	// configuration file. False means the contract does not require it, not
	// that the designation is forbidden — see Verify.
	config bool
}

// requirements returns the contract table for the given format, and false when
// the format is not one this package knows.
//
// The table is per-format because the contract distinguishes the two — the
// broker unit and the PAM policy are built from different repository sources
// in each — but the destinations, modes and config designations are the same
// in both, so at this commit the two tables are identical.
func requirements(f Format) ([]requirement, bool) {
	switch f {
	case FormatDeb, FormatRPM:
		// A fresh slice per call: callers must not be able to mutate the
		// contract by writing through a shared backing array.
		return []requirement{
			{dest: "/usr/lib/systemd/system/pilothouse.service"},
			{dest: "/usr/lib/systemd/system/pilothoused.service"},
			{dest: "/etc/pam.d/pilothouse", config: true},
			{dest: "/usr/lib/sysusers.d/pilothouse.conf"},
			{dest: "/etc/pilothouse", mode: 0o750},
			{dest: "/etc/pilothouse/storage/credentials", mode: 0o700},
			{dest: "/etc/pilothouse/pilothouse.env", mode: 0o640, config: true},
			{dest: "/etc/pilothouse/pilothoused.env", mode: 0o640, config: true},
			{dest: "/usr/bin/pilothouse"},
			{dest: "/usr/bin/pilothoused"},
		}, true
	default:
		return nil, false
	}
}
