package packaging

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The job tests below check an action by its name and the 40-hex form of its
// ref, never by a SHA written here (core ADR-0088, ADR-0090). The version
// comment and the 1 SHA for each action across every workflow are checked
// once, repository-wide, by internal/workflowcheck/actions_test.go; the
// commit itself is checked by the owner's review of each bump pull request.

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// actionOf returns the action of a uses: value pinned to a full commit SHA, or
// "" for any other value (a tag, a branch, a short SHA, a local path).
func actionOf(uses string) string {
	action, ref, ok := strings.Cut(uses, "@")
	if !ok || !commitSHA.MatchString(ref) {
		return ""
	}
	return action
}

// requireUsesAction fails unless uses holds action at a full commit SHA.
func requireUsesAction(t *testing.T, uses []string, action, why string) {
	t.Helper()
	for _, ref := range uses {
		if actionOf(ref) == action {
			return
		}
	}
	require.Failf(t, "missing pinned action",
		"want %s@<40-hex commit SHA> in %q: %s", action, uses, why)
}

func TestActionOf(t *testing.T) {
	t.Parallel()

	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct{ uses, want string }{
		{"actions/checkout@" + sha, "actions/checkout"},
		{"frostyard/repogen/.github/actions/publish-to-r2@" + sha, "frostyard/repogen/.github/actions/publish-to-r2"},
		{"actions/checkout@v7", ""},
		{"actions/checkout@" + sha[:39], ""},
		{"actions/checkout@" + strings.ToUpper(sha), ""},
		{"actions/checkout", ""},
		{"./.github/actions/check", ""},
	} {
		require.Equalf(t, tc.want, actionOf(tc.uses), "actionOf(%q)", tc.uses)
	}
}
