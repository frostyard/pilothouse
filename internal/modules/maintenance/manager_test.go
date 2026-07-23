package maintenance

import (
	"context"
	"encoding/json"
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
		&fakeRunner{}, root, true, true,
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
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{records: []jobs.Job{{Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}}, &fakeRunner{}, root, true, true)
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
		&fakeRunner{},
		root, true, true,
	)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.False(t, state.RebootRequired)
	assert.Empty(t, state.RebootReasons)
}

func TestRebootUsesFixedSystemctlArguments(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, runner, t.TempDir(), true, true)
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
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, &fakeRunner{}, root, true, true)
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
	manager := NewSystemManager(source, fakeJobs{}, &fakeRunner{}, root, true, true)

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
	manager := NewSystemManager(source, fakeJobs{}, &fakeRunner{}, root, true, false)

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
	manager := NewSystemManager(source, fakeJobs{}, &fakeRunner{}, root, false, true)

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
	manager := NewSystemManager(source, fakeJobs{}, &fakeRunner{}, root, false, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 0, source.checkCalls, "identical observable output to the updex-absent/sysext-present case")
	assert.Equal(t, 0, source.listCalls)
	assert.Empty(t, state.Updates)
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
}
