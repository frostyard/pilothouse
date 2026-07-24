package maintenance

import (
	"encoding/json"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoUpdateSourcePath is the file the two mechanical tests below inspect.
const autoUpdateSourcePath = "autoupdate.go"

// TestNormalizeBootcAutoUpdatePolicy walks the full drop-in presence matrix for
// the two units bootc ships: {service has drop-ins, service has none} x {timer
// has drop-ins, timer has none}, plus the nil-versus-empty-slice spelling of
// "none" and a multi-path spelling of "some".
//
// The returned booleans are assigned onto a real BootcAutoUpdate rather than
// asserted as bare return values, so the test proves the normalizer's output
// lines up with the ServiceDropinsPresent / TimerDropinsPresent fields the wire
// type actually carries -- including that neither is swapped for the other.
func TestNormalizeBootcAutoUpdatePolicy(t *testing.T) {
	tests := []struct {
		name                  string
		serviceDropInPaths    []string
		timerDropInPaths      []string
		wantPolicy            string
		wantServiceDropins    bool
		wantTimerDropins      bool
		wantDropinDescription string
	}{
		{
			name:                  "no drop-ins at all, nil slices",
			serviceDropInPaths:    nil,
			timerDropInPaths:      nil,
			wantPolicy:            BootcPolicyApply,
			wantDropinDescription: "the shipped units are untouched, so the shipped fetch-and-apply default holds",
		},
		{
			name:                  "no drop-ins at all, empty slices",
			serviceDropInPaths:    []string{},
			timerDropInPaths:      []string{},
			wantPolicy:            BootcPolicyApply,
			wantDropinDescription: "an empty slice means the same thing as a nil one",
		},
		{
			name:                  "service drop-in only",
			serviceDropInPaths:    []string{"/etc/systemd/system/bootc-fetch-apply-updates.service.d/override.conf"},
			timerDropInPaths:      nil,
			wantPolicy:            BootcPolicyCustomUnknown,
			wantServiceDropins:    true,
			wantDropinDescription: "what the service was changed to do cannot be told from a path",
		},
		{
			name:                  "timer drop-in only",
			serviceDropInPaths:    nil,
			timerDropInPaths:      []string{"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/schedule.conf"},
			wantPolicy:            BootcPolicyCustomUnknown,
			wantTimerDropins:      true,
			wantDropinDescription: "a timer-only customization is still a customization",
		},
		{
			name:                  "drop-ins on both units",
			serviceDropInPaths:    []string{"/etc/systemd/system/bootc-fetch-apply-updates.service.d/override.conf"},
			timerDropInPaths:      []string{"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/schedule.conf"},
			wantPolicy:            BootcPolicyCustomUnknown,
			wantServiceDropins:    true,
			wantTimerDropins:      true,
			wantDropinDescription: "both units customized",
		},
		{
			name: "several drop-ins on each unit",
			serviceDropInPaths: []string{
				"/usr/lib/systemd/system/bootc-fetch-apply-updates.service.d/10-vendor.conf",
				"/etc/systemd/system/bootc-fetch-apply-updates.service.d/20-local.conf",
			},
			timerDropInPaths: []string{
				"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/10-schedule.conf",
				"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/20-jitter.conf",
			},
			wantPolicy:            BootcPolicyCustomUnknown,
			wantServiceDropins:    true,
			wantTimerDropins:      true,
			wantDropinDescription: "presence is a boolean, not a count",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, serviceDropinsPresent, timerDropinsPresent := NormalizeBootcAutoUpdatePolicy(test.serviceDropInPaths, test.timerDropInPaths)

			status := BootcAutoUpdate{
				Policy:                policy,
				ServiceDropinsPresent: serviceDropinsPresent,
				TimerDropinsPresent:   timerDropinsPresent,
			}
			assert.Equal(t, test.wantPolicy, status.Policy, "policy for %s: %s", test.name, test.wantDropinDescription)
			assert.Equal(t, test.wantServiceDropins, status.ServiceDropinsPresent, "service_dropins_present must be exactly len(serviceDropInPaths) > 0")
			assert.Equal(t, test.wantTimerDropins, status.TimerDropinsPresent, "timer_dropins_present must be exactly len(timerDropInPaths) > 0")

			assert.Equal(t, len(test.serviceDropInPaths) > 0, serviceDropinsPresent, "service presence must track its own slice, never the timer's")
			assert.Equal(t, len(test.timerDropInPaths) > 0, timerDropinsPresent, "timer presence must track its own slice, never the service's")

			assert.Contains(t, []string{BootcPolicyApply, BootcPolicyCustomUnknown, BootcPolicyStageOnly}, policy, "the normalizer must stay inside the closed policy vocabulary")
			assert.NotEqual(t, BootcPolicyStageOnly, policy, "no drop-in-path input can currently produce stage-only; see docs/autoupdate.md")
		})
	}

	require.Len(t, tests, 6, "expected the full drop-in presence matrix (neither/service-only/timer-only/both, plus the nil-vs-empty and multi-path spellings); a short table would make the assertions above vacuous")
}

// TestNormalizeBootcAutoUpdatePolicyDoesNotMutateItsInputs pins the "pure
// function" half of the contract: the caller's slices come back untouched, so
// the classifier can never be a hidden second writer of systemd state.
func TestNormalizeBootcAutoUpdatePolicyDoesNotMutateItsInputs(t *testing.T) {
	serviceDropInPaths := []string{"/etc/systemd/system/bootc-fetch-apply-updates.service.d/override.conf"}
	timerDropInPaths := []string{"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/schedule.conf"}

	policy, serviceDropinsPresent, timerDropinsPresent := NormalizeBootcAutoUpdatePolicy(serviceDropInPaths, timerDropInPaths)

	assert.Equal(t, BootcPolicyCustomUnknown, policy)
	assert.True(t, serviceDropinsPresent)
	assert.True(t, timerDropinsPresent)
	assert.Equal(t, []string{"/etc/systemd/system/bootc-fetch-apply-updates.service.d/override.conf"}, serviceDropInPaths)
	assert.Equal(t, []string{"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/schedule.conf"}, timerDropInPaths)
}

// TestBootcAutoUpdateStageOnlyPolicyIsRepresentable constructs a
// BootcAutoUpdate carrying BootcPolicyStageOnly directly -- deliberately not
// via NormalizeBootcAutoUpdatePolicy -- and round-trips it through
// json.Marshal/json.Unmarshal, proving the reserved value survives the wire
// end to end.
//
// In plain language: stage-only is a value the classifier cannot currently
// produce from any drop-in-path input, and that is deliberate, not an
// oversight. Upstream bootc ships no non-command-line systemd property and no
// drop-in filename convention that distinguishes "stage but do not apply" from
// any other local customization, and this package is forbidden from reading the
// unit's start command line, which is the only place upstream encodes the
// distinction today. So the enum value is defined, tested, and representable --
// reserved for a future, more specific detection signal -- while the classifier
// itself returns only apply or custom/unknown. This test asserts
// representability; it makes no claim that the normalizer can reach the value.
// docs/autoupdate.md records the same limitation with its upstream citations.
func TestBootcAutoUpdateStageOnlyPolicyIsRepresentable(t *testing.T) {
	original := BootcAutoUpdate{
		NextTrigger:           time.Date(2026, time.July, 23, 3, 30, 0, 0, time.UTC),
		Policy:                BootcPolicyStageOnly,
		ServiceActiveState:    "inactive",
		ServiceDropinsPresent: true,
		ServiceResult:         "success",
		TimerActiveState:      "active",
		TimerDropinsPresent:   false,
		TimerUnitFileState:    "enabled",
	}

	encoded, err := json.Marshal(original)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"next_trigger": "2026-07-23T03:30:00Z",
		"policy": "stage-only",
		"service_active_state": "inactive",
		"service_dropins_present": true,
		"service_result": "success",
		"timer_active_state": "active",
		"timer_dropins_present": false,
		"timer_unit_file_state": "enabled"
	}`, string(encoded))

	var decoded BootcAutoUpdate
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, original, decoded)
	assert.Equal(t, "stage-only", decoded.Policy, "the reserved value must survive the wire unchanged")
}

// TestBootcPolicyVocabularyIsExactlyThreeValues pins the closed enum the spec
// names, so adding or renaming a policy string is a deliberate, visible change.
func TestBootcPolicyVocabularyIsExactlyThreeValues(t *testing.T) {
	assert.Equal(t, "apply", BootcPolicyApply)
	assert.Equal(t, "stage-only", BootcPolicyStageOnly)
	assert.Equal(t, "custom/unknown", BootcPolicyCustomUnknown)

	vocabulary := map[string]bool{BootcPolicyApply: true, BootcPolicyStageOnly: true, BootcPolicyCustomUnknown: true}
	require.Len(t, vocabulary, 3, "the three policy constants must be distinct values")
}

// TestAutoUpdateStatusNotConfiguredIsTheZeroValue proves the canonical "no
// automatic updater is configured" answer is exactly the zero AutoUpdateStatus:
// both availability bools false and both payload pointers absent from the wire
// form. That state is a valid report, not an error.
func TestAutoUpdateStatusNotConfiguredIsTheZeroValue(t *testing.T) {
	var status AutoUpdateStatus
	assert.False(t, status.BootcConfigured)
	assert.False(t, status.RPMOStreeConfigured)
	assert.Nil(t, status.Bootc)
	assert.Nil(t, status.RPMOStree)

	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	assert.JSONEq(t, `{"bootc_configured": false, "rpm_ostree_configured": false}`, string(encoded))

	var decoded AutoUpdateStatus
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, status, decoded)
}

// TestAutoUpdateStatusConfiguredWireShape pins every JSON tag on all three
// types at once, with both updaters configured and a policy the classifier can
// actually produce from drop-in paths -- so the outer keys
// (bootc_configured/bootc/rpm_ostree_configured/rpm_ostree) and the per-updater
// keys are asserted against literal JSON rather than restated Go field names.
func TestAutoUpdateStatusConfiguredWireShape(t *testing.T) {
	policy, serviceDropinsPresent, timerDropinsPresent := NormalizeBootcAutoUpdatePolicy(
		[]string{"/etc/systemd/system/bootc-fetch-apply-updates.service.d/override.conf"},
		nil,
	)

	status := AutoUpdateStatus{
		Bootc: &BootcAutoUpdate{
			NextTrigger:           time.Date(2026, time.July, 23, 4, 0, 0, 0, time.UTC),
			Policy:                policy,
			ServiceActiveState:    "inactive",
			ServiceDropinsPresent: serviceDropinsPresent,
			ServiceResult:         "success",
			TimerActiveState:      "active",
			TimerDropinsPresent:   timerDropinsPresent,
			TimerUnitFileState:    "enabled",
		},
		BootcConfigured: true,
		RPMOStree: &RPMOStreeAutoUpdate{
			NextTrigger:           time.Date(2026, time.July, 23, 5, 0, 0, 0, time.UTC),
			Policy:                "check",
			ServiceActiveState:    "inactive",
			ServiceDropinsPresent: false,
			ServiceResult:         "failed",
			TimerActiveState:      "active",
			TimerDropinsPresent:   true,
			TimerUnitFileState:    "enabled",
		},
		RPMOStreeConfigured: true,
	}

	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"bootc_configured": true,
		"bootc": {
			"next_trigger": "2026-07-23T04:00:00Z",
			"policy": "custom/unknown",
			"service_active_state": "inactive",
			"service_dropins_present": true,
			"service_result": "success",
			"timer_active_state": "active",
			"timer_dropins_present": false,
			"timer_unit_file_state": "enabled"
		},
		"rpm_ostree_configured": true,
		"rpm_ostree": {
			"next_trigger": "2026-07-23T05:00:00Z",
			"policy": "check",
			"service_active_state": "inactive",
			"service_dropins_present": false,
			"service_result": "failed",
			"timer_active_state": "active",
			"timer_dropins_present": true,
			"timer_unit_file_state": "enabled"
		}
	}`, string(encoded))

	var decoded AutoUpdateStatus
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, status, decoded)
}

// TestAutoUpdateNeverReferencesTheUnitStartCommand mechanically enforces the
// spec's "never inferred from the unit's start command line" constraint at the
// source-text level: the banned token may not appear anywhere in autoupdate.go,
// comments included. Comments are deliberately *not* excluded here -- unlike
// TestMaintenanceNeverReferencesZincati, whose criterion is about tokens that
// reach the compiler, this criterion is about the file never naming the
// property at all, so even a comment mentioning it is a violation. The
// derivation that needs to cite the upstream line lives in docs/autoupdate.md
// instead.
//
// The file is tokenized with go/scanner in ScanComments mode so every token,
// comment included, is examined; a raw byte scan backs that up so a future
// change to the tokenizer's mode cannot silently narrow the check. The scanned
// token count is pinned positive the same way TestMaintenanceNeverReferencesZincati
// pins its own walk, so an unreadable or empty file fails loudly instead of
// passing vacuously.
func TestAutoUpdateNeverReferencesTheUnitStartCommand(t *testing.T) {
	const bannedToken = "ExecStart"

	source, err := os.ReadFile(autoUpdateSourcePath)
	require.NoErrorf(t, err, "reading %s", autoUpdateSourcePath)
	require.NotEmptyf(t, source, "%s is empty; the assertions below would be vacuous", autoUpdateSourcePath)

	fileSet := token.NewFileSet()
	file := fileSet.AddFile(autoUpdateSourcePath, fileSet.Base(), len(source))
	var goScanner scanner.Scanner
	goScanner.Init(file, source, func(position token.Position, message string) {
		t.Fatalf("%s: scanning failed at %s: %s", autoUpdateSourcePath, position, message)
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

	assert.Emptyf(t, mentions, "%s references %s (%s); bootc automatic-update policy must never be derived from it, and even naming it here is banned", autoUpdateSourcePath, bannedToken, strings.Join(mentions, "; "))
	assert.Falsef(t, strings.Contains(string(source), bannedToken), "%s contains the raw text %s somewhere the tokenizer above did not report", autoUpdateSourcePath, bannedToken)
	require.Positivef(t, scanned, "expected to have scanned at least one token in %s; a zero-token scan would make the assertions above vacuous", autoUpdateSourcePath)
}

// TestAutoUpdateRunsNothing proves the "pure, zero-I/O" half of this chunk the
// same way TestHostImageRunsNoBootcSubcommand proves it for hostimage.go: by
// asserting the file's imports against an allowlist. With only "time" importable
// there is no os/exec, no os, no syscall, no net, and no D-Bus client reachable
// from this file, so nothing here can run a command, read the filesystem, or
// talk to systemd -- the caller supplies the drop-in paths.
func TestAutoUpdateRunsNothing(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, autoUpdateSourcePath, nil, parser.ParseComments)
	require.NoError(t, err)

	allowedImports := []string{"time"}
	for _, spec := range file.Imports {
		path, unquoteErr := strconv.Unquote(spec.Path.Value)
		require.NoError(t, unquoteErr)
		assert.Containsf(t, allowedImports, path, "%s must not import anything that can run a command or reach the host", autoUpdateSourcePath)
	}
}
