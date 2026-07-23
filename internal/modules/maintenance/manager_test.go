package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeUpdates struct {
	features []sysext.Feature
	updates  []sysext.AvailableUpdate
}

func (f fakeUpdates) Check(context.Context) ([]sysext.AvailableUpdate, error) { return f.updates, nil }
func (f fakeUpdates) List(context.Context) ([]sysext.Feature, error)          { return f.features, nil }

type fakeJobs struct{ records []jobs.Job }

func (f fakeJobs) List(context.Context, jobs.Filter) ([]jobs.Job, error) { return f.records, nil }
func (f fakeJobs) RebootRequiredSince(_ context.Context, since time.Time) (bool, error) {
	for _, job := range f.records {
		if job.Status == jobs.StatusSucceeded && job.RebootRequired && job.FinishedAt != nil && job.FinishedAt.After(since) {
			return true, nil
		}
	}
	return false, nil
}

type fakeRunner struct {
	args []string
	name string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, args
	return nil, nil
}

func TestStateCombinesUpdatesAndRebootSignals(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("3600.00 0.00\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc/os-release"), []byte("PRETTY_NAME=\"Snosi\"\nIMAGE_VERSION=20260718\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "run/reboot-required"), nil, 0o644))
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-10 * time.Minute)
	manager := NewSystemManager(
		fakeUpdates{updates: []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}}, features: []sysext.Feature{{Name: "docker", Merged: true}}},
		fakeJobs{records: []jobs.Job{{Action: "update", Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}},
		nil, &fakeRunner{}, root, true, true, false,
	)
	manager.now = func() time.Time { return now }

	state, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Snosi · image 20260718", state.OSVersion)
	assert.Len(t, state.Updates, 1)
	assert.True(t, state.RebootRequired)
	assert.Len(t, state.RebootReasons, 3)
}

func TestOldUpdateJobDoesNotRequireReboot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("60\n"), 0o644))
	now := time.Now().UTC()
	finished := now.Add(-time.Hour)
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{records: []jobs.Job{{Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}}, nil, &fakeRunner{}, root, true, true, false)
	manager.now = func() time.Time { return now }
	state, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.False(t, state.RebootRequired)
}

func TestEnabledMergedExtensionDoesNotRequireReboot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("60\n"), 0o644))
	manager := NewSystemManager(
		fakeUpdates{features: []sysext.Feature{{Name: "docker", Enabled: true, Merged: true}}},
		fakeJobs{},
		nil,
		&fakeRunner{},
		root, true, true, false,
	)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.False(t, state.RebootRequired)
	assert.Empty(t, state.RebootReasons)
}

func TestRebootUsesFixedSystemctlArguments(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, nil, runner, t.TempDir(), true, true, false)
	require.NoError(t, manager.Reboot(context.Background()))
	assert.Equal(t, "systemctl", runner.name)
	assert.Equal(t, []string{"reboot", "--no-wall", "--no-block"}, runner.args)
}

// TestStateSliceFieldsSerializeAsArrays verifies that when there are no
// updates and no reboot reasons, the broker-serialized maintenance state
// uses JSON `[]` for the slice fields, never `null`. Downstream JSON
// consumers should not have to special-case null vs empty array.
func TestStateSliceFieldsSerializeAsArrays(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("60\n"), 0o644))

	// fakeUpdates{} returns a nil updates slice from Check(); no reboot marker
	// and no jobs means RebootReasons and Jobs are empty too.
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, nil, &fakeRunner{}, root, true, true, false)
	state, err := manager.State(context.Background())
	require.NoError(t, err)

	assert.NotNil(t, state.Updates, "Updates must be non-nil to serialize as []")
	assert.NotNil(t, state.RebootReasons, "RebootReasons must be non-nil to serialize as []")

	b, err := json.Marshal(state)
	require.NoError(t, err)
	out := string(b)
	assert.Contains(t, out, `"updates":[]`)
	assert.Contains(t, out, `"reboot_reasons":[]`)
	assert.False(t, strings.Contains(out, `"updates":null`), "updates must not be null")
	assert.False(t, strings.Contains(out, `"reboot_reasons":null`), "reboot_reasons must not be null")
}

// callCountingUpdates tracks how many times Check/List are invoked, so the
// updex/sysext presence combination tests below can assert that
// extensionState skips Check()/List() entirely when updex is absent, rather
// than merely asserting on the returned data.
type callCountingUpdates struct {
	checkCalls int
	features   []sysext.Feature
	listCalls  int
	updates    []sysext.AvailableUpdate
}

func (u *callCountingUpdates) Check(context.Context) ([]sysext.AvailableUpdate, error) {
	u.checkCalls++
	return u.updates, nil
}

func (u *callCountingUpdates) List(context.Context) ([]sysext.Feature, error) {
	u.listCalls++
	return u.features, nil
}

func newExtensionRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("60\n"), 0o644))
	return root
}

// The four tests below cover the degrade table from the chunk spec, grounded
// in what sysext.SystemManager's Check()/List() actually depend on: Check()
// only ever invokes updex, while List() invokes updex to enumerate feature
// definitions and additionally systemd-sysext to attach installed/merged
// status. In no combination does State() return an error.

func TestStateWithUpdexAndSysextBothPresentPopulatesUpdatesAndFeatureReasons(t *testing.T) {
	root := newExtensionRoot(t)
	source := &callCountingUpdates{
		updates:  []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}},
		features: []sysext.Feature{{Name: "docker", Merged: true}},
	}
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, root, true, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, source.checkCalls)
	assert.Equal(t, 1, source.listCalls)
	assert.Len(t, state.Updates, 1)
	assert.Contains(t, state.RebootReasons, "docker is disabled but remains active until reboot.")
	assert.True(t, state.RebootRequired)
}

func TestStateWithUpdexPresentSysextAbsentSkipsFeatureDerivedReasons(t *testing.T) {
	root := newExtensionRoot(t)
	source := &callCountingUpdates{
		updates:  []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}},
		features: []sysext.Feature{{Name: "docker", Merged: true}},
	}
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, root, true, false, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, source.checkCalls, "Check never touches systemd-sysext, so it still runs")
	assert.Equal(t, 0, source.listCalls, "List's installed/merged status is meaningless without systemd-sysext")
	assert.Len(t, state.Updates, 1)
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
}

func TestStateWithUpdexAbsentSysextPresentOmitsUpdatesAndFeatureReasons(t *testing.T) {
	root := newExtensionRoot(t)
	source := &callCountingUpdates{
		updates:  []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}},
		features: []sysext.Feature{{Name: "docker", Merged: true}},
	}
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, root, false, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 0, source.checkCalls, "neither Check nor List can enumerate feature definitions without updex")
	assert.Equal(t, 0, source.listCalls)
	assert.Empty(t, state.Updates)
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
}

func TestStateWithUpdexAndSysextBothAbsentOmitsUpdatesAndFeatureReasons(t *testing.T) {
	root := newExtensionRoot(t)
	source := &callCountingUpdates{
		updates:  []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}},
		features: []sysext.Feature{{Name: "docker", Merged: true}},
	}
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, root, false, false, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 0, source.checkCalls, "identical observable output to the updex-absent/sysext-present case")
	assert.Equal(t, 0, source.listCalls)
	assert.Empty(t, state.Updates)
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
}

// callCountingHostImage is the host-image analogue of callCountingUpdates: it
// records how many times Status is invoked so the bootc presence tests below
// can assert that State skips the source *entirely* when bootc is absent,
// rather than merely asserting on the resulting state (which a source that ran
// and had its result discarded would satisfy too).
type callCountingHostImage struct {
	err         error
	status      HostImageStatus
	statusCalls int
}

func (h *callCountingHostImage) Status(context.Context) (HostImageStatus, error) {
	h.statusCalls++
	return h.status, h.err
}

func boolPtr(value bool) *bool { return &value }

// stagedHostImage is a host-image status with a staged deployment waiting for
// activation, i.e. the fact State must turn into a reboot reason.
func stagedHostImage() HostImageStatus {
	return HostImageStatus{
		BootcAvailable: true,
		Booted:         &Deployment{Image: "quay.io/example/os:latest", Digest: "sha256:booted"},
		Staged:         &Deployment{Image: "quay.io/example/os:latest", Digest: "sha256:staged"},
	}
}

// bootedOnlyHostImage is a host-image status from a host with nothing pending:
// booted deployment only, no staged deployment.
func bootedOnlyHostImage() HostImageStatus {
	return HostImageStatus{
		BootcAvailable: true,
		Booted:         &Deployment{Image: "quay.io/example/os:latest", Digest: "sha256:booted"},
	}
}

// The tests below cover the bootc leg of State's degrade table. It mirrors the
// updex/sysext leg above: the source is consulted only when its probed
// capability flag is true, and in no combination -- absent bootc, failing
// bootc, bootc with nothing staged -- does State return an error.

func TestStateWithBootcStagedDeploymentRequiresReboot(t *testing.T) {
	root := newExtensionRoot(t)
	source := &callCountingHostImage{status: stagedHostImage()}
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, true)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, source.statusCalls, "the host-image source is read exactly once per State call")
	assert.Contains(t, state.RebootReasons, stagedHostImageReason)
	assert.True(t, state.RebootRequired)
}

// TestStateWithBootcNothingStagedKeepsExistingRebootReasons pins the negative
// half of the staged-deployment rule together with the two reason sources that
// predate it: with bootc reporting nothing staged, the /run/reboot-required
// marker and the completed reboot-requiring job must still produce their own
// reasons, and no staged-deployment reason may appear.
func TestStateWithBootcNothingStagedKeepsExistingRebootReasons(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("3600.00 0.00\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "run/reboot-required"), nil, 0o644))
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-10 * time.Minute)
	source := &callCountingHostImage{status: bootedOnlyHostImage()}
	manager := NewSystemManager(
		fakeUpdates{},
		fakeJobs{records: []jobs.Job{{Action: "update", Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}},
		source, &fakeRunner{}, root, true, true, true,
	)
	manager.now = func() time.Time { return now }

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, source.statusCalls)
	assert.NotContains(t, state.RebootReasons, stagedHostImageReason)
	assert.Contains(t, state.RebootReasons, "The operating system requested a reboot.")
	assert.Contains(t, state.RebootReasons, "A completed extension update requires activation by reboot.")
	assert.True(t, state.RebootRequired)
}

// TestStateSoftRebootCapableIsInformationalOnly proves the second, independent
// use of the same host-image read: eligibility is copied onto State whether or
// not anything is staged, and it never makes a reboot required by itself.
func TestStateSoftRebootCapableIsInformationalOnly(t *testing.T) {
	root := newExtensionRoot(t)
	status := bootedOnlyHostImage()
	status.SoftRebootCapable = boolPtr(true)
	source := &callCountingHostImage{status: status}
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, true)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	require.NotNil(t, state.SoftRebootCapable)
	assert.True(t, *state.SoftRebootCapable)
	assert.Empty(t, state.RebootReasons, "soft-reboot eligibility is not a reboot reason")
	assert.False(t, state.RebootRequired, "soft-reboot eligibility alone never requires a reboot")

	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"soft_reboot_capable":true`)
}

// TestStateCopiesSoftRebootCapableVerbatim walks all three states of the
// pointer. The nil case is the one that matters most: an older bootc that does
// not report eligibility must leave State.SoftRebootCapable nil (and the JSON
// key absent), never a synthesized false, so "unknown" and "not eligible" stay
// distinguishable end to end.
func TestStateCopiesSoftRebootCapableVerbatim(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		source      *bool
		wantJSON    string
		wantOmitted bool
	}{
		{name: "reported true", source: boolPtr(true), wantJSON: `"soft_reboot_capable":true`},
		{name: "reported false", source: boolPtr(false), wantJSON: `"soft_reboot_capable":false`},
		{name: "not reported", source: nil, wantOmitted: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := newExtensionRoot(t)
			status := bootedOnlyHostImage()
			status.SoftRebootCapable = testCase.source
			manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, &callCountingHostImage{status: status}, &fakeRunner{}, root, true, true, true)

			state, err := manager.State(context.Background())

			require.NoError(t, err)
			encoded, err := json.Marshal(state)
			require.NoError(t, err)
			if testCase.wantOmitted {
				assert.Nil(t, state.SoftRebootCapable, "an unreported value must stay nil, not become false")
				assert.NotContains(t, string(encoded), "soft_reboot_capable")
				return
			}
			require.NotNil(t, state.SoftRebootCapable)
			assert.Equal(t, *testCase.source, *state.SoftRebootCapable)
			assert.Contains(t, string(encoded), testCase.wantJSON)
		})
	}
}

// TestStateWithBootcAbsentNeverReadsHostImageSource is the counterpart of the
// updex-absent cases: the source is injected and would report both a staged
// deployment and soft-reboot eligibility, but with bootcAvailable false it is
// never asked, so neither fact reaches State.
func TestStateWithBootcAbsentNeverReadsHostImageSource(t *testing.T) {
	root := newExtensionRoot(t)
	status := stagedHostImage()
	status.SoftRebootCapable = boolPtr(true)
	source := &callCountingHostImage{status: status}
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 0, source.statusCalls, "a host without bootc must not be asked for host-image status at all")
	assert.NotContains(t, state.RebootReasons, stagedHostImageReason)
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
	assert.Nil(t, state.SoftRebootCapable)
}

// TestStateWithHostImageSourceErrorStillSucceeds pins the non-fatal contract:
// source availability and errors are QueryHostImageStatus's to report, so a
// bootc that cannot answer degrades this call's host-image contribution to
// nothing instead of failing the whole maintenance posture.
func TestStateWithHostImageSourceErrorStillSucceeds(t *testing.T) {
	root := newExtensionRoot(t)
	status := stagedHostImage()
	status.SoftRebootCapable = boolPtr(true)
	source := &callCountingHostImage{err: errors.New("bootc status: exit status 1"), status: status}
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, true)

	state, err := manager.State(context.Background())

	require.NoError(t, err, "a host-image read failure must not fail State")
	assert.Equal(t, 1, source.statusCalls)
	assert.NotContains(t, state.RebootReasons, stagedHostImageReason)
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
	assert.Nil(t, state.SoftRebootCapable, "a failed read reports nothing, not a synthesized false")
}

// TestSystemManagerConsumesTheRealHostImageManager proves the seam State
// depends on is the one cmd/pilothoused actually passes it: the concrete
// *HostImageManager built for QueryHostImageStatus satisfies HostImageSource,
// so the daemon wires one reader into both consumers rather than opening a
// second path to bootc.
func TestSystemManagerConsumesTheRealHostImageManager(t *testing.T) {
	var source HostImageSource = NewHostImageManager(&fakeRunner{}, false, false)
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, source, &fakeRunner{}, newExtensionRoot(t), true, true, true)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Nil(t, state.SoftRebootCapable)
	assert.Empty(t, state.RebootReasons)
}
