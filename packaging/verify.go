package packaging

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// Verify reports every way m violates the packaging contract.
//
// Verify ACCUMULATES. No check returns early and no check is skipped because
// an earlier one produced a finding, so the returned slice holds every
// independent violation the model exhibits. A nil or empty result means m
// satisfies every assertion implemented so far.
//
// Verify is pure: it reads only m and the repository sources compiled into
// this package, opens no file and runs no command.
//
// The assertions Verify makes at this commit are exactly nine:
//
//   - CodeUnknownFormat (not path-scoped, so Path is empty) when m.Format is
//     neither FormatDeb nor FormatRPM. Without it a zero-value Model would
//     match no contract table and so verify clean, which is the worst failure
//     mode a verifier can have. An unknown format yields no other finding: the
//     contract for such a package is unknown, not violated.
//   - CodeMissingPath, with Path set to the destination, for every destination
//     the format's contract requires that no entry in m.Entries installs to.
//     An entry installed somewhere else — a binary under /usr/local/bin, say —
//     leaves its contract destination uninstalled and is reported this way.
//   - CodeDuplicateEntry, with Path set to the destination, when two or more
//     entries install to the same required destination. Exactly ONE finding is
//     emitted per duplicated destination however many copies there are:
//     findings are identified by their (Code, Path) pair, so the N-1 further
//     identical findings a per-copy rule would emit carry no information.
//   - CodeWrongMode, with Path set to the destination, when the entry's
//     permission bits differ from the mode the contract pins. Only four
//     destinations carry a mode; a requirement whose mode is zero states no
//     mode and is not asserted (see requirement.mode).
//   - CodeMissingConfigFlag, with Path set to the destination, when an entry
//     the contract requires to be config-designated is not. Only three
//     destinations require it.
//   - CodeWrongContent, with Path set to the destination and a Message naming
//     the repository source expected there, when the entry's bytes are not
//     exactly the bytes of the embedded source the contract builds that
//     destination from. Six destinations are compared this way; a requirement
//     whose source is empty compares no content (see requirement.source).
//   - CodeDependencyMismatch, with Path empty because a dependency concerns no
//     destination, in two independent shapes. One finding, naming got and
//     want, when the sorted declared list is not equal to the sorted list the
//     format's contract requires — so a missing, extra, duplicated or
//     misspelled element all fail. One further finding per declared expression
//     containing "|", naming that expression, because the contract requires
//     simple package names and a Debian alternative such as
//     "libc6 | libc6-udeb" satisfies the requirement only by accident of which
//     alternative the resolver picks. The two are independent: a list carrying
//     both faults reports both.
//   - CodeMissingScriptlet, with Path empty because a scriptlet concerns no
//     destination, when m.Postinstall is nil — the model's representation of
//     "this package ships no postinstall scriptlet". Ownership of the
//     package-owned configuration paths is repaired by that script and by
//     nothing else, so its absence is a contract violation in its own right
//     and not merely a missing file.
//   - CodeWrongContent, with Path likewise EMPTY and a Message naming
//     packaging/postinstall.sh, when the scriptlet's bytes are not the bytes of
//     that embedded repository source. This is the only CodeWrongContent
//     finding that is not path-scoped; a consumer distinguishing the two reads
//     Path, and a consumer distinguishing "no scriptlet" from "wrong scriptlet"
//     reads the Code.
//
// Three deliberate silences:
//
//   - Modes are compared on permission bits only (Entry.Mode.Perm()). An
//     extractor that sets fs.ModeDir on a directory entry — which a real one
//     may well do for /etc/pilothouse and /etc/pilothouse/storage/credentials
//     — must not be falsely reported, and the type bits are not part of the
//     contract in any case.
//   - A Config designation on an entry the contract does NOT require to be
//     config is not a finding. The contract pins a minimum, and the code
//     vocabulary has no unexpected_config_flag: missing_config_flag is the
//     only asserted direction.
//   - Content is never compared at the four destinations whose requirement
//     carries no source: the two binaries, whose bytes differ per build, and
//     the two directories, which have no content. Whatever an extractor
//     records there — arbitrary bytes or none — is not a finding.
//
// The PAYLOAD content comparison is exact bytes.Equal against the embedded
// source, with no normalization of any kind: an entry extracted from a real
// archive carries the shipped bytes verbatim, so even one added byte is a
// difference worth reporting. A byte-compared entry whose Content is nil is
// likewise reported, because every embedded source is non-empty: an extractor
// that failed to capture the bytes must not verify clean.
//
// The SCRIPTLET comparison is the single exception, and its normalization is
// deliberately as narrow as it can be: at most one trailing "\n" is stripped
// from each side before the same exact bytes.Equal. See trimFinalNewline for
// why, and for the asymmetry that follows.
//
// Nothing in the scriptlet check tokenizes shell, applies a regular expression
// to the script, or looks for any individual command inside it. That is not an
// omission to be filled in later: the script's fail-fast, idempotent,
// presence-guarded behavior is already proven by running the real script in
// packaging/postinstall_test.go, and all this package has to prove is that the
// artifact ships exactly those bytes.
//
// The dependency comparison is on SORTED CLONES, so it is order-independent —
// nothing in the contract fixes the order the packaging metadata lists
// dependencies in — but multiplicity-sensitive: it compares slices, not set
// membership, so a list that repeats one name and omits another is reported
// even though its set of names would still be a subset of the contract's.
// Neither clone is written back: m is never mutated.
//
// A duplicate does not suppress the other checks for its destination: Verify
// evaluates the first entry installing there for mode, config designation and
// content, and still accumulates.
//
// Nothing else is asserted yet; further assertions arrive in later changes.
func Verify(m Model) []Finding {
	var findings []Finding

	reqs, known := requirements(m.Format)
	if !known {
		findings = append(findings, Finding{
			Code: CodeUnknownFormat,
			Message: fmt.Sprintf(
				"unknown package format %q: expected %q or %q",
				string(m.Format), string(FormatDeb), string(FormatRPM),
			),
		})
	}

	// installs counts the entries per destination; first records the earliest
	// entry installing to each, which is the one the per-entry checks read.
	installs := make(map[string]int, len(m.Entries))
	first := make(map[string]Entry, len(m.Entries))
	for _, entry := range m.Entries {
		installs[entry.Dest]++
		if installs[entry.Dest] == 1 {
			first[entry.Dest] = entry
		}
	}

	for _, req := range reqs {
		if installs[req.dest] == 0 {
			findings = append(findings, Finding{
				Code:    CodeMissingPath,
				Path:    req.dest,
				Message: fmt.Sprintf("the package installs nothing to the required destination %s", req.dest),
			})

			continue
		}

		if installs[req.dest] > 1 {
			findings = append(findings, Finding{
				Code: CodeDuplicateEntry,
				Path: req.dest,
				Message: fmt.Sprintf(
					"the package installs %d entries to %s; the contract requires exactly one",
					installs[req.dest], req.dest,
				),
			})
		}

		entry := first[req.dest]

		if req.mode != 0 && entry.Mode.Perm() != req.mode {
			findings = append(findings, Finding{
				Code: CodeWrongMode,
				Path: req.dest,
				Message: fmt.Sprintf(
					"%s is installed with mode %#o; the contract requires %#o",
					req.dest, entry.Mode.Perm(), req.mode,
				),
			})
		}

		if req.config && !entry.Config {
			findings = append(findings, Finding{
				Code: CodeMissingConfigFlag,
				Path: req.dest,
				Message: fmt.Sprintf(
					"%s is not designated a configuration file by the packaging metadata",
					req.dest,
				),
			})
		}

		if req.source != "" && !bytes.Equal(entry.Content, sourceBytes(req.source)) {
			findings = append(findings, Finding{
				Code: CodeWrongContent,
				Path: req.dest,
				Message: fmt.Sprintf(
					"%s does not carry the bytes of the repository source packaging/%s",
					req.dest, req.source,
				),
			})
		}
	}

	// Gated on the format being known, for the same reason the requirement
	// loop is: the contract for an unrecognised format is unknown, not
	// violated, so CodeUnknownFormat stays the only finding such a model gets.
	if want, known := contractDependencies(m.Format); known {
		findings = append(findings, dependencyFindings(m.Dependencies, want)...)
	}

	// Gated on the format being known for the same reason the two blocks above
	// are. The scriptlet requirement is itself format-independent — nfpm's
	// scripts key is not per-format, so the same script is the deb postinst
	// and the rpm %post — but a model whose format this package does not
	// recognise describes a package whose contract is unknown rather than
	// violated, and CodeUnknownFormat stays the only finding such a model gets.
	if known {
		findings = append(findings, postinstallFindings(m.Postinstall)...)
	}

	return findings
}

// postinstallFindings reports every way the model's postinstall scriptlet
// violates the contract.
//
// The two faults are mutually exclusive and use distinct codes. A nil
// scriptlet is CodeMissingScriptlet rather than CodeMissingPath: O1's code
// list is explicitly a minimum, the scriptlet is not path-scoped so
// CodeMissingPath would have to invent a Path for it, and #73's CLI has to
// tell "this package ships no scriptlet" apart from "this package ships the
// wrong scriptlet". Differing bytes reuse CodeWrongContent, because that is
// exactly what they are — the finding is distinguished from a payload one by
// its empty Path.
//
// Both findings carry an empty Path: a scriptlet has no destination.
func postinstallFindings(scriptlet *Scriptlet) []Finding {
	if scriptlet == nil {
		return []Finding{{
			Code: CodeMissingScriptlet,
			Message: fmt.Sprintf(
				"the package ships no postinstall scriptlet; "+
					"the contract requires the repository source packaging/%s",
				postinstallSource,
			),
		}}
	}

	if bytes.Equal(trimFinalNewline(scriptlet.Content), trimFinalNewline(sourceBytes(postinstallSource))) {
		return nil
	}

	return []Finding{{
		Code: CodeWrongContent,
		Message: fmt.Sprintf(
			"the postinstall scriptlet does not carry the bytes of the repository source packaging/%s",
			postinstallSource,
		),
	}}
}

// trimFinalNewline strips AT MOST ONE trailing "\n" from b.
//
// This is the whole of the scriptlet comparison's normalization, and its
// narrowness is the point. It exists for exactly one reason: whether a script
// is shipped with or without a final newline is not a contract violation, and
// an extractor may reasonably present it either way. packaging/postinstall.sh
// ends with exactly one "\n", so the rule has three consequences, all of them
// intended:
//
//   - the script as it stands in the repository verifies clean;
//   - the same script with its final newline removed verifies clean;
//   - the same script with one further newline appended is CodeWrongContent.
//
// The asymmetry in the third case is not an oversight. The rejected
// alternative, bytes.TrimRight(b, "\n"), would strip ALL trailing newlines and
// so silently accept a script padded with arbitrary blank lines — a real byte
// difference in a file whose entire contract is "ships exactly these bytes",
// and the only assertion this package makes about the scriptlet's contents.
func trimFinalNewline(b []byte) []byte {
	return bytes.TrimSuffix(b, []byte("\n"))
}

// dependencyFindings reports every way the declared dependency list got
// violates the contract list want.
//
// The two checks are deliberately independent, and a list exhibiting both
// faults produces one finding from each: an otherwise-correct list in which a
// single element has been rewritten as an alternative fails the sorted
// comparison too, but the alternative must be named on its own so the reason
// it is rejected is not buried in a whole-list diff.
//
// Both findings carry an empty Path: a dependency concerns no destination.
func dependencyFindings(got, want []string) []Finding {
	var findings []Finding

	gotSorted := slices.Sorted(slices.Values(got))
	wantSorted := slices.Sorted(slices.Values(want))

	if !slices.Equal(gotSorted, wantSorted) {
		findings = append(findings, Finding{
			Code: CodeDependencyMismatch,
			Message: fmt.Sprintf(
				"the package declares dependencies %q; the contract requires exactly %q",
				gotSorted, wantSorted,
			),
		})
	}

	for _, dep := range got {
		if !strings.Contains(dep, "|") {
			continue
		}

		findings = append(findings, Finding{
			Code: CodeDependencyMismatch,
			Message: fmt.Sprintf(
				"the dependency expression %q offers an alternative; "+
					"the contract requires plain package names",
				dep,
			),
		})
	}

	return findings
}
