package packaging

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFindingCodeValues pins the exact string of every finding code.
//
// This is not a tautology: the strings are the exported contract that this
// package's own tests and #73's CLI key off, so renaming a code's value must
// fail here rather than silently break a downstream consumer that matches on
// the old text.
func TestFindingCodeValues(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: "CodeMissingPath", got: CodeMissingPath, want: "missing_path"},
		{name: "CodeWrongMode", got: CodeWrongMode, want: "wrong_mode"},
		{name: "CodeWrongContent", got: CodeWrongContent, want: "wrong_content"},
		{name: "CodeMissingConfigFlag", got: CodeMissingConfigFlag, want: "missing_config_flag"},
		{name: "CodeDependencyMismatch", got: CodeDependencyMismatch, want: "dependency_mismatch"},
		{name: "CodeForbiddenPath", got: CodeForbiddenPath, want: "forbidden_path"},
		{name: "CodeDuplicateEntry", got: CodeDuplicateEntry, want: "duplicate_entry"},
		{name: "CodeMissingScriptlet", got: CodeMissingScriptlet, want: "missing_scriptlet"},
		{name: "CodeUnknownFormat", got: CodeUnknownFormat, want: "unknown_format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, tc.got)
		})
	}
}

// TestFindingCodesAreDistinct proves no two codes collapse onto the same
// string. Two codes sharing a value would make findings indistinguishable to
// any consumer that switches on Code.
func TestFindingCodesAreDistinct(t *testing.T) {
	t.Parallel()

	codes := []string{
		CodeMissingPath,
		CodeWrongMode,
		CodeWrongContent,
		CodeMissingConfigFlag,
		CodeDependencyMismatch,
		CodeForbiddenPath,
		CodeDuplicateEntry,
		CodeMissingScriptlet,
		CodeUnknownFormat,
	}
	require.Len(t, codes, 9)

	seen := make(map[string]int, len(codes))
	for i, code := range codes {
		require.NotEmpty(t, code)
		if first, dup := seen[code]; dup {
			t.Fatalf("codes %d and %d share the value %q", first, i, code)
		}
		seen[code] = i
	}
	require.Len(t, seen, len(codes))
}
