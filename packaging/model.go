// Package packaging models the contract the built .deb and .rpm artifacts must
// satisfy, independently of how an artifact is taken apart.
//
// The model is a plain in-memory description of a package: its format, the
// files it installs, the dependencies it declares, and its postinstall
// scriptlet. An extractor (out of scope here) populates it; the job of the
// types declared in this file and in finding.go is only to describe the
// shape. They are inert data: constructing or reading one opens no file and
// starts no process.
//
// Verify checks a Model against the contract. It reports an unknown format,
// every required destination the model installs nothing to or installs more
// than once, and — for the destinations the contract pins them on — a wrong
// mode, a missing configuration-file designation, or content that is not the
// repository source the destination is built from; the remaining assertions
// arrive in later changes.
package packaging

import "io/fs"

// Format identifies the package format a Model describes.
type Format string

const (
	// FormatDeb is a Debian package.
	FormatDeb Format = "deb"
	// FormatRPM is an RPM package.
	FormatRPM Format = "rpm"
)

// Entry is one file the package installs.
//
// Owner and Group are recorded for the convenience of extractors that happen
// to surface them, and they drive NO assertion (M1). An artifact cannot prove
// installed ownership: a DEB payload records numeric UID/GID 0 by
// construction, and the RPM equivalent is unconfirmed against a real nFPM
// build. Ownership on disk after installation is #67's to verify; nothing in
// this package may read these two fields.
type Entry struct {
	// Dest is the absolute destination path the file installs to.
	Dest string
	// Mode is the file mode recorded in the payload.
	Mode fs.FileMode
	// Config reports whether the packaging metadata designates the entry a
	// configuration file.
	Config bool
	// Content is the file's bytes, for the entries whose bytes matter.
	Content []byte
	// Owner is informational only; see the type comment (M1).
	Owner string
	// Group is informational only; see the type comment (M1).
	Group string
}

// Scriptlet is a maintainer script shipped inside the package.
type Scriptlet struct {
	// Content is the script's bytes.
	Content []byte
}

// Model is the extracted description of a single package artifact.
//
// A nil Postinstall is the representation of "the package ships no postinstall
// scriptlet". That distinction is load-bearing: a package that ships no
// scriptlet at all and a package whose scriptlet bytes differ from the
// repository's source are separate faults and are tested separately.
type Model struct {
	// Format is the package format this model describes.
	Format Format
	// Entries are the files the package installs, in no particular order.
	Entries []Entry
	// Dependencies are the package's declared runtime dependencies.
	Dependencies []string
	// Postinstall is the postinstall scriptlet, or nil when the package
	// ships none.
	Postinstall *Scriptlet
}
