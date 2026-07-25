package packaging

import (
	"bytes"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file holds behavioral tests only: everything here runs against models
// built in memory from bytes compiled into the test binary by contract.go's
// //go:embed, so no test in this file opens a file or runs a command.

// contractModel builds a model that satisfies the contract for the given
// format.
//
// It is the shared fixture every mutation test starts from: a test copies it,
// breaks exactly one thing, and asserts the finding that break must produce.
// The content fields come from the real repository sources through
// sourceBytes, so the fixture cannot drift from the files the packages ship.
// That is fixture-from-source, not oracle-from-gate — what proves an assertion
// is the mutation, and every assertion carries one.
func contractModel(t *testing.T, format Format) Model {
	t.Helper()

	// The two per-format sources: the broker unit differs in its
	// --admin-group default and the PAM policy in the stacks it includes.
	var brokerUnit, pamPolicy string
	switch format {
	case FormatDeb:
		brokerUnit, pamPolicy = "deb/pilothoused.service", "pilothouse.pam"
	case FormatRPM:
		brokerUnit, pamPolicy = "rpm/pilothoused.service", "rpm/pilothouse.pam"
	default:
		t.Fatalf("contractModel: %q is not a packaged format", string(format))
	}

	return Model{
		Format: format,
		Entries: []Entry{{
			Dest:    "/usr/lib/systemd/system/pilothouse.service",
			Mode:    0o644,
			Content: sourceBytes("pilothouse.service"),
			Owner:   "root",
			Group:   "root",
		}, {
			Dest:    "/usr/lib/systemd/system/pilothoused.service",
			Mode:    0o644,
			Content: sourceBytes(brokerUnit),
			Owner:   "root",
			Group:   "root",
		}, {
			Dest:    "/etc/pam.d/pilothouse",
			Mode:    0o644,
			Config:  true,
			Content: sourceBytes(pamPolicy),
			Owner:   "root",
			Group:   "root",
		}, {
			Dest:    "/usr/lib/sysusers.d/pilothouse.conf",
			Mode:    0o644,
			Content: sourceBytes("pilothouse.sysusers"),
			Owner:   "root",
			Group:   "root",
		}, {
			Dest:  "/etc/pilothouse",
			Mode:  0o750,
			Owner: "root",
			Group: "pilothouse",
		}, {
			Dest:  "/etc/pilothouse/storage/credentials",
			Mode:  0o700,
			Owner: "root",
			Group: "root",
		}, {
			Dest:    "/etc/pilothouse/pilothouse.env",
			Mode:    0o640,
			Config:  true,
			Content: sourceBytes("pilothouse.env"),
			Owner:   "root",
			Group:   "pilothouse",
		}, {
			Dest:    "/etc/pilothouse/pilothoused.env",
			Mode:    0o640,
			Config:  true,
			Content: sourceBytes("pilothoused.env"),
			Owner:   "root",
			Group:   "pilothouse",
		}, {
			// Binary bytes differ per build and are never compared; the
			// fixture carries a placeholder so the entry is not
			// accidentally special.
			Dest:    "/usr/bin/pilothouse",
			Mode:    0o755,
			Content: []byte("pilothouse binary bytes"),
			Owner:   "root",
			Group:   "root",
		}, {
			Dest:    "/usr/bin/pilothoused",
			Mode:    0o755,
			Content: []byte("pilothoused binary bytes"),
			Owner:   "root",
			Group:   "root",
		}},
		Dependencies: fixtureDependencies(t, format),
		Postinstall:  &Scriptlet{Content: sourceBytes("postinstall.sh")},
	}
}

// fixtureDependencies returns the dependency list .goreleaser.yaml declares for
// the format, transcribed by hand from that file's per-format overrides.
func fixtureDependencies(t *testing.T, format Format) []string {
	t.Helper()

	switch format {
	case FormatDeb:
		return []string{"libc6", "libpam0g", "libpam-modules", "libpam-runtime", "libsystemd0", "systemd"}
	case FormatRPM:
		return []string{"glibc", "pam-libs", "pam", "authselect-libs", "systemd-libs", "systemd"}
	default:
		t.Fatalf("fixtureDependencies: %q is not a packaged format", string(format))

		return nil
	}
}

// findingsFor counts the findings carrying exactly the given code and path.
// Findings are matched by the (Code, Path) pair throughout this package's
// tests; Message is deliberately never matched on.
func findingsFor(findings []Finding, code, path string) int {
	n := 0
	for _, f := range findings {
		if f.Code == code && f.Path == path {
			n++
		}
	}

	return n
}

// withoutEntry returns a copy of m with the entry installing to dest removed,
// failing the test if the fixture had no such entry (which would make the
// mutation vacuous).
func withoutEntry(t *testing.T, m Model, dest string) Model {
	t.Helper()

	kept := make([]Entry, 0, len(m.Entries))
	removed := 0
	for _, entry := range m.Entries {
		if entry.Dest == dest {
			removed++

			continue
		}
		kept = append(kept, entry)
	}
	require.Equalf(t, 1, removed, "fixture should install exactly one entry at %s", dest)

	m.Entries = kept

	return m
}

// relocateEntry returns a copy of m with the entry at from installed to to
// instead, failing the test if the fixture had no entry at from.
func relocateEntry(t *testing.T, m Model, from, to string) Model {
	t.Helper()

	moved := 0
	entries := make([]Entry, len(m.Entries))
	copy(entries, m.Entries)
	for i := range entries {
		if entries[i].Dest == from {
			entries[i].Dest = to
			moved++
		}
	}
	require.Equalf(t, 1, moved, "fixture should install exactly one entry at %s", from)

	m.Entries = entries

	return m
}

// duplicateEntry returns a copy of m with the entry installing to dest
// appended copies further times, so the model installs 1+copies entries there.
// It fails the test if the fixture had no entry at dest.
func duplicateEntry(t *testing.T, m Model, dest string, copies int) Model {
	t.Helper()

	var found *Entry
	entries := make([]Entry, len(m.Entries))
	copy(entries, m.Entries)
	for i := range entries {
		if entries[i].Dest == dest {
			require.Nilf(t, found, "fixture should install exactly one entry at %s", dest)
			found = &entries[i]
		}
	}
	require.NotNilf(t, found, "fixture should install exactly one entry at %s", dest)

	for range copies {
		entries = append(entries, *found)
	}

	m.Entries = entries

	return m
}

// withMode returns a copy of m with the mode of the entry installing to dest
// replaced, failing the test if the fixture had no entry at dest or already
// recorded that mode (which would make the mutation vacuous).
func withMode(t *testing.T, m Model, dest string, mode fs.FileMode) Model {
	t.Helper()

	changed := 0
	entries := make([]Entry, len(m.Entries))
	copy(entries, m.Entries)
	for i := range entries {
		if entries[i].Dest == dest {
			require.NotEqualf(t, mode, entries[i].Mode, "fixture already records mode %#o at %s", mode, dest)
			entries[i].Mode = mode
			changed++
		}
	}
	require.Equalf(t, 1, changed, "fixture should install exactly one entry at %s", dest)

	m.Entries = entries

	return m
}

// withConfig returns a copy of m with the config designation of the entry
// installing to dest set, failing the test if the fixture had no entry at dest
// or already carried that designation.
func withConfig(t *testing.T, m Model, dest string, config bool) Model {
	t.Helper()

	changed := 0
	entries := make([]Entry, len(m.Entries))
	copy(entries, m.Entries)
	for i := range entries {
		if entries[i].Dest == dest {
			require.NotEqualf(t, config, entries[i].Config, "fixture already records Config=%t at %s", config, dest)
			entries[i].Config = config
			changed++
		}
	}
	require.Equalf(t, 1, changed, "fixture should install exactly one entry at %s", dest)

	m.Entries = entries

	return m
}

// withContent returns a copy of m with the content of the entry installing to
// dest replaced, failing the test if the fixture had no entry at dest or
// already carried exactly those bytes (which would make the mutation vacuous).
//
// The vacuity guard is load-bearing for the cross-format tests: substituting
// the other format's PAM policy or broker unit only proves anything because
// this require fails if the two repository sources are ever made identical.
func withContent(t *testing.T, m Model, dest string, content []byte) Model {
	t.Helper()

	changed := 0
	entries := make([]Entry, len(m.Entries))
	copy(entries, m.Entries)

	for i := range entries {
		if entries[i].Dest == dest {
			require.Falsef(t, bytes.Equal(entries[i].Content, content),
				"fixture already records exactly these bytes at %s", dest)
			entries[i].Content = content
			changed++
		}
	}

	require.Equalf(t, 1, changed, "fixture should install exactly one entry at %s", dest)

	m.Entries = entries

	return m
}

// contentAt returns the content of the entry installing to dest, failing the
// test if the fixture does not install exactly one entry there.
func contentAt(t *testing.T, m Model, dest string) []byte {
	t.Helper()

	var content []byte

	found := 0

	for _, entry := range m.Entries {
		if entry.Dest == dest {
			content = entry.Content
			found++
		}
	}

	require.Equalf(t, 1, found, "fixture should install exactly one entry at %s", dest)
	require.NotEmptyf(t, content, "fixture should carry content at %s", dest)

	return content
}

// perturb returns content with a single newline appended.
//
// The comparison is exact and normalizes nothing, so this minimal difference —
// the one a well-meaning extractor is most likely to introduce — must be
// reported just as loudly as a wholly different file.
func perturb(content []byte) []byte {
	perturbed := make([]byte, 0, len(content)+1)
	perturbed = append(perturbed, content...)

	return append(perturbed, '\n')
}

// withoutDependency returns a copy of m no longer declaring dep, failing the
// test if the fixture did not declare it exactly once (which would make the
// mutation vacuous or ambiguous).
func withoutDependency(t *testing.T, m Model, dep string) Model {
	t.Helper()

	kept := make([]string, 0, len(m.Dependencies))
	removed := 0

	for _, declared := range m.Dependencies {
		if declared == dep {
			removed++

			continue
		}

		kept = append(kept, declared)
	}

	require.Equalf(t, 1, removed, "fixture should declare %s exactly once", dep)

	m.Dependencies = kept

	return m
}

// withExtraDependency returns a copy of m declaring dep in addition to
// everything it already declares, failing the test if the fixture already
// declares it — that would be the duplicate mutation, not the extra one.
func withExtraDependency(t *testing.T, m Model, dep string) Model {
	t.Helper()

	require.NotContainsf(t, m.Dependencies, dep, "fixture should not already declare %s", dep)

	m.Dependencies = append(slices.Clone(m.Dependencies), dep)

	return m
}

// withDuplicatedDependency returns a copy of m declaring dep a second time,
// failing the test if the fixture did not declare it exactly once.
//
// This is the mutation a set-membership comparison would silently accept: the
// set of declared names is unchanged and only the multiplicity differs.
func withDuplicatedDependency(t *testing.T, m Model, dep string) Model {
	t.Helper()

	declared := 0

	for _, name := range m.Dependencies {
		if name == dep {
			declared++
		}
	}

	require.Equalf(t, 1, declared, "fixture should declare %s exactly once", dep)

	m.Dependencies = append(slices.Clone(m.Dependencies), dep)

	return m
}

// withRenamedDependency returns a copy of m declaring to where it declared
// from, failing the test if the fixture did not declare from exactly once or
// if the two names are equal (which would make the mutation vacuous).
//
// to may be a name the fixture already declares: that is exactly the
// multiplicity mutation, which keeps the list's length and drops one distinct
// name.
func withRenamedDependency(t *testing.T, m Model, from, to string) Model {
	t.Helper()

	require.NotEqual(t, from, to, "renaming a dependency to itself is not a mutation")

	deps := slices.Clone(m.Dependencies)
	renamed := 0

	for i := range deps {
		if deps[i] == from {
			deps[i] = to
			renamed++
		}
	}

	require.Equalf(t, 1, renamed, "fixture should declare %s exactly once", from)

	m.Dependencies = deps

	return m
}

// packagedFormats is the (format, name) axis every mutation table runs over.
var packagedFormats = []struct {
	name   string
	format Format
}{
	{name: "deb", format: FormatDeb},
	{name: "rpm", format: FormatRPM},
}

// requiredDestinations is the ten destinations the contract demands, written
// out by hand from the issue rather than read back from contract.go's table:
// the table is the thing under test, so it may not also be the oracle.
var requiredDestinations = []string{
	"/usr/lib/systemd/system/pilothouse.service",
	"/usr/lib/systemd/system/pilothoused.service",
	"/etc/pam.d/pilothouse",
	"/usr/lib/sysusers.d/pilothouse.conf",
	"/etc/pilothouse",
	"/etc/pilothouse/storage/credentials",
	"/etc/pilothouse/pilothouse.env",
	"/etc/pilothouse/pilothoused.env",
	"/usr/bin/pilothouse",
	"/usr/bin/pilothoused",
}

// modePinnedDestinations is the four destinations .goreleaser.yaml gives a
// file_info mode, transcribed by hand from the issue for the same reason
// requiredDestinations is: contract.go's table is the thing under test.
var modePinnedDestinations = []struct {
	dest string
	mode fs.FileMode
}{
	{dest: "/etc/pilothouse", mode: 0o750},
	{dest: "/etc/pilothouse/storage/credentials", mode: 0o700},
	{dest: "/etc/pilothouse/pilothouse.env", mode: 0o640},
	{dest: "/etc/pilothouse/pilothoused.env", mode: 0o640},
}

// modeFreeDestinations is the destinations the contract states no mode for, so
// changing their mode must produce nothing. One representative unit file and
// one binary suffice to prove the zero-mode row really is unasserted.
var modeFreeDestinations = []string{
	"/usr/lib/systemd/system/pilothouse.service",
	"/usr/bin/pilothouse",
}

// configDestinations is the three destinations the packaging metadata must
// designate configuration files, likewise written out by hand.
var configDestinations = []string{
	"/etc/pam.d/pilothouse",
	"/etc/pilothouse/pilothouse.env",
	"/etc/pilothouse/pilothoused.env",
}

// byteComparedDestinations is the six destinations whose bytes the contract
// pins to a repository source, written out by hand from the issue for the same
// reason requiredDestinations is: contract.go's table is the thing under test
// and may not also be the oracle.
var byteComparedDestinations = []string{
	"/usr/lib/systemd/system/pilothouse.service",
	"/usr/lib/systemd/system/pilothoused.service",
	"/etc/pam.d/pilothouse",
	"/usr/lib/sysusers.d/pilothouse.conf",
	"/etc/pilothouse/pilothouse.env",
	"/etc/pilothouse/pilothoused.env",
}

// contentFreeDestinations is the four destinations the contract compares no
// content at: the two binaries, whose bytes differ per build so only
// destination and multiplicity are contract-relevant, and the two directories,
// which have no content.
var contentFreeDestinations = []string{
	"/usr/bin/pilothouse",
	"/usr/bin/pilothoused",
	"/etc/pilothouse",
	"/etc/pilothouse/storage/credentials",
}

// dependencyFaults names, per format, one concrete instance of each fault N2
// requires a dependency list to be rejected for.
//
// Every name here is written out by hand from .goreleaser.yaml's per-format
// overrides — the same provenance as fixtureDependencies — and never read back
// from contract.go's contractDependencies, which is the thing under test and
// may not also be the oracle.
var dependencyFaults = []struct {
	name   string
	format Format
	// drop is the element the missing-element mutation removes.
	drop string
	// extra is a plausible neighbouring package the fixture does NOT declare,
	// which the extra-element mutation adds.
	extra string
	// duplicate is the element the duplicated-element mutation repeats.
	duplicate string
	// misspelledFrom and misspelledTo are the element the misspelling
	// mutation replaces and the near-miss name it replaces it with.
	misspelledFrom, misspelledTo string
	// collapsedFrom and collapsedTo are the multiplicity mutation: the
	// element replaced, and the already-declared element it is replaced by.
	// The result has the same length as the contract list and one fewer
	// distinct name, so only a multiset comparison rejects it.
	collapsedFrom, collapsedTo string
	// alternativeFrom is the element the alternatives mutation rewrites, and
	// alternativeTo is the alternative-bearing expression it becomes.
	alternativeFrom, alternativeTo string
}{{
	name:            "deb",
	format:          FormatDeb,
	drop:            "libpam-runtime",
	extra:           "libpam-modules-bin",
	duplicate:       "systemd",
	misspelledFrom:  "libsystemd0",
	misspelledTo:    "libsystemd-0",
	collapsedFrom:   "libpam0g",
	collapsedTo:     "libc6",
	alternativeFrom: "libc6",
	alternativeTo:   "libc6 | libc6-udeb",
}, {
	name:            "rpm",
	format:          FormatRPM,
	drop:            "authselect-libs",
	extra:           "pam-devel",
	duplicate:       "systemd",
	misspelledFrom:  "systemd-libs",
	misspelledTo:    "systemd-lib",
	collapsedFrom:   "pam-libs",
	collapsedTo:     "glibc",
	alternativeFrom: "glibc",
	alternativeTo:   "glibc | glibc-minimal-langpack",
}}

// verifyAsContractSignature returns Verify through exactly the signature the
// issue fixes, func(Model) []Finding. The return statement stops compiling if
// Verify's parameter or result type ever changes.
func verifyAsContractSignature() func(Model) []Finding { return Verify }

// TestVerifySignature pins Verify's exported signature and proves the pinned
// function value is the one under test.
func TestVerifySignature(t *testing.T) {
	t.Parallel()

	verify := verifyAsContractSignature()
	require.NotNil(t, verify)
	require.Empty(t, verify(contractModel(t, FormatDeb)))
	require.Equal(t, 1, findingsFor(verify(Model{}), CodeUnknownFormat, ""))
}

// TestVerifyContractModelIsClean is the happy path: the fixture violates
// nothing Verify asserts, in either format. Every mutation test below depends
// on this being true, since it is what makes a single finding attributable to
// the single thing the test broke.
func TestVerifyContractModelIsClean(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Empty(t, Verify(contractModel(t, tc.format)))
		})
	}
}

// TestVerifyUnknownFormat proves a model whose format Verify does not know is
// reported rather than silently accepted — including the zero-value Model,
// which would otherwise match no contract table at all and so verify clean.
func TestVerifyUnknownFormat(t *testing.T) {
	t.Parallel()

	t.Run("zero value model", func(t *testing.T) {
		t.Parallel()

		findings := Verify(Model{})
		require.Equal(t, 1, findingsFor(findings, CodeUnknownFormat, ""))
		require.Len(t, findings, 1)
	})

	t.Run("contract satisfying model in an unpackaged format", func(t *testing.T) {
		t.Parallel()

		// Everything about this model satisfies the contract except the
		// format label, so the unknown-format finding cannot be a side
		// effect of some other fault.
		model := contractModel(t, FormatDeb)
		model.Format = Format("apk")

		findings := Verify(model)
		require.Equal(t, 1, findingsFor(findings, CodeUnknownFormat, ""))
		require.Len(t, findings, 1)
	})
}

// TestVerifyMissingRequiredDestination removes each required destination in
// turn, in each format, and asserts the missing-path finding for exactly that
// destination.
func TestVerifyMissingRequiredDestination(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dest := range requiredDestinations {
			t.Run(tc.name+" "+dest, func(t *testing.T) {
				t.Parallel()

				findings := Verify(withoutEntry(t, contractModel(t, tc.format), dest))
				require.Equal(t, 1, findingsFor(findings, CodeMissingPath, dest))
				require.Len(t, findings, 1)
			})
		}
	}
}

// TestVerifyBinaryInstalledElsewhere proves a binary shipped to a destination
// the contract does not name leaves its contract destination uninstalled and
// is reported, in each format.
func TestVerifyBinaryInstalledElsewhere(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, binary := range []struct{ from, to string }{
			{from: "/usr/bin/pilothouse", to: "/usr/local/bin/pilothouse"},
			{from: "/usr/bin/pilothoused", to: "/usr/local/bin/pilothoused"},
		} {
			t.Run(tc.name+" "+binary.from, func(t *testing.T) {
				t.Parallel()

				findings := Verify(relocateEntry(t, contractModel(t, tc.format), binary.from, binary.to))
				require.Equal(t, 1, findingsFor(findings, CodeMissingPath, binary.from))
				require.Len(t, findings, 1)
			})
		}
	}
}

// TestVerifyDuplicateEntry appends a second, identical entry at each required
// destination in turn, in each format, and asserts the duplicate finding for
// exactly that destination. The two binaries are covered by the same table,
// which is what satisfies O2's "a duplicate entry for either destination".
func TestVerifyDuplicateEntry(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dest := range requiredDestinations {
			t.Run(tc.name+" "+dest, func(t *testing.T) {
				t.Parallel()

				findings := Verify(duplicateEntry(t, contractModel(t, tc.format), dest, 1))
				require.Equal(t, 1, findingsFor(findings, CodeDuplicateEntry, dest))
				require.Len(t, findings, 1)
			})
		}
	}
}

// TestVerifyDuplicateEntryReportedOncePerDestination proves the multiplicity
// rule is per destination, not per extra copy: three entries at one
// destination still produce exactly one finding, because findings are matched
// by their (Code, Path) pair and the further copies would carry no
// information.
func TestVerifyDuplicateEntryReportedOncePerDestination(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const dest = "/usr/bin/pilothoused"

			model := duplicateEntry(t, contractModel(t, tc.format), dest, 2)
			require.Len(t, model.Entries, len(requiredDestinations)+2)

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeDuplicateEntry, dest))
			require.Len(t, findings, 1)
		})
	}
}

// TestVerifyWrongMode sets a wrong mode on each destination the contract pins
// a mode for, in each format, and asserts the wrong-mode finding for exactly
// that destination.
func TestVerifyWrongMode(t *testing.T) {
	t.Parallel()

	// A single wrong mode that differs from all four pinned modes, so one
	// mutation value covers the whole table.
	const wrongMode fs.FileMode = 0o644

	for _, tc := range packagedFormats {
		for _, pinned := range modePinnedDestinations {
			t.Run(tc.name+" "+pinned.dest, func(t *testing.T) {
				t.Parallel()

				require.NotEqual(t, pinned.mode, wrongMode)

				findings := Verify(withMode(t, contractModel(t, tc.format), pinned.dest, wrongMode))
				require.Equal(t, 1, findingsFor(findings, CodeWrongMode, pinned.dest))
				require.Len(t, findings, 1)
			})
		}
	}
}

// TestVerifyModeComparesPermissionBitsOnly proves the comparison ignores type
// bits: a directory entry an extractor marks with fs.ModeDir carries the
// contract's permission bits and must verify clean, not be falsely reported.
func TestVerifyModeComparesPermissionBitsOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dir := range []struct {
			dest string
			mode fs.FileMode
		}{
			{dest: "/etc/pilothouse", mode: fs.ModeDir | 0o750},
			{dest: "/etc/pilothouse/storage/credentials", mode: fs.ModeDir | 0o700},
		} {
			t.Run(tc.name+" "+dir.dest, func(t *testing.T) {
				t.Parallel()

				findings := Verify(withMode(t, contractModel(t, tc.format), dir.dest, dir.mode))
				require.Zero(t, findingsFor(findings, CodeWrongMode, dir.dest))
				require.Empty(t, findings)
			})
		}
	}
}

// TestVerifyModeNotAssertedWhereContractStatesNone proves the zero-mode rows
// really are unasserted: .goreleaser.yaml sets file_info on exactly the four
// destinations above, so changing the mode of a unit file or a binary must
// produce nothing rather than being measured against an invented default.
func TestVerifyModeNotAssertedWhereContractStatesNone(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dest := range modeFreeDestinations {
			t.Run(tc.name+" "+dest, func(t *testing.T) {
				t.Parallel()

				findings := Verify(withMode(t, contractModel(t, tc.format), dest, 0o600))
				require.Zero(t, findingsFor(findings, CodeWrongMode, dest))
				require.Empty(t, findings)
			})
		}
	}
}

// TestVerifyMissingConfigFlag clears the config designation on each
// destination the contract requires one for, in each format, and asserts the
// finding for exactly that destination.
func TestVerifyMissingConfigFlag(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dest := range configDestinations {
			t.Run(tc.name+" "+dest, func(t *testing.T) {
				t.Parallel()

				findings := Verify(withConfig(t, contractModel(t, tc.format), dest, false))
				require.Equal(t, 1, findingsFor(findings, CodeMissingConfigFlag, dest))
				require.Len(t, findings, 1)
			})
		}
	}
}

// TestVerifyUnexpectedConfigFlagIsNotAFinding pins the deliberate silence: the
// contract pins a minimum set of config-designated entries, and the code
// vocabulary has no unexpected_config_flag, so designating an entry the
// contract does not name is not reported.
func TestVerifyUnexpectedConfigFlagIsNotAFinding(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := withConfig(t, contractModel(t, tc.format), "/usr/lib/sysusers.d/pilothouse.conf", true)
			require.Empty(t, Verify(model))
		})
	}
}

// TestVerifyWrongContent perturbs the content of each byte-compared
// destination in turn, in each format, and asserts the wrong-content finding
// for exactly that destination. The perturbation is a single appended newline,
// which is what proves the comparison normalizes nothing.
func TestVerifyWrongContent(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dest := range byteComparedDestinations {
			t.Run(tc.name+" "+dest, func(t *testing.T) {
				t.Parallel()

				model := contractModel(t, tc.format)
				model = withContent(t, model, dest, perturb(contentAt(t, model, dest)))

				findings := Verify(model)
				require.Equal(t, 1, findingsFor(findings, CodeWrongContent, dest))
				require.Len(t, findings, 1)
			})
		}
	}
}

// TestVerifyNilContentIsWrongContent proves an entry the contract byte-compares
// whose Content is nil is reported rather than treated as "nothing to compare".
// An extractor that failed to capture the bytes must not verify clean, or
// #73's extractor bugs become invisible.
func TestVerifyNilContentIsWrongContent(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dest := range byteComparedDestinations {
			t.Run(tc.name+" "+dest, func(t *testing.T) {
				t.Parallel()

				findings := Verify(withContent(t, contractModel(t, tc.format), dest, nil))
				require.Equal(t, 1, findingsFor(findings, CodeWrongContent, dest))
				require.Len(t, findings, 1)
			})
		}
	}
}

// TestVerifyCrossFormatPAMPolicy is one of the two proofs this check exists
// for: a package shipping the *other* format's PAM policy installs a
// syntactically valid file at the right destination with the right mode and
// config designation, and only the byte comparison can catch it.
func TestVerifyCrossFormatPAMPolicy(t *testing.T) {
	t.Parallel()

	const dest = "/etc/pam.d/pilothouse"

	for _, tc := range []struct {
		name   string
		format Format
		source string
	}{
		{name: "rpm shipping the deb PAM policy", format: FormatRPM, source: "pilothouse.pam"},
		{name: "deb shipping the rpm PAM policy", format: FormatDeb, source: "rpm/pilothouse.pam"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := withContent(t, contractModel(t, tc.format), dest, sourceBytes(tc.source))

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeWrongContent, dest))
			require.Len(t, findings, 1)
		})
	}
}

// TestVerifyCrossFormatBrokerUnit is the other cross-format proof: the two
// packaged broker units differ only in their --admin-group default, so a
// package shipping the wrong one starts cleanly and grants the wrong group.
func TestVerifyCrossFormatBrokerUnit(t *testing.T) {
	t.Parallel()

	const dest = "/usr/lib/systemd/system/pilothoused.service"

	for _, tc := range []struct {
		name   string
		format Format
		source string
	}{
		{name: "rpm shipping the deb broker unit", format: FormatRPM, source: "deb/pilothoused.service"},
		{name: "deb shipping the rpm broker unit", format: FormatDeb, source: "rpm/pilothoused.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := withContent(t, contractModel(t, tc.format), dest, sourceBytes(tc.source))

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeWrongContent, dest))
			require.Len(t, findings, 1)
		})
	}
}

// TestVerifyContentNotComparedWhereContractStatesNoSource pins the deliberate
// exclusion: the two binaries and the two directories carry no source in the
// contract, so whatever an extractor records as their content — arbitrary
// bytes or none at all — must produce nothing.
func TestVerifyContentNotComparedWhereContractStatesNoSource(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		for _, dest := range contentFreeDestinations {
			t.Run(tc.name+" "+dest+" arbitrary bytes", func(t *testing.T) {
				t.Parallel()

				model := withContent(t, contractModel(t, tc.format), dest, []byte("bytes from some other build"))

				findings := Verify(model)
				require.Zero(t, findingsFor(findings, CodeWrongContent, dest))
				require.Empty(t, findings)
			})

			t.Run(tc.name+" "+dest+" nil", func(t *testing.T) {
				t.Parallel()

				// Reached by way of arbitrary bytes so the mutation is never
				// vacuous: the two directory entries already carry nil content
				// in the fixture.
				model := withContent(t, contractModel(t, tc.format), dest, []byte("bytes from some other build"))
				model = withContent(t, model, dest, nil)

				findings := Verify(model)
				require.Zero(t, findingsFor(findings, CodeWrongContent, dest))
				require.Empty(t, findings)
			})
		}
	}
}

// TestVerifyDependencyFaults is N2's mutation table: each of the five faults a
// dependency list must be rejected for, in each format — ten cases. Every case
// asserts a dependency_mismatch finding and pins the total number of findings,
// so a mutation may not produce a second, unrelated one.
//
// The alternative case expects two: rewriting an element as an alternative
// also breaks the sorted comparison, and N2 makes the two rejection reasons
// independent, so both are reported. The dedicated alternatives test below is
// what proves which finding is which.
func TestVerifyDependencyFaults(t *testing.T) {
	t.Parallel()

	for _, tc := range dependencyFaults {
		for _, mutation := range []struct {
			name  string
			apply func(*testing.T, Model) Model
			want  int
		}{{
			name:  "missing element",
			apply: func(t *testing.T, m Model) Model { t.Helper(); return withoutDependency(t, m, tc.drop) },
			want:  1,
		}, {
			name:  "extra element",
			apply: func(t *testing.T, m Model) Model { t.Helper(); return withExtraDependency(t, m, tc.extra) },
			want:  1,
		}, {
			name:  "duplicated element",
			apply: func(t *testing.T, m Model) Model { t.Helper(); return withDuplicatedDependency(t, m, tc.duplicate) },
			want:  1,
		}, {
			name: "misspelled element",
			apply: func(t *testing.T, m Model) Model {
				t.Helper()

				return withRenamedDependency(t, m, tc.misspelledFrom, tc.misspelledTo)
			},
			want: 1,
		}, {
			name: "alternative-containing expression",
			apply: func(t *testing.T, m Model) Model {
				t.Helper()

				return withRenamedDependency(t, m, tc.alternativeFrom, tc.alternativeTo)
			},
			want: 2,
		}} {
			t.Run(tc.name+" "+mutation.name, func(t *testing.T) {
				t.Parallel()

				findings := Verify(mutation.apply(t, contractModel(t, tc.format)))
				require.Equal(t, mutation.want, findingsFor(findings, CodeDependencyMismatch, ""))
				require.Len(t, findings, mutation.want)
			})
		}
	}
}

// TestVerifyDependencyOrderIsNotAsserted proves the comparison is
// order-independent: nothing in the contract fixes the order the packaging
// metadata lists dependencies in, so the fixture's list reversed — a fixed
// permutation, not a random shuffle, so the test is deterministic — must
// verify clean.
func TestVerifyDependencyOrderIsNotAsserted(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := contractModel(t, tc.format)

			reversed := slices.Clone(model.Dependencies)
			slices.Reverse(reversed)
			require.NotEqual(t, model.Dependencies, reversed, "reversing should be a real permutation")

			model.Dependencies = reversed

			require.Empty(t, Verify(model))
		})
	}
}

// TestVerifyDependencyMultiplicityIsCompared proves the comparison is on
// slices rather than set membership: replacing one element with a duplicate of
// another keeps the list's length and merely drops one distinct name, which a
// set comparison built on "every declared name is expected" would accept.
func TestVerifyDependencyMultiplicityIsCompared(t *testing.T) {
	t.Parallel()

	for _, tc := range dependencyFaults {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := contractModel(t, tc.format)
			model := withRenamedDependency(t, fixture, tc.collapsedFrom, tc.collapsedTo)

			// The mutation is exactly the one described: same length, one
			// fewer distinct name, and every declared name still one the
			// contract expects.
			require.Len(t, model.Dependencies, len(fixture.Dependencies))
			require.Len(t, slices.Compact(slices.Sorted(slices.Values(model.Dependencies))),
				len(fixture.Dependencies)-1)

			for _, dep := range model.Dependencies {
				require.Contains(t, fixture.Dependencies, dep)
			}

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeDependencyMismatch, ""))
			require.Len(t, findings, 1)
		})
	}
}

// TestVerifyDependencyAlternativeIsReportedSeparatelyFromTheSortedComparison
// proves the alternatives rule is an independent rejection reason and not a
// side effect of the list comparison: exactly one element of an otherwise
// correct list is rewritten to offer an alternative, and Verify reports the
// sorted-slice mismatch AND a second finding naming that expression.
//
// This is the one place this package's tests read a Message. They match only
// on the expression the finding must name — the criterion N2 states — never on
// the surrounding wording, which stays unstable.
func TestVerifyDependencyAlternativeIsReportedSeparatelyFromTheSortedComparison(t *testing.T) {
	t.Parallel()

	for _, tc := range dependencyFaults {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, tc.alternativeTo, " | ", "the mutation should offer an alternative")

			model := withRenamedDependency(t, contractModel(t, tc.format), tc.alternativeFrom, tc.alternativeTo)

			findings := Verify(model)
			require.Equal(t, 2, findingsFor(findings, CodeDependencyMismatch, ""))
			require.Len(t, findings, 2)

			naming := 0

			for _, finding := range findings {
				if strings.Contains(finding.Message, tc.alternativeTo) {
					naming++
				}
			}

			// One of the two names the expression on its own; the other is the
			// whole-list comparison, which happens to quote it inside the got
			// list as well, so at least one is the assertion that holds.
			require.GreaterOrEqual(t, naming, 1,
				"a finding should name the alternative-containing expression")
		})
	}
}

// TestVerifyDependencyFindingsAreNotPathScoped pins O1's "empty where not
// path-scoped" for this code: a dependency concerns no destination, so every
// dependency finding — from either rejection reason — carries an empty Path.
func TestVerifyDependencyFindingsAreNotPathScoped(t *testing.T) {
	t.Parallel()

	for _, tc := range dependencyFaults {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Both faults at once, so both shapes of dependency finding are
			// covered by the assertion below.
			model := withExtraDependency(t, contractModel(t, tc.format), tc.extra)
			model = withRenamedDependency(t, model, tc.alternativeFrom, tc.alternativeTo)

			findings := Verify(model)
			require.Equal(t, 2, findingsFor(findings, CodeDependencyMismatch, ""))
			require.Len(t, findings, 2)

			for _, finding := range findings {
				require.Equal(t, CodeDependencyMismatch, finding.Code)
				require.Empty(t, finding.Path, "a dependency finding is not path-scoped")
			}
		})
	}
}

// TestVerifyAccumulatesDependencyAndMissingPath proves the dependency check
// accumulates with the path-scoped checks rather than either masking the
// other: a non-path-scoped finding and a path-scoped one coexist.
func TestVerifyAccumulatesDependencyAndMissingPath(t *testing.T) {
	t.Parallel()

	const missingDest = "/usr/lib/sysusers.d/pilothouse.conf"

	for _, tc := range dependencyFaults {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := withoutDependency(t, contractModel(t, tc.format), tc.drop)
			model = withoutEntry(t, model, missingDest)

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeDependencyMismatch, ""))
			require.Equal(t, 1, findingsFor(findings, CodeMissingPath, missingDest))
			require.Len(t, findings, 2)
		})
	}
}

// TestVerifyAccumulatesWrongContentAndMissing proves the content check
// accumulates with the presence check rather than either masking the other.
func TestVerifyAccumulatesWrongContentAndMissing(t *testing.T) {
	t.Parallel()

	const (
		wrongDest   = "/etc/pilothouse/pilothouse.env"
		missingDest = "/usr/lib/sysusers.d/pilothouse.conf"
	)

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := contractModel(t, tc.format)
			model = withContent(t, model, wrongDest, perturb(contentAt(t, model, wrongDest)))
			model = withoutEntry(t, model, missingDest)

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeWrongContent, wrongDest))
			require.Equal(t, 1, findingsFor(findings, CodeMissingPath, missingDest))
			require.Len(t, findings, 2)
		})
	}
}

// TestVerifyAccumulatesDuplicateAndMissing proves the new checks accumulate
// with the presence check rather than either masking the other.
func TestVerifyAccumulatesDuplicateAndMissing(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := duplicateEntry(t, contractModel(t, tc.format), "/usr/bin/pilothouse", 1)
			model = withoutEntry(t, model, "/usr/lib/sysusers.d/pilothouse.conf")

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeDuplicateEntry, "/usr/bin/pilothouse"))
			require.Equal(t, 1, findingsFor(findings, CodeMissingPath, "/usr/lib/sysusers.d/pilothouse.conf"))
			require.Len(t, findings, 2)
		})
	}
}

// TestVerifyDuplicateDoesNotSuppressTheOtherChecks proves a duplicated
// destination is still evaluated for mode, config designation and content:
// Verify reads the first entry installing there and accumulates all four
// findings.
func TestVerifyDuplicateDoesNotSuppressTheOtherChecks(t *testing.T) {
	t.Parallel()

	const dest = "/etc/pilothouse/pilothoused.env"

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := withMode(t, contractModel(t, tc.format), dest, 0o644)
			model = withConfig(t, model, dest, false)
			model = withContent(t, model, dest, perturb(contentAt(t, model, dest)))
			model = duplicateEntry(t, model, dest, 1)

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeDuplicateEntry, dest))
			require.Equal(t, 1, findingsFor(findings, CodeWrongMode, dest))
			require.Equal(t, 1, findingsFor(findings, CodeMissingConfigFlag, dest))
			require.Equal(t, 1, findingsFor(findings, CodeWrongContent, dest))
			require.Len(t, findings, 4)
		})
	}
}

// TestVerifyAccumulatesUnrelatedFaults proves Verify does not short-circuit:
// two unrelated destinations removed at once produce both findings, not just
// the first.
func TestVerifyAccumulatesUnrelatedFaults(t *testing.T) {
	t.Parallel()

	for _, tc := range packagedFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := withoutEntry(t, contractModel(t, tc.format), "/etc/pam.d/pilothouse")
			model = withoutEntry(t, model, "/usr/bin/pilothoused")

			findings := Verify(model)
			require.Equal(t, 1, findingsFor(findings, CodeMissingPath, "/etc/pam.d/pilothouse"))
			require.Equal(t, 1, findingsFor(findings, CodeMissingPath, "/usr/bin/pilothoused"))
			require.Len(t, findings, 2)
		})
	}
}

// TestSourceBytes proves every embedded repository source is present and
// non-empty, so a fixture built from them can never be silently empty.
func TestSourceBytes(t *testing.T) {
	t.Parallel()

	for _, name := range sourceNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NotEmpty(t, sourceBytes(name))
		})
	}
}

// TestSourcesHoldsExactlyTheDeclaredNames proves sourceNames and the //go:embed
// patterns above it describe the same set of files, so a source added to one
// and forgotten in the other fails here.
func TestSourcesHoldsExactlyTheDeclaredNames(t *testing.T) {
	t.Parallel()

	var embedded []string
	require.NoError(t, fs.WalkDir(sources, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			embedded = append(embedded, path)
		}

		return nil
	}))

	require.ElementsMatch(t, sourceNames, embedded)
	require.Len(t, sourceNames, 9)
}

// TestSourceBytesPanicsOnUnembeddedName documents the deliberate panic: the
// set of embedded names is fixed at compile time and never derived from a
// Model, so a miss is a programming error rather than runtime input.
func TestSourceBytesPanicsOnUnembeddedName(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() { sourceBytes("deb/pilothouse.pam") })
}
