package packaging

import "fmt"

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
// The assertions Verify makes at this commit are exactly five:
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
//
// Two deliberate silences:
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
//
// A duplicate does not suppress the other checks for its destination: Verify
// evaluates the first entry installing there for mode and config designation,
// and still accumulates.
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
	}

	return findings
}
