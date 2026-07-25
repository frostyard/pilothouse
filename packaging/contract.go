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

// contractDependencies returns the runtime dependency list the contract
// requires for the given format, and false when the format is not one this
// package knows.
//
// The two lists are hand-written constants, transcribed from
// .goreleaser.yaml's per-format overrides.<format>.dependencies at b1294e1.
// Nothing here reads that file: keeping the expectation hand-written is what
// makes it an independent statement of the contract rather than a restatement
// of whatever the config happens to say. Tying the two together is a separate
// drift guard, which arrives in a later change.
//
// The name is contractDependencies rather than the more obvious
// wantDependencies because goreleaser_config_test.go already declares
// wantDependencies in this package for the configuration-level assertion.
//
// Each list names the DIRECT provider of the same six runtime roles on both
// platforms: the linked C library, the PAM shared library pilothoused links
// via cgo, the package providing the PAM modules the policy loads, the package
// providing the PAM stacks the policy includes, the libsystemd shared library,
// and systemd itself.
//
// A fresh slice per call, for the same reason requirements returns one:
// callers must not be able to mutate the contract by writing through a shared
// backing array, and Verify sorts what it compares.
func contractDependencies(f Format) ([]string, bool) {
	switch f {
	case FormatDeb:
		return []string{
			"libc6",
			"libpam0g",
			"libpam-modules",
			"libpam-runtime",
			"libsystemd0",
			"systemd",
		}, true
	case FormatRPM:
		return []string{
			"glibc",
			"pam-libs",
			"pam",
			"authselect-libs",
			"systemd-libs",
			"systemd",
		}, true
	default:
		return nil, false
	}
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
	// source is the name of the embedded repository source whose bytes the
	// entry installed to dest must equal, exactly.
	//
	// The empty string means the contract compares no content at this
	// destination, and it is empty for exactly four rows: the two binaries,
	// whose bytes differ per build so only destination and multiplicity are
	// contract-relevant, and the two directories, which have no content.
	source string
}

// requirements returns the contract table for the given format, and false when
// the format is not one this package knows.
//
// The table is per-format because the contract distinguishes the two: the
// destinations, modes and config designations are the same in both, but the
// broker unit and the PAM policy are built from different repository sources
// in each, so the two tables differ in exactly those two source names.
func requirements(f Format) ([]requirement, bool) {
	// The two per-format sources. The broker unit differs in its
	// --admin-group default and the PAM policy in the stacks it includes, so
	// a package shipping the other format's file is a real defect and is
	// precisely what the source column makes detectable.
	var brokerUnit, pamPolicy string

	switch f {
	case FormatDeb:
		brokerUnit, pamPolicy = "deb/pilothoused.service", "pilothouse.pam"
	case FormatRPM:
		brokerUnit, pamPolicy = "rpm/pilothoused.service", "rpm/pilothouse.pam"
	default:
		return nil, false
	}

	// A fresh slice per call: callers must not be able to mutate the contract
	// by writing through a shared backing array.
	return []requirement{
		{dest: "/usr/lib/systemd/system/pilothouse.service", source: "pilothouse.service"},
		{dest: "/usr/lib/systemd/system/pilothoused.service", source: brokerUnit},
		{dest: "/etc/pam.d/pilothouse", config: true, source: pamPolicy},
		{dest: "/usr/lib/sysusers.d/pilothouse.conf", source: "pilothouse.sysusers"},
		{dest: "/etc/pilothouse", mode: 0o750},
		{dest: "/etc/pilothouse/storage/credentials", mode: 0o700},
		{dest: "/etc/pilothouse/pilothouse.env", mode: 0o640, config: true, source: "pilothouse.env"},
		{dest: "/etc/pilothouse/pilothoused.env", mode: 0o640, config: true, source: "pilothoused.env"},
		{dest: "/usr/bin/pilothouse"},
		{dest: "/usr/bin/pilothoused"},
	}, true
}
