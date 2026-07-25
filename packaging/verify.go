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
// The assertions Verify makes at this commit are exactly two:
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

	installed := make(map[string]bool, len(m.Entries))
	for _, entry := range m.Entries {
		installed[entry.Dest] = true
	}

	for _, req := range reqs {
		if !installed[req.dest] {
			findings = append(findings, Finding{
				Code:    CodeMissingPath,
				Path:    req.dest,
				Message: fmt.Sprintf("the package installs nothing to the required destination %s", req.dest),
			})
		}
	}

	return findings
}
