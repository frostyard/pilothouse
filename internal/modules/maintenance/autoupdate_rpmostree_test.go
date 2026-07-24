package maintenance

import (
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoUpdateRPMOStreeSourcePath is the file the two mechanical tests below
// inspect.
const autoUpdateRPMOStreeSourcePath = "autoupdate_rpmostree.go"

// rpmOStreePolicyVocabulary is the closed set ParseRPMOStreeAutomaticUpdatePolicy
// may return. Every table case below asserts membership in it, so a normalizer
// that invented a sixth value would fail even if its individual case expectation
// were updated to match.
var rpmOStreePolicyVocabulary = []string{
	RPMOStreePolicyNone,
	RPMOStreePolicyCheck,
	RPMOStreePolicyStage,
	RPMOStreePolicyApply,
	RPMOStreePolicyCustomUnknown,
}

// TestParseRPMOStreeAutomaticUpdatePolicy walks the full mapping matrix.
//
// Rows one through six are rpm-ostree's own accepted spellings, taken from
// rpmostree_str_to_auto_update_policy in src/libpriv/rpmostree-util.cxx:
// none/off, check, stage/ex-stage, apply. The remaining rows are every way the
// answer can fail to be one of those: a value rpm-ostree itself would reject, a
// file with no AutomaticUpdatePolicy key, empty input, and nil input -- all of
// which normalize to custom/unknown rather than to rpm-ostree's own "absent
// means none" default, per the spec.
//
// The returned policy is assigned onto a real RPMOStreeAutoUpdate rather than
// asserted as a bare return value, so the test proves the normalizer's output
// is exactly what the wire type's Policy field carries.
func TestParseRPMOStreeAutomaticUpdatePolicy(t *testing.T) {
	tests := []struct {
		name       string
		config     []byte
		wantPolicy string
		why        string
	}{
		{
			name:       "none",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=none\n"),
			wantPolicy: RPMOStreePolicyNone,
			why:        "rpm-ostree's own \"none\"",
		},
		{
			name:       "off is an alias for none",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=off\n"),
			wantPolicy: RPMOStreePolicyNone,
			why:        "rpmostree_str_to_auto_update_policy accepts off alongside none",
		},
		{
			name:       "check",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=check\n"),
			wantPolicy: RPMOStreePolicyCheck,
			why:        "check only reports an available update",
		},
		{
			name:       "stage",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=stage\n"),
			wantPolicy: RPMOStreePolicyStage,
			why:        "stage downloads and deploys for the next boot",
		},
		{
			name:       "ex-stage is an alias for stage",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=ex-stage\n"),
			wantPolicy: RPMOStreePolicyStage,
			why:        "ex-stage is rpm-ostree's backwards-compatibility spelling of stage",
		},
		{
			name:       "apply",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=apply\n"),
			wantPolicy: RPMOStreePolicyApply,
			why:        "apply updates the booted deployment automatically",
		},
		{
			name:       "unrecognized value",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=bogus\n"),
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "rpm-ostree itself rejects this value, so Pilothouse cannot say what the daemon is doing",
		},
		{
			name:       "no AutomaticUpdatePolicy key at all",
			config:     []byte("[Daemon]\nIdleExitTimeout=60\nLockLayering=false\n"),
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "an absent key normalizes to unknown, deliberately not to rpm-ostree's own none default",
		},
		{
			name:       "empty input",
			config:     []byte{},
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "an empty body carries no observation of the daemon's policy",
		},
		{
			name:       "nil input",
			config:     nil,
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "nil is how the caller reports an absent or unreadable file",
		},
		{
			name:       "empty value",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=\n"),
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "a present but empty value is not one rpm-ostree accepts",
		},
		{
			name:       "value case is significant",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=None\n"),
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "rpm-ostree compares with g_str_equal, so None is not none",
		},
		{
			name:       "key outside the Daemon group is not the daemon's policy",
			config:     []byte("[Experimental]\nAutomaticUpdatePolicy=apply\n"),
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "the daemon reads DAEMON_CONFIG_GROUP \"Daemon\" only",
		},
		{
			name:       "key before any group header is ignored",
			config:     []byte("AutomaticUpdatePolicy=apply\n[Daemon]\nIdleExitTimeout=60\n"),
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "a key-value pair outside any group belongs to no group",
		},
		{
			name:       "the Daemon group after another group is still found",
			config:     []byte("[Experimental]\nAutomaticUpdatePolicy=bogus\n\n[Daemon]\nAutomaticUpdatePolicy=check\n"),
			wantPolicy: RPMOStreePolicyCheck,
			why:        "group tracking must switch back on, not latch off",
		},
		{
			name:       "comments and blank lines are skipped",
			config:     []byte("# rpm-ostreed configuration\n\n[Daemon]\n# AutomaticUpdatePolicy=apply\nAutomaticUpdatePolicy=stage\n"),
			wantPolicy: RPMOStreePolicyStage,
			why:        "a commented-out line is not a setting",
		},
		{
			name:       "whitespace around the key and value is ignored",
			config:     []byte("  [Daemon]  \n\tAutomaticUpdatePolicy   =   apply   \n"),
			wantPolicy: RPMOStreePolicyApply,
			why:        "key-file syntax ignores whitespace around the separator",
		},
		{
			name:       "CRLF line endings",
			config:     []byte("[Daemon]\r\nAutomaticUpdatePolicy=check\r\n"),
			wantPolicy: RPMOStreePolicyCheck,
			why:        "a stray carriage return must not become part of the value",
		},
		{
			name:       "no trailing newline",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=apply"),
			wantPolicy: RPMOStreePolicyApply,
			why:        "the last line counts even without a terminator",
		},
		{
			name:       "a repeated key takes its last spelling",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicy=apply\nAutomaticUpdatePolicy=none\n"),
			wantPolicy: RPMOStreePolicyNone,
			why:        "GLib's key-file parser replaces the earlier entry, so the last one is what the daemon loaded",
		},
		{
			name:       "a similarly named key is not the policy key",
			config:     []byte("[Daemon]\nAutomaticUpdatePolicyOverride=apply\n"),
			wantPolicy: RPMOStreePolicyCustomUnknown,
			why:        "the key name must match exactly, not by prefix",
		},
		{
			name: "a realistic shipped configuration",
			config: []byte(`# Configuration file for rpm-ostreed

[Daemon]
#AutomaticUpdatePolicy=none
AutomaticUpdatePolicy=stage
IdleExitTimeout=60
`),
			wantPolicy: RPMOStreePolicyStage,
			why:        "the whole file parses to the one live setting",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := ParseRPMOStreeAutomaticUpdatePolicy(test.config)

			status := RPMOStreeAutoUpdate{Policy: policy}
			assert.Equalf(t, test.wantPolicy, status.Policy, "policy for %s: %s", test.name, test.why)
			assert.Containsf(t, rpmOStreePolicyVocabulary, policy, "the normalizer must stay inside the closed rpm-ostree policy vocabulary")
		})
	}

	require.GreaterOrEqual(t, len(tests), 22, "expected the full mapping matrix (every native value and alias, plus every way the answer can be unknown); a short table would make the assertions above vacuous")
}

// TestParseRPMOStreeAutomaticUpdatePolicyCoversEveryNativeValue asserts, from
// the mapping direction rather than the table direction, that each of
// rpm-ostree's accepted spellings reaches its normalized constant. It is the
// cross-check that keeps the table above honest: a table row could be deleted
// without a compile error, but a missing alias here fails loudly.
func TestParseRPMOStreeAutomaticUpdatePolicyCoversEveryNativeValue(t *testing.T) {
	native := map[string]string{
		"none":     RPMOStreePolicyNone,
		"off":      RPMOStreePolicyNone,
		"check":    RPMOStreePolicyCheck,
		"stage":    RPMOStreePolicyStage,
		"ex-stage": RPMOStreePolicyStage,
		"apply":    RPMOStreePolicyApply,
	}
	require.Len(t, native, 6, "rpmostree_str_to_auto_update_policy accepts exactly these six spellings")

	for value, want := range native {
		t.Run(value, func(t *testing.T) {
			policy := ParseRPMOStreeAutomaticUpdatePolicy([]byte("[Daemon]\nAutomaticUpdatePolicy=" + value + "\n"))
			assert.Equal(t, want, policy)
			assert.NotEqual(t, RPMOStreePolicyCustomUnknown, policy, "a value rpm-ostree accepts must never normalize to unknown")
		})
	}
}

// TestParseRPMOStreeAutomaticUpdatePolicyDoesNotMutateItsInput pins the "pure
// function" half of the contract: the caller's bytes come back untouched, so
// the parser can never be a hidden second writer of host configuration.
func TestParseRPMOStreeAutomaticUpdatePolicyDoesNotMutateItsInput(t *testing.T) {
	config := []byte("[Daemon]\nAutomaticUpdatePolicy=stage\n")

	assert.Equal(t, RPMOStreePolicyStage, ParseRPMOStreeAutomaticUpdatePolicy(config))
	assert.Equal(t, []byte("[Daemon]\nAutomaticUpdatePolicy=stage\n"), config, "the parser must not write through its input slice")

	assert.Equal(t, RPMOStreePolicyStage, ParseRPMOStreeAutomaticUpdatePolicy(config), "a second call on the same bytes must return the same answer")
}

// TestRPMOStreePolicyVocabularyIsExactlyFiveValues pins the closed enum, so
// adding or renaming a policy string is a deliberate, visible change. It also
// pins that rpm-ostree's vocabulary is not bootc's: the two share only
// custom/unknown, and "apply" is a coincidence of spelling across two separate
// enums, not a shared value.
func TestRPMOStreePolicyVocabularyIsExactlyFiveValues(t *testing.T) {
	assert.Equal(t, "none", RPMOStreePolicyNone)
	assert.Equal(t, "check", RPMOStreePolicyCheck)
	assert.Equal(t, "stage", RPMOStreePolicyStage)
	assert.Equal(t, "apply", RPMOStreePolicyApply)
	assert.Equal(t, "custom/unknown", RPMOStreePolicyCustomUnknown)

	distinct := map[string]bool{}
	for _, policy := range rpmOStreePolicyVocabulary {
		distinct[policy] = true
	}
	require.Len(t, distinct, 5, "the five policy constants must be distinct values")

	assert.NotEqual(t, BootcPolicyStageOnly, RPMOStreePolicyStage, "rpm-ostree's stage is its own value, not bootc's stage-only")
	assert.NotContains(t, rpmOStreePolicyVocabulary, BootcPolicyStageOnly, "bootc's vocabulary must not leak into rpm-ostree's")
}

// TestAutoUpdateRPMOStreeNeverReferencesTheUnitStartCommand mechanically
// enforces the spec's "never by parsing the unit's start command line"
// constraint for the rpm-ostree reader at the source-text level, exactly as
// TestAutoUpdateNeverReferencesTheUnitStartCommand does for autoupdate.go: the
// banned token may not appear anywhere in the file, comments included. The
// derivation that needs to name upstream constructs lives in
// docs/autoupdate.md instead.
func TestAutoUpdateRPMOStreeNeverReferencesTheUnitStartCommand(t *testing.T) {
	const bannedToken = "ExecStart"

	source, err := os.ReadFile(autoUpdateRPMOStreeSourcePath)
	require.NoErrorf(t, err, "reading %s", autoUpdateRPMOStreeSourcePath)
	require.NotEmptyf(t, source, "%s is empty; the assertions below would be vacuous", autoUpdateRPMOStreeSourcePath)

	fileSet := token.NewFileSet()
	file := fileSet.AddFile(autoUpdateRPMOStreeSourcePath, fileSet.Base(), len(source))
	var goScanner scanner.Scanner
	goScanner.Init(file, source, func(position token.Position, message string) {
		t.Fatalf("%s: scanning failed at %s: %s", autoUpdateRPMOStreeSourcePath, position, message)
	}, scanner.ScanComments)

	scanned := 0
	var mentions []string
	for {
		position, tok, literal := goScanner.Scan()
		if tok == token.EOF {
			break
		}
		scanned++
		text := literal
		if text == "" {
			text = tok.String()
		}
		if strings.Contains(text, bannedToken) {
			mentions = append(mentions, fileSet.Position(position).String()+": "+text)
		}
	}

	assert.Emptyf(t, mentions, "%s references %s (%s); rpm-ostree automatic-update policy must come from the daemon configuration, never from a unit's start command", autoUpdateRPMOStreeSourcePath, bannedToken, strings.Join(mentions, "; "))
	assert.Falsef(t, strings.Contains(string(source), bannedToken), "%s contains the raw text %s somewhere the tokenizer above did not report", autoUpdateRPMOStreeSourcePath, bannedToken)
	require.Positivef(t, scanned, "expected to have scanned at least one token in %s; a zero-token scan would make the assertions above vacuous", autoUpdateRPMOStreeSourcePath)
}

// TestAutoUpdateRPMOStreeReadsNothing proves the "the file performs no
// os.ReadFile or other I/O itself" half of this chunk the same way
// TestAutoUpdateRunsNothing proves it for autoupdate.go: by asserting the
// file's imports against an allowlist. With only "strings" importable there is
// no os, no os/exec, no io, no syscall, no net, and no D-Bus client reachable
// from this file, so nothing here can open /etc/rpm-ostreed.conf, run
// rpm-ostree, or talk to a daemon -- the caller supplies the bytes.
func TestAutoUpdateRPMOStreeReadsNothing(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, autoUpdateRPMOStreeSourcePath, nil, parser.ParseComments)
	require.NoError(t, err)

	allowedImports := []string{"strings"}
	for _, spec := range file.Imports {
		path, unquoteErr := strconv.Unquote(spec.Path.Value)
		require.NoError(t, unquoteErr)
		assert.Containsf(t, allowedImports, path, "%s must not import anything that can read the host or run a command", autoUpdateRPMOStreeSourcePath)
	}
}
