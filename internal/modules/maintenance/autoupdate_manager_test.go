package maintenance

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoUpdateManagerSourcePath is the file the mechanical tests at the bottom
// inspect.
const autoUpdateManagerSourcePath = "autoupdate_manager.go"

// The test matrix this file covers, written out in full before the tests
// were, per docs/agents/skills/enumerate-the-full-test-matrix-for-multi-axis-criteria.md.
// Rows are the two updaters; every cell below has its own assertion, and every
// one of them is driven through the manager's real Status(ctx) -- never
// through a helper that takes pre-computed properties as a plain parameter
// (docs/agents/skills/exercise-the-actual-boundary-not-a-precomputed-shim.md,
// docs/agents/skills/test-the-composing-function-not-its-merge-helper.md).
//
//	| condition                              | bootc                                    | rpm-ostree                                     |
//	|----------------------------------------|------------------------------------------|------------------------------------------------|
//	| capability absent                      | TestAutoUpdateManagerBootcAbsent         | TestAutoUpdateManagerRPMOStreeAbsent           |
//	| full success, live fake                | TestAutoUpdateManagerBootcFullSuccess    | TestAutoUpdateManagerRPMOStreeFullSuccess      |
//	| nil systemd client                     | TestAutoUpdateManagerNilClient           | TestAutoUpdateManagerNilClient                 |
//	| timer GetUnitProperties fails          | TestAutoUpdateManagerBootcReadFailures   | TestAutoUpdateManagerRPMOStreeReadFailures     |
//	| timer GetUnitTypeProperties fails      | TestAutoUpdateManagerBootcReadFailures   | TestAutoUpdateManagerRPMOStreeReadFailures     |
//	| service GetUnitProperties fails        | TestAutoUpdateManagerBootcReadFailures   | TestAutoUpdateManagerRPMOStreeReadFailures     |
//	| service GetUnitTypeProperties fails    | TestAutoUpdateManagerBootcReadFailures   | TestAutoUpdateManagerRPMOStreeReadFailures     |
//	| all four reads fail                    | TestAutoUpdateManagerBootcReadFailures   | TestAutoUpdateManagerRPMOStreeReadFailures     |
//	| drop-in paths present / unreadable     | TestAutoUpdateManagerBootcPolicyMatrix   | TestAutoUpdateManagerRPMOStreeDropInsAreIndependentOfPolicy |
//	| rpm-ostreed.conf present / absent      | n/a (bootc has no config input)          | TestAutoUpdateManagerRPMOStreePolicyFromConfig |
//
// Plus, across both: neither configured (the zero AutoUpdateStatus), both
// configured at once, construction performing no I/O, and the exact D-Bus call
// list Status makes.

// autoUpdateCall records one property read the manager made, so a test can
// assert not only what Status reported but exactly which units it asked about
// -- and, for the absent-capability cells, which it never asked about at all.
type autoUpdateCall struct {
	name     string
	unitType string
}

// fakeAutoUpdateSystemdClient implements the manager's whole systemdClient
// interface end-to-end, not a narrower connector-only shim
// (docs/agents/skills/dbus-fake-must-cover-full-success-path.md): both property
// getters answer from per-unit maps and can be made to fail independently, so
// the full success path and each individual failure branch are all reachable
// through the production Status call.
type fakeAutoUpdateSystemdClient struct {
	calls          []autoUpdateCall
	properties     map[string]map[string]any
	propertyErrors map[string]error
	typeErrors     map[string]error
	typeProperties map[string]map[string]any
}

// The fake must satisfy the real interface the manager depends on; if
// systemdClient ever grows a method, this fails to compile rather than
// silently leaving the new method untested.
var _ systemdClient = (*fakeAutoUpdateSystemdClient)(nil)

func (client *fakeAutoUpdateSystemdClient) GetUnitPropertiesContext(_ context.Context, name string) (map[string]any, error) {
	client.calls = append(client.calls, autoUpdateCall{name: name})
	return client.properties[name], client.propertyErrors[name]
}

func (client *fakeAutoUpdateSystemdClient) GetUnitTypePropertiesContext(_ context.Context, name, unitType string) (map[string]any, error) {
	client.calls = append(client.calls, autoUpdateCall{name: name, unitType: unitType})
	key := name + ":" + unitType
	return client.typeProperties[key], client.typeErrors[key]
}

// autoUpdateNextTrigger is the one wall-clock instant the fixtures schedule.
// It is a fixed date, never time.Now, so the assertions below are exact.
var autoUpdateNextTrigger = time.Date(2026, 7, 23, 3, 30, 0, 0, time.UTC)

// liveAutoUpdateClient answers for both updaters with deliberately distinct
// values per updater -- bootc's timer is "active"/"enabled", rpm-ostree's is
// "inactive"/"static", their results and next triggers differ -- so a manager
// that read the wrong updater's units, or crossed the two payloads, fails
// loudly instead of matching by coincidence
// (docs/agents/skills/calibrate-canned-fixture-data-per-capability-set.md).
//
// dropIns selects whether either pair carries drop-ins; the empty slices in
// the no-drop-ins case are what systemd itself reports for an unmodified unit
// (a present, empty "as" array), which is exactly the distinction the
// manager's drop-ins-known signal turns on.
func liveAutoUpdateClient(bootcDropIns, rpmOStreeDropIns bool) *fakeAutoUpdateSystemdClient {
	bootcServiceDropInPaths := []string{}
	bootcTimerDropInPaths := []string{}
	if bootcDropIns {
		bootcServiceDropInPaths = []string{"/etc/systemd/system/bootc-fetch-apply-updates.service.d/10-local.conf"}
		bootcTimerDropInPaths = []string{"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/10-schedule.conf"}
	}
	rpmOStreeServiceDropInPaths := []string{}
	rpmOStreeTimerDropInPaths := []string{}
	if rpmOStreeDropIns {
		rpmOStreeServiceDropInPaths = []string{"/etc/systemd/system/rpm-ostreed-automatic.service.d/10-local.conf"}
		rpmOStreeTimerDropInPaths = []string{"/etc/systemd/system/rpm-ostreed-automatic.timer.d/10-schedule.conf"}
	}
	return &fakeAutoUpdateSystemdClient{
		properties: map[string]map[string]any{
			bootcAutoUpdateTimerUnit: {
				"ActiveState":   "active",
				"DropInPaths":   bootcTimerDropInPaths,
				"UnitFileState": "enabled",
			},
			bootcAutoUpdateServiceUnit: {
				"ActiveState":   "inactive",
				"DropInPaths":   bootcServiceDropInPaths,
				"UnitFileState": "static",
			},
			rpmOStreeAutoUpdateTimerUnit: {
				"ActiveState":   "inactive",
				"DropInPaths":   rpmOStreeTimerDropInPaths,
				"UnitFileState": "static",
			},
			rpmOStreeAutoUpdateServiceUnit: {
				"ActiveState":   "activating",
				"DropInPaths":   rpmOStreeServiceDropInPaths,
				"UnitFileState": "static",
			},
		},
		typeProperties: map[string]map[string]any{
			bootcAutoUpdateTimerUnit + ":Timer": {
				"LastTriggerUSec":        uint64(autoUpdateNextTrigger.Add(-24 * time.Hour).UnixMicro()),
				"NextElapseUSecRealtime": uint64(autoUpdateNextTrigger.UnixMicro()),
			},
			bootcAutoUpdateServiceUnit + ":Service": {
				"Result": "success",
			},
			rpmOStreeAutoUpdateTimerUnit + ":Timer": {
				"LastTriggerUSec":        uint64(autoUpdateNextTrigger.Add(-48 * time.Hour).UnixMicro()),
				"NextElapseUSecRealtime": uint64(autoUpdateNextTrigger.Add(time.Hour).UnixMicro()),
			},
			rpmOStreeAutoUpdateServiceUnit + ":Service": {
				"Result": "timeout",
			},
		},
		propertyErrors: map[string]error{},
		typeErrors:     map[string]error{},
	}
}

// autoUpdateRoot builds a temporary root holding rpm-ostree's daemon
// configuration, so the manager's one file read never touches the test host's
// real /etc.
func autoUpdateRoot(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, rpmOStreeConfigFile), []byte(config), 0o600))
	return root
}

// namesCalled lists the unit names the fake was asked about, in order.
func (client *fakeAutoUpdateSystemdClient) namesCalled() []string {
	names := make([]string, 0, len(client.calls))
	for _, call := range client.calls {
		names = append(names, call.name)
	}
	return names
}

func TestNewAutoUpdateManagerTouchesNeitherDiskNorDBus(t *testing.T) {
	client := liveAutoUpdateClient(false, false)
	// A root that does not exist: if construction read the configuration file
	// it would have to fail or observe an absence, and the call counter below
	// would be the only witness either way.
	manager := NewAutoUpdateManager(client, true, true, filepath.Join(t.TempDir(), "missing"))

	require.NotNil(t, manager)
	assert.Empty(t, client.calls, "constructing the manager must make no D-Bus call; every read is deferred to Status")
}

func TestNewAutoUpdateManagerDefaultsRootToSlash(t *testing.T) {
	manager := NewAutoUpdateManager(nil, false, false, "")

	assert.Equal(t, "/", manager.root)
	assert.Equal(t, "/etc/rpm-ostreed.conf", manager.path(rpmOStreeConfigFile),
		"an empty root must resolve rpm-ostree's daemon configuration at the real host path, exactly as SystemManager's root/path() pattern does")

	rooted := NewAutoUpdateManager(nil, false, false, "/tmp/example")
	assert.Equal(t, "/tmp/example/etc/rpm-ostreed.conf", rooted.path(rpmOStreeConfigFile),
		"a non-empty root must be honored, so tests can redirect the read at a temporary directory")
}

func TestAutoUpdateManagerNoUpdaterConfiguredIsTheZeroStatus(t *testing.T) {
	client := liveAutoUpdateClient(false, false)
	manager := NewAutoUpdateManager(client, false, false, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=apply\n"))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	assert.Equal(t, AutoUpdateStatus{}, status, "no configured updater must report the canonical zero status, not an error")
	assert.Empty(t, client.calls, "an unconfigured host must produce no D-Bus call at all")
}

func TestAutoUpdateManagerBootcAbsent(t *testing.T) {
	client := liveAutoUpdateClient(false, false)
	manager := NewAutoUpdateManager(client, false, true, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=check\n"))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	assert.False(t, status.BootcConfigured)
	assert.Nil(t, status.Bootc, "an unconfigured bootc updater contributes no payload")
	for _, name := range client.namesCalled() {
		assert.NotContainsf(t, name, "bootc-fetch-apply-updates", "no bootc-fetch-apply-updates.* D-Bus call may be made when the capability is absent, but %s was asked about", name)
	}
	// The other updater is unaffected: absence is per-updater, not global.
	assert.True(t, status.RPMOStreeConfigured)
	require.NotNil(t, status.RPMOStree)
	assert.Equal(t, RPMOStreePolicyCheck, status.RPMOStree.Policy)
}

func TestAutoUpdateManagerRPMOStreeAbsent(t *testing.T) {
	client := liveAutoUpdateClient(false, false)
	manager := NewAutoUpdateManager(client, true, false, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=apply\n"))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	assert.False(t, status.RPMOStreeConfigured)
	assert.Nil(t, status.RPMOStree, "an unconfigured rpm-ostree updater contributes no payload")
	for _, name := range client.namesCalled() {
		assert.NotContainsf(t, name, "rpm-ostreed-automatic", "no rpm-ostreed-automatic.* D-Bus call may be made when the capability is absent, but %s was asked about", name)
	}
	assert.True(t, status.BootcConfigured)
	require.NotNil(t, status.Bootc)
	assert.Equal(t, BootcPolicyApply, status.Bootc.Policy)
}

func TestAutoUpdateManagerBootcFullSuccess(t *testing.T) {
	client := liveAutoUpdateClient(false, false)
	manager := NewAutoUpdateManager(client, true, false, autoUpdateRoot(t, ""))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	assert.True(t, status.BootcConfigured)
	require.NotNil(t, status.Bootc)
	assert.Equal(t, BootcAutoUpdate{
		NextTrigger:        autoUpdateNextTrigger,
		Policy:             BootcPolicyApply,
		ServiceActiveState: "inactive",
		ServiceResult:      "success",
		TimerActiveState:   "active",
		TimerUnitFileState: "enabled",
	}, *status.Bootc)

	assert.Equal(t, []autoUpdateCall{
		{name: bootcAutoUpdateTimerUnit},
		{name: bootcAutoUpdateTimerUnit, unitType: "Timer"},
		{name: bootcAutoUpdateServiceUnit},
		{name: bootcAutoUpdateServiceUnit, unitType: "Service"},
	}, client.calls, "the bootc half must read exactly its timer and service units, through the two property getters only")
}

func TestAutoUpdateManagerRPMOStreeFullSuccess(t *testing.T) {
	client := liveAutoUpdateClient(false, false)
	manager := NewAutoUpdateManager(client, false, true, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=stage\n"))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	assert.True(t, status.RPMOStreeConfigured)
	require.NotNil(t, status.RPMOStree)
	assert.Equal(t, RPMOStreeAutoUpdate{
		NextTrigger:        autoUpdateNextTrigger.Add(time.Hour),
		Policy:             RPMOStreePolicyStage,
		ServiceActiveState: "activating",
		ServiceResult:      "timeout",
		TimerActiveState:   "inactive",
		TimerUnitFileState: "static",
	}, *status.RPMOStree)

	assert.Equal(t, []autoUpdateCall{
		{name: rpmOStreeAutoUpdateTimerUnit},
		{name: rpmOStreeAutoUpdateTimerUnit, unitType: "Timer"},
		{name: rpmOStreeAutoUpdateServiceUnit},
		{name: rpmOStreeAutoUpdateServiceUnit, unitType: "Service"},
	}, client.calls, "the rpm-ostree half must read exactly its timer and service units, through the two property getters only")
}

func TestAutoUpdateManagerBothUpdatersConfigured(t *testing.T) {
	client := liveAutoUpdateClient(true, true)
	manager := NewAutoUpdateManager(client, true, true, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=apply\n"))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	require.NotNil(t, status.Bootc)
	require.NotNil(t, status.RPMOStree)

	// Drop-ins on both units of both updaters: bootc's policy is derived from
	// them and collapses to custom/unknown; rpm-ostree's comes from its own
	// configuration and is unaffected, which is exactly the asymmetry the two
	// separate policy vocabularies exist to express.
	assert.Equal(t, BootcPolicyCustomUnknown, status.Bootc.Policy)
	assert.True(t, status.Bootc.ServiceDropinsPresent)
	assert.True(t, status.Bootc.TimerDropinsPresent)
	assert.Equal(t, RPMOStreePolicyApply, status.RPMOStree.Policy)
	assert.True(t, status.RPMOStree.ServiceDropinsPresent)
	assert.True(t, status.RPMOStree.TimerDropinsPresent)

	// The two payloads carry their own updater's facts, never each other's.
	assert.Equal(t, "active", status.Bootc.TimerActiveState)
	assert.Equal(t, "inactive", status.RPMOStree.TimerActiveState)
	assert.Equal(t, "success", status.Bootc.ServiceResult)
	assert.Equal(t, "timeout", status.RPMOStree.ServiceResult)

	assert.Equal(t, []autoUpdateCall{
		{name: bootcAutoUpdateTimerUnit},
		{name: bootcAutoUpdateTimerUnit, unitType: "Timer"},
		{name: bootcAutoUpdateServiceUnit},
		{name: bootcAutoUpdateServiceUnit, unitType: "Service"},
		{name: rpmOStreeAutoUpdateTimerUnit},
		{name: rpmOStreeAutoUpdateTimerUnit, unitType: "Timer"},
		{name: rpmOStreeAutoUpdateServiceUnit},
		{name: rpmOStreeAutoUpdateServiceUnit, unitType: "Service"},
	}, client.calls)
}

// TestAutoUpdateManagerNilClient covers the nil-client cell for both updaters
// at once: each configured updater still gets a non-nil payload with every
// field at its zero value and Policy at custom/unknown, and nothing panics.
func TestAutoUpdateManagerNilClient(t *testing.T) {
	// The configuration file is deliberately present and readable here: it
	// proves the file read is independent of the systemd client, so a nil
	// client degrades the systemd-sourced fields only.
	manager := NewAutoUpdateManager(nil, true, true, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=check\n"))

	require.NotPanics(t, func() {
		status, err := manager.Status(context.Background())

		require.NoError(t, err)
		assert.True(t, status.BootcConfigured)
		assert.True(t, status.RPMOStreeConfigured)
		require.NotNil(t, status.Bootc, "a configured updater always reports a payload, even with no systemd client")
		require.NotNil(t, status.RPMOStree)
		assert.Equal(t, BootcAutoUpdate{Policy: BootcPolicyCustomUnknown}, *status.Bootc,
			"a nil client must behave exactly as though every systemd read failed")
		assert.Equal(t, RPMOStreeAutoUpdate{Policy: RPMOStreePolicyCheck}, *status.RPMOStree,
			"rpm-ostree's policy comes from its configuration file, so a nil systemd client zeroes only the unit fields")
	})
}

func TestAutoUpdateManagerNilClientWithNoConfigFile(t *testing.T) {
	// The whole-system worst case: no systemd session and no configuration
	// file. Both payloads exist, both are entirely zero apart from
	// custom/unknown, and Status still reports no error.
	manager := NewAutoUpdateManager(nil, true, true, filepath.Join(t.TempDir(), "empty-root"))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	require.NotNil(t, status.Bootc)
	require.NotNil(t, status.RPMOStree)
	assert.Equal(t, BootcAutoUpdate{Policy: BootcPolicyCustomUnknown}, *status.Bootc)
	assert.Equal(t, RPMOStreeAutoUpdate{Policy: RPMOStreePolicyCustomUnknown}, *status.RPMOStree)
}

// TestAutoUpdateManagerBootcReadFailures walks every individual D-Bus read
// behind the bootc payload, failing exactly one at a time (plus the all-four
// case), and asserts the payload survives with only that read's fields
// degraded to zero.
func TestAutoUpdateManagerBootcReadFailures(t *testing.T) {
	readFailure := errors.New("dbus: connection reset")
	tests := []struct {
		name     string
		fail     func(client *fakeAutoUpdateSystemdClient)
		expected BootcAutoUpdate
	}{
		{
			name: "timer unit properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.propertyErrors[bootcAutoUpdateTimerUnit] = readFailure
			},
			// TimerActiveState, TimerUnitFileState and the timer's drop-in
			// list all come from that one call; losing the drop-in list alone
			// is enough to force custom/unknown.
			expected: BootcAutoUpdate{
				NextTrigger:        autoUpdateNextTrigger,
				Policy:             BootcPolicyCustomUnknown,
				ServiceActiveState: "inactive",
				ServiceResult:      "success",
			},
		},
		{
			name: "timer type properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.typeErrors[bootcAutoUpdateTimerUnit+":Timer"] = readFailure
			},
			expected: BootcAutoUpdate{
				Policy:             BootcPolicyApply,
				ServiceActiveState: "inactive",
				ServiceResult:      "success",
				TimerActiveState:   "active",
				TimerUnitFileState: "enabled",
			},
		},
		{
			name: "service unit properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.propertyErrors[bootcAutoUpdateServiceUnit] = readFailure
			},
			expected: BootcAutoUpdate{
				NextTrigger:        autoUpdateNextTrigger,
				Policy:             BootcPolicyCustomUnknown,
				ServiceResult:      "success",
				TimerActiveState:   "active",
				TimerUnitFileState: "enabled",
			},
		},
		{
			name: "service type properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.typeErrors[bootcAutoUpdateServiceUnit+":Service"] = readFailure
			},
			expected: BootcAutoUpdate{
				NextTrigger:        autoUpdateNextTrigger,
				Policy:             BootcPolicyApply,
				ServiceActiveState: "inactive",
				TimerActiveState:   "active",
				TimerUnitFileState: "enabled",
			},
		},
		{
			name: "every read fails",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.propertyErrors[bootcAutoUpdateTimerUnit] = readFailure
				client.propertyErrors[bootcAutoUpdateServiceUnit] = readFailure
				client.typeErrors[bootcAutoUpdateTimerUnit+":Timer"] = readFailure
				client.typeErrors[bootcAutoUpdateServiceUnit+":Service"] = readFailure
			},
			expected: BootcAutoUpdate{Policy: BootcPolicyCustomUnknown},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := liveAutoUpdateClient(false, false)
			test.fail(client)
			manager := NewAutoUpdateManager(client, true, false, autoUpdateRoot(t, ""))

			status, err := manager.Status(context.Background())

			require.NoError(t, err, "a failed D-Bus read must never become Status's own error")
			assert.True(t, status.BootcConfigured)
			require.NotNil(t, status.Bootc, "a failed read degrades one field, never the whole payload")
			assert.Equal(t, test.expected, *status.Bootc)
			assert.Len(t, client.calls, 4, "a failed read must not short-circuit the remaining reads")
		})
	}
}

// TestAutoUpdateManagerRPMOStreeReadFailures is the symmetric matrix for
// rpm-ostree. Its Policy comes from the configuration file rather than from
// drop-ins, so unlike bootc's, it survives every D-Bus failure intact.
func TestAutoUpdateManagerRPMOStreeReadFailures(t *testing.T) {
	readFailure := errors.New("dbus: connection reset")
	tests := []struct {
		name     string
		fail     func(client *fakeAutoUpdateSystemdClient)
		expected RPMOStreeAutoUpdate
	}{
		{
			name: "timer unit properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.propertyErrors[rpmOStreeAutoUpdateTimerUnit] = readFailure
			},
			expected: RPMOStreeAutoUpdate{
				NextTrigger:        autoUpdateNextTrigger.Add(time.Hour),
				Policy:             RPMOStreePolicyApply,
				ServiceActiveState: "activating",
				ServiceResult:      "timeout",
			},
		},
		{
			name: "timer type properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.typeErrors[rpmOStreeAutoUpdateTimerUnit+":Timer"] = readFailure
			},
			expected: RPMOStreeAutoUpdate{
				Policy:             RPMOStreePolicyApply,
				ServiceActiveState: "activating",
				ServiceResult:      "timeout",
				TimerActiveState:   "inactive",
				TimerUnitFileState: "static",
			},
		},
		{
			name: "service unit properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.propertyErrors[rpmOStreeAutoUpdateServiceUnit] = readFailure
			},
			expected: RPMOStreeAutoUpdate{
				NextTrigger:        autoUpdateNextTrigger.Add(time.Hour),
				Policy:             RPMOStreePolicyApply,
				ServiceResult:      "timeout",
				TimerActiveState:   "inactive",
				TimerUnitFileState: "static",
			},
		},
		{
			name: "service type properties unreadable",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.typeErrors[rpmOStreeAutoUpdateServiceUnit+":Service"] = readFailure
			},
			expected: RPMOStreeAutoUpdate{
				NextTrigger:        autoUpdateNextTrigger.Add(time.Hour),
				Policy:             RPMOStreePolicyApply,
				ServiceActiveState: "activating",
				TimerActiveState:   "inactive",
				TimerUnitFileState: "static",
			},
		},
		{
			name: "every read fails",
			fail: func(client *fakeAutoUpdateSystemdClient) {
				client.propertyErrors[rpmOStreeAutoUpdateTimerUnit] = readFailure
				client.propertyErrors[rpmOStreeAutoUpdateServiceUnit] = readFailure
				client.typeErrors[rpmOStreeAutoUpdateTimerUnit+":Timer"] = readFailure
				client.typeErrors[rpmOStreeAutoUpdateServiceUnit+":Service"] = readFailure
			},
			expected: RPMOStreeAutoUpdate{Policy: RPMOStreePolicyApply},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := liveAutoUpdateClient(false, false)
			test.fail(client)
			manager := NewAutoUpdateManager(client, false, true, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=apply\n"))

			status, err := manager.Status(context.Background())

			require.NoError(t, err, "a failed D-Bus read must never become Status's own error")
			assert.True(t, status.RPMOStreeConfigured)
			require.NotNil(t, status.RPMOStree, "a failed read degrades one field, never the whole payload")
			assert.Equal(t, test.expected, *status.RPMOStree)
			assert.Len(t, client.calls, 4, "a failed read must not short-circuit the remaining reads")
		})
	}
}

// TestAutoUpdateManagerBootcPolicyMatrix drives c1's classifier through the
// live manager for every drop-in shape systemd can report, including the two
// "the list could not be read" spellings -- an errored call and a properties
// map with no DropInPaths key -- which must both fall back to custom/unknown
// rather than being mistaken for "no drop-ins."
func TestAutoUpdateManagerBootcPolicyMatrix(t *testing.T) {
	tests := []struct {
		name                   string
		serviceDropInPaths     any
		timerDropInPaths       any
		expectedPolicy         string
		expectedServicePresent bool
		expectedTimerPresent   bool
	}{
		{
			name:               "no drop-ins on either unit",
			serviceDropInPaths: []string{},
			timerDropInPaths:   []string{},
			expectedPolicy:     BootcPolicyApply,
		},
		{
			name:                   "service drop-in only",
			serviceDropInPaths:     []string{"/etc/systemd/system/bootc-fetch-apply-updates.service.d/10-local.conf"},
			timerDropInPaths:       []string{},
			expectedPolicy:         BootcPolicyCustomUnknown,
			expectedServicePresent: true,
		},
		{
			name:                 "timer drop-in only",
			serviceDropInPaths:   []string{},
			timerDropInPaths:     []string{"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/10-schedule.conf"},
			expectedPolicy:       BootcPolicyCustomUnknown,
			expectedTimerPresent: true,
		},
		{
			name:                   "drop-ins on both units",
			serviceDropInPaths:     []string{"/usr/lib/systemd/system/bootc-fetch-apply-updates.service.d/10-vendor.conf"},
			timerDropInPaths:       []string{"/etc/systemd/system/bootc-fetch-apply-updates.timer.d/10-schedule.conf"},
			expectedPolicy:         BootcPolicyCustomUnknown,
			expectedServicePresent: true,
			expectedTimerPresent:   true,
		},
		{
			name:               "service drop-in list missing from the properties map",
			serviceDropInPaths: nil,
			timerDropInPaths:   []string{},
			expectedPolicy:     BootcPolicyCustomUnknown,
		},
		{
			name:               "timer drop-in list missing from the properties map",
			serviceDropInPaths: []string{},
			timerDropInPaths:   nil,
			expectedPolicy:     BootcPolicyCustomUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := liveAutoUpdateClient(false, false)
			if test.serviceDropInPaths == nil {
				delete(client.properties[bootcAutoUpdateServiceUnit], "DropInPaths")
			} else {
				client.properties[bootcAutoUpdateServiceUnit]["DropInPaths"] = test.serviceDropInPaths
			}
			if test.timerDropInPaths == nil {
				delete(client.properties[bootcAutoUpdateTimerUnit], "DropInPaths")
			} else {
				client.properties[bootcAutoUpdateTimerUnit]["DropInPaths"] = test.timerDropInPaths
			}
			manager := NewAutoUpdateManager(client, true, false, autoUpdateRoot(t, ""))

			status, err := manager.Status(context.Background())

			require.NoError(t, err)
			require.NotNil(t, status.Bootc)
			assert.Equal(t, test.expectedPolicy, status.Bootc.Policy)
			assert.Equal(t, test.expectedServicePresent, status.Bootc.ServiceDropinsPresent)
			assert.Equal(t, test.expectedTimerPresent, status.Bootc.TimerDropinsPresent)
			assert.Contains(t, []string{BootcPolicyApply, BootcPolicyCustomUnknown, BootcPolicyStageOnly}, status.Bootc.Policy,
				"the reported policy must stay inside bootc's closed vocabulary")
		})
	}
}

// TestAutoUpdateManagerRPMOStreePolicyFromConfig covers the configuration-file
// axis: a recognized value maps through c2's parser, and an absent file
// (os.IsNotExist) reports custom/unknown per the spec's absent-handling rule
// -- deliberately not rpm-ostree's own "absent means none" default.
func TestAutoUpdateManagerRPMOStreePolicyFromConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		absent   bool
		expected string
	}{
		{name: "none", config: "[Daemon]\nAutomaticUpdatePolicy=none\n", expected: RPMOStreePolicyNone},
		{name: "off alias", config: "[Daemon]\nAutomaticUpdatePolicy=off\n", expected: RPMOStreePolicyNone},
		{name: "check", config: "[Daemon]\nAutomaticUpdatePolicy=check\n", expected: RPMOStreePolicyCheck},
		{name: "stage", config: "[Daemon]\nAutomaticUpdatePolicy=stage\n", expected: RPMOStreePolicyStage},
		{name: "ex-stage alias", config: "[Daemon]\nAutomaticUpdatePolicy=ex-stage\n", expected: RPMOStreePolicyStage},
		{name: "apply", config: "[Daemon]\nAutomaticUpdatePolicy=apply\n", expected: RPMOStreePolicyApply},
		{name: "unrecognized value", config: "[Daemon]\nAutomaticUpdatePolicy=whenever\n", expected: RPMOStreePolicyCustomUnknown},
		{name: "no policy key", config: "[Daemon]\nIdleExitTimeout=60\n", expected: RPMOStreePolicyCustomUnknown},
		{name: "empty file", config: "", expected: RPMOStreePolicyCustomUnknown},
		{name: "file absent", absent: true, expected: RPMOStreePolicyCustomUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			if test.absent {
				// No etc/ at all: the read fails with an os.IsNotExist error.
				require.NoError(t, os.MkdirAll(root, 0o755))
				_, statErr := os.Stat(filepath.Join(root, rpmOStreeConfigFile))
				require.True(t, os.IsNotExist(statErr), "the fixture must genuinely be an absent file")
			} else {
				root = autoUpdateRoot(t, test.config)
			}
			client := liveAutoUpdateClient(false, false)
			manager := NewAutoUpdateManager(client, false, true, root)

			status, err := manager.Status(context.Background())

			require.NoError(t, err, "an unreadable configuration file must never become Status's own error")
			require.NotNil(t, status.RPMOStree)
			assert.Equal(t, test.expected, status.RPMOStree.Policy)
			// The unit fields are unaffected by the configuration file.
			assert.Equal(t, "inactive", status.RPMOStree.TimerActiveState)
			assert.Equal(t, "timeout", status.RPMOStree.ServiceResult)
		})
	}
}

// TestAutoUpdateManagerRPMOStreeDropInsAreIndependentOfPolicy pins the
// asymmetry between the two updaters: rpm-ostree's drop-in booleans are plain
// presence facts that never feed its policy, which comes from its own
// configuration file.
func TestAutoUpdateManagerRPMOStreeDropInsAreIndependentOfPolicy(t *testing.T) {
	client := liveAutoUpdateClient(false, true)
	manager := NewAutoUpdateManager(client, false, true, autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=check\n"))

	status, err := manager.Status(context.Background())

	require.NoError(t, err)
	require.NotNil(t, status.RPMOStree)
	assert.True(t, status.RPMOStree.ServiceDropinsPresent)
	assert.True(t, status.RPMOStree.TimerDropinsPresent)
	assert.Equal(t, RPMOStreePolicyCheck, status.RPMOStree.Policy,
		"drop-in presence must not disturb rpm-ostree's configured policy")
}

// TestAutoUpdateManagerRereadsOnEveryCall proves the no-caching claim the
// file's doc comment makes, so no later documentation chunk can describe a
// shared or memoized parse that does not exist
// (docs/agents/skills/dont-claim-a-shared-parse-without-verifying-memoization.md).
func TestAutoUpdateManagerRereadsOnEveryCall(t *testing.T) {
	client := liveAutoUpdateClient(false, false)
	root := autoUpdateRoot(t, "[Daemon]\nAutomaticUpdatePolicy=check\n")
	manager := NewAutoUpdateManager(client, true, true, root)

	first, err := manager.Status(context.Background())
	require.NoError(t, err)
	require.NotNil(t, first.RPMOStree)
	assert.Equal(t, RPMOStreePolicyCheck, first.RPMOStree.Policy)
	assert.Len(t, client.calls, 8)

	// Change both sources underneath the manager. A cached manager would
	// keep reporting the first answer.
	require.NoError(t, os.WriteFile(filepath.Join(root, rpmOStreeConfigFile), []byte("[Daemon]\nAutomaticUpdatePolicy=apply\n"), 0o600))
	client.properties[bootcAutoUpdateTimerUnit]["ActiveState"] = "failed"

	second, err := manager.Status(context.Background())
	require.NoError(t, err)
	require.NotNil(t, second.RPMOStree)
	require.NotNil(t, second.Bootc)
	assert.Equal(t, RPMOStreePolicyApply, second.RPMOStree.Policy, "the configuration file is re-read on every Status call")
	assert.Equal(t, "failed", second.Bootc.TimerActiveState, "systemd is re-read on every Status call")
	assert.Len(t, client.calls, 16, "a second Status must make a second full set of D-Bus reads; nothing is cached")
}

// TestAutoUpdateManagerRejectsBogusNextTrigger pins the MaxInt64 guard the
// usec conversion inherits from internal/modules/backups, plus the "zero
// microseconds is no time at all, not the Unix epoch" rule.
func TestAutoUpdateManagerRejectsBogusNextTrigger(t *testing.T) {
	tests := map[string]any{
		"unscheduled timer":    uint64(0),
		"out-of-range instant": uint64(1) << 63,
		"wrongly typed value":  "soon",
		"missing from the map": nil,
	}

	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			client := liveAutoUpdateClient(false, false)
			if value == nil {
				delete(client.typeProperties[bootcAutoUpdateTimerUnit+":Timer"], "NextElapseUSecRealtime")
			} else {
				client.typeProperties[bootcAutoUpdateTimerUnit+":Timer"]["NextElapseUSecRealtime"] = value
			}
			manager := NewAutoUpdateManager(client, true, false, autoUpdateRoot(t, ""))

			status, err := manager.Status(context.Background())

			require.NoError(t, err)
			require.NotNil(t, status.Bootc)
			assert.True(t, status.Bootc.NextTrigger.IsZero(), "an unusable next-elapse value must report the zero time")
			assert.Equal(t, BootcPolicyApply, status.Bootc.Policy, "one unusable property must not disturb the rest of the payload")
		})
	}
}

// TestAutoUpdateManagerReadsNothingExecutable is the mechanical half of the
// "no lifecycle mutation" guarantee: with this import allowlist there is no
// os/exec, no syscall, and no net reachable from the file, so nothing here can
// run bootc, rpm-ostree, or systemctl at all -- the manager's only outward
// reach is the injected systemdClient and one os.ReadFile.
func TestAutoUpdateManagerReadsNothingExecutable(t *testing.T) {
	allowedImports := []string{`"context"`, `"math"`, `"os"`, `"path/filepath"`, `"time"`}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, autoUpdateManagerSourcePath, nil, parser.ParseComments)
	require.NoErrorf(t, err, "parsing %s", autoUpdateManagerSourcePath)

	require.NotEmptyf(t, file.Imports, "%s has no imports; the assertion below would be vacuous", autoUpdateManagerSourcePath)
	for _, imported := range file.Imports {
		assert.Containsf(t, allowedImports, imported.Path.Value, "%s must not import anything that can run a command", autoUpdateManagerSourcePath)
	}
}

// TestAutoUpdateManagerNeverNamesALifecycleSubcommand bans, at the source-text
// level, every bootc and rpm-ostree subcommand that changes the host. The
// manager reports on automatic updates; it must never be able to perform one,
// and naming a mutating subcommand anywhere in the file -- comments included --
// is treated as a violation so the ban cannot be softened into prose.
func TestAutoUpdateManagerNeverNamesALifecycleSubcommand(t *testing.T) {
	bannedTokens := []string{
		"upgrade", "rollback", "rebase", "deploy", "install", "uninstall",
		"ExecStart", "exec.Command", "os/exec", "systemctl",
		"StartUnit", "StopUnit", "RestartUnit", "EnableUnitFiles", "DisableUnitFiles",
	}

	source, err := os.ReadFile(autoUpdateManagerSourcePath)
	require.NoErrorf(t, err, "reading %s", autoUpdateManagerSourcePath)
	require.NotEmptyf(t, source, "%s is empty; the assertions below would be vacuous", autoUpdateManagerSourcePath)

	for _, banned := range bannedTokens {
		assert.NotContainsf(t, string(source), banned,
			"%s names %q; this file may only read systemd properties and rpm-ostree's configuration file, never mutate an updater's lifecycle",
			autoUpdateManagerSourcePath, banned)
	}
}

// TestAutoUpdateManagerCallsOnlyPropertyGettersAndReadFile walks the file's AST
// and pins the two outward call surfaces the chunk's acceptance criteria name:
// every method invoked on the injected client is one of the two property
// getters, and the only os package function called is ReadFile.
func TestAutoUpdateManagerCallsOnlyPropertyGettersAndReadFile(t *testing.T) {
	allowedClientMethods := []string{"GetUnitPropertiesContext", "GetUnitTypePropertiesContext"}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, autoUpdateManagerSourcePath, nil, parser.ParseComments)
	require.NoErrorf(t, err, "parsing %s", autoUpdateManagerSourcePath)

	clientCalls := 0
	osCalls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if identifier, ok := selector.X.(*ast.Ident); ok && identifier.Name == "os" {
			osCalls++
			assert.Equalf(t, "ReadFile", selector.Sel.Name, "%s may call no os function other than ReadFile", autoUpdateManagerSourcePath)
			return true
		}
		// m.client.<Method>(...) -- the only calls made on the systemd seam.
		inner, ok := selector.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "client" {
			return true
		}
		clientCalls++
		assert.Containsf(t, allowedClientMethods, selector.Sel.Name,
			"%s may call no systemd method other than the two read-only property getters", autoUpdateManagerSourcePath)
		return true
	})

	require.Equalf(t, 4, clientCalls, "expected exactly the four property reads one updater pair needs in %s", autoUpdateManagerSourcePath)
	require.Equalf(t, 1, osCalls, "expected exactly one os call (the rpm-ostreed.conf read) in %s", autoUpdateManagerSourcePath)
}

// TestAutoUpdateManagerReadsOneFixedConfigurationPath pins the single path the
// file may read, both as a source-level constant and through the production
// path() resolver, so a future change cannot quietly widen the file read into a
// directory walk or a caller-supplied path.
func TestAutoUpdateManagerReadsOneFixedConfigurationPath(t *testing.T) {
	assert.Equal(t, "etc/rpm-ostreed.conf", rpmOStreeConfigFile)

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, autoUpdateManagerSourcePath, nil, parser.ParseComments)
	require.NoErrorf(t, err, "parsing %s", autoUpdateManagerSourcePath)

	// Collect import-path literals so they can be excluded: an import spelled
	// with a slash ("path/filepath") is not a filesystem path the file reads.
	importPaths := make(map[string]bool, len(file.Imports))
	for _, imported := range file.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		require.NoError(t, err)
		importPaths[value] = true
	}

	var paths []string
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		require.NoError(t, err)
		// "/" is the default root (SystemManager's own pattern), not a path
		// the file reads; import paths are excluded above.
		if value == "/" || importPaths[value] {
			return true
		}
		if strings.Contains(value, "/") {
			paths = append(paths, value)
		}
		return true
	})

	assert.Equal(t, []string{"etc/rpm-ostreed.conf"}, paths,
		"%s may name exactly one filesystem path, rpm-ostree's daemon configuration", autoUpdateManagerSourcePath)
}

// TestAutoUpdateManagerUnitAllowlistMatchesTheCapabilityProbe pins the four
// unit names this manager reads to the spec's exact updater allowlist, the
// same four internal/capability's systemd probe keys the two Autoupdate*
// capabilities on.
func TestAutoUpdateManagerUnitAllowlistMatchesTheCapabilityProbe(t *testing.T) {
	assert.Equal(t, "bootc-fetch-apply-updates.timer", bootcAutoUpdateTimerUnit)
	assert.Equal(t, "bootc-fetch-apply-updates.service", bootcAutoUpdateServiceUnit)
	assert.Equal(t, "rpm-ostreed-automatic.timer", rpmOStreeAutoUpdateTimerUnit)
	assert.Equal(t, "rpm-ostreed-automatic.service", rpmOStreeAutoUpdateServiceUnit)
}
