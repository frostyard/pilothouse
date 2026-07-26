package packaging

// Finding is a single contract violation.
type Finding struct {
	// Code is a stable, exported string identifying the kind of violation.
	// This package's tests and cmd/verify-packages both key off these
	// values, so a
	// code's string is part of the exported contract and may not change
	// without breaking downstream consumers.
	Code string
	// Path is the destination path the finding concerns. It is empty for
	// findings that are not path-scoped, such as a dependency mismatch or a
	// missing scriptlet.
	Path string
	// Message is a human-readable explanation. It is unstable: nothing may
	// key off its wording.
	Message string
}

// The code vocabulary. These string values are the exported contract; the
// golden test in finding_test.go pins each one literally so a rename fails
// here rather than silently downstream.
const (
	// CodeMissingPath reports a required destination the package does not
	// install.
	CodeMissingPath = "missing_path"
	// CodeWrongMode reports an entry installed with an unexpected file mode.
	CodeWrongMode = "wrong_mode"
	// CodeWrongContent reports an entry whose bytes differ from the
	// repository source it is built from.
	CodeWrongContent = "wrong_content"
	// CodeMissingConfigFlag reports an entry that must be designated a
	// configuration file but is not.
	CodeMissingConfigFlag = "missing_config_flag"
	// CodeDependencyMismatch reports a declared dependency list that does not
	// match the expected set for the format. It is not path-scoped.
	CodeDependencyMismatch = "dependency_mismatch"
	// CodeForbiddenPath reports an entry installed to a destination the
	// package must never own.
	CodeForbiddenPath = "forbidden_path"
	// CodeDuplicateEntry reports more than one entry installing to the same
	// destination.
	CodeDuplicateEntry = "duplicate_entry"

	// CodeMissingScriptlet reports a package that ships no postinstall
	// scriptlet. It is an addition to O1's explicitly-minimal list: the
	// finding is not path-scoped, so reporting it as CodeMissingPath would
	// require inventing a fake Path for a fault that concerns no destination.
	CodeMissingScriptlet = "missing_scriptlet"
	// CodeUnknownFormat reports a Model whose Format is neither FormatDeb nor
	// FormatRPM. It is likewise an addition to O1's list: without it the
	// zero-value Model — whose empty Format matches no contract at all —
	// could verify clean, and a model that verifies clean by accident is
	// worse than one that fails loudly.
	CodeUnknownFormat = "unknown_format"
)
