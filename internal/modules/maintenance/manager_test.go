package maintenance

import (
	"context"
	"os"
	"path/filepath"
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
		&fakeRunner{}, root,
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
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{records: []jobs.Job{{Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}}, &fakeRunner{}, root)
	manager.now = func() time.Time { return now }
	state, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.False(t, state.RebootRequired)
}

func TestRebootUsesFixedSystemctlArguments(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewSystemManager(fakeUpdates{}, fakeJobs{}, runner, t.TempDir())
	require.NoError(t, manager.Reboot(context.Background()))
	assert.Equal(t, "systemctl", runner.name)
	assert.Equal(t, []string{"reboot", "--no-wall", "--no-block"}, runner.args)
}
