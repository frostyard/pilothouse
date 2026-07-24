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
	"github.com/frostyard/pilothouse/internal/modules/sysext/extctl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExtensions is the minimal sysext.ExtensionsSource stand-in for the tests
// that do not care about the extension leg at all. The real seam's contract is
// exercised by callCountingExtensions below.
type fakeExtensions struct {
	extensions []sysext.Extension
}

func (f fakeExtensions) State(context.Context, bool, bool) (sysext.ExtensionsState, error) {
	return sysext.ExtensionsState{Extensions: f.extensions, SysextAvailable: true, UpdexAvailable: true}, nil
}

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

func TestStateCombinesEveryRebootSignal(t *testing.T) {
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
		fakeExtensions{extensions: []sysext.Extension{{Name: "docker", Managed: true, Merged: true}}},
		fakeJobs{records: []jobs.Job{{Action: "update", Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}},
		nil, &fakeRunner{}, root, true, true, false,
	)
	manager.now = func() time.Time { return now }

	state, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Snosi · image 20260718", state.OSVersion)
	assert.True(t, state.RebootRequired)
	assert.Len(t, state.RebootReasons, 3)
}

func TestOldUpdateJobDoesNotRequireReboot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("60\n"), 0o644))
	now := time.Now().UTC()
	finished := now.Add(-time.Hour)
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{records: []jobs.Job{{Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}}, nil, &fakeRunner{}, root, true, true, false)
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
		fakeExtensions{extensions: []sysext.Extension{{Name: "docker", Enabled: true, Managed: true, Merged: true}}},
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
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, nil, runner, t.TempDir(), true, true, false)
	require.NoError(t, manager.Reboot(context.Background()))
	assert.Equal(t, "systemctl", runner.name)
	assert.Equal(t, []string{"reboot", "--no-wall", "--no-block"}, runner.args)
}

// TestStateSliceFieldsSerializeAsArrays verifies that when there are no reboot
// reasons and no jobs, the broker-serialized maintenance state uses JSON `[]`
// for the slice fields, never `null`. Downstream JSON consumers should not have
// to special-case null vs empty array.
//
// It also pins the ownership narrowing this chunk makes: `updates` is not a key
// of the serialized maintenance state at all any more — extension update
// availability belongs to broker.QueryExtensionsState, not here.
func TestStateSliceFieldsSerializeAsArrays(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("60\n"), 0o644))

	// fakeExtensions{} reports an empty inventory; no reboot marker and no jobs
	// means RebootReasons and Jobs are empty too.
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, nil, &fakeRunner{}, root, true, true, false)
	state, err := manager.State(context.Background())
	require.NoError(t, err)

	assert.NotNil(t, state.RebootReasons, "RebootReasons must be non-nil to serialize as []")
	assert.NotNil(t, state.Jobs, "Jobs must be non-nil to serialize as []")

	b, err := json.Marshal(state)
	require.NoError(t, err)
	out := string(b)
	assert.Contains(t, out, `"reboot_reasons":[]`)
	assert.Contains(t, out, `"jobs":[]`)
	assert.False(t, strings.Contains(out, `"reboot_reasons":null`), "reboot_reasons must not be null")
	assert.NotContains(t, out, `"updates"`, "maintenance state no longer carries extension update availability")
}

// callCountingExtensions is the sysext.ExtensionsSource stand-in the extension
// leg's tests drive. It records how many times State was invoked and the
// capability flags it was handed, so a test can prove extensionState threads
// the probed updex/sysext facts straight through to the source (which owns the
// never-attempt rule) and consults it exactly as often as the cache allows --
// not merely that the resulting State looks right.
type callCountingExtensions struct {
	calls int
	// err is returned verbatim, standing in for the error result
	// sysext.ExtensionsSource reserves for conditions outside per-source
	// reporting. The source never returns one for a source-level failure.
	err error
	// sawSysextAvailable and sawUpdexAvailable record the last flags passed.
	sawSysextAvailable bool
	sawUpdexAvailable  bool
	state              sysext.ExtensionsState
}

func (e *callCountingExtensions) State(_ context.Context, updexAvailable, sysextAvailable bool) (sysext.ExtensionsState, error) {
	e.calls++
	e.sawUpdexAvailable, e.sawSysextAvailable = updexAvailable, sysextAvailable
	return e.state, e.err
}

// mergedButDisabledState is the aggregate a healthy host with one
// merged-but-disabled extension reports: both sources answered, and docker is
// merged (active now) while disabled (not active after reboot). It also carries
// a pending update, so every assertion that maintenance no longer reports
// update availability is made against a source that actually has some.
func mergedButDisabledState() sysext.ExtensionsState {
	return sysext.ExtensionsState{
		Extensions: []sysext.Extension{{
			Managed:   true,
			Merged:    true,
			Name:      "docker",
			Updates:   []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}},
			Enabled:   false,
			Installed: true,
		}},
		SysextAvailable: true,
		UpdexAvailable:  true,
	}
}

// failedSourcesState is the aggregate a host whose updex and systemd-sysext are
// both present but both failed reports: no extensions, both *Available false,
// both *Error populated. Per sysext.ExtensionsSource's contract this arrives
// with a nil error -- source-level failures are reported in the state, never as
// the method's error -- which is exactly the shape maintenance must survive.
func failedSourcesState() sysext.ExtensionsState {
	return sysext.ExtensionsState{
		Extensions:  []sysext.Extension{},
		SysextError: "run systemd-sysext list: exit status 1",
		UpdexError:  "run updex features list: exit status 1",
	}
}

func newExtensionRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("60\n"), 0o644))
	return root
}

// newRebootMarkerRoot is newExtensionRoot plus the OS reboot marker, so a test
// can prove RebootRequired is still computed from the non-extension reasons
// when the extension leg contributes nothing at all.
func newRebootMarkerRoot(t *testing.T) string {
	t.Helper()
	root := newExtensionRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "run/reboot-required"), nil, 0o644))
	return root
}

// The tests below cover the extension leg of State's degrade table. Maintenance
// no longer branches per capability itself: it hands the probed
// updex/sysext facts to sysext.ExtensionsSource.State, which owns the
// never-attempt rule, and derives exactly one thing from the answer -- the
// merged-but-disabled reboot reason. In no combination, including a source that
// reports both of its own sources failed, does State() return an error.

// TestStateDerivesRebootReasonFromMergedButDisabledExtension is the positive
// case: a merged-but-disabled extension in the aggregate still produces its
// reboot reason, verbatim, exactly as it did when maintenance read Check()/
// List() itself. The probed capability flags are threaded through untouched.
func TestStateDerivesRebootReasonFromMergedButDisabledExtension(t *testing.T) {
	root := newExtensionRoot(t)
	source := &callCountingExtensions{state: mergedButDisabledState()}
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, root, true, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, source.calls, "the aggregate is read exactly once per State call")
	assert.True(t, source.sawUpdexAvailable, "the probed updex fact must reach the source")
	assert.True(t, source.sawSysextAvailable, "the probed sysext fact must reach the source")
	assert.Contains(t, state.RebootReasons, "docker is disabled but remains active until reboot.")
	assert.True(t, state.RebootRequired)
}

// TestStateThreadsProbedCapabilitiesIntoTheSource walks the whole
// updex x sysext flag matrix. Maintenance no longer decides which source to
// attempt -- it passes both probed facts down and the source decides -- so what
// is checkable here is that both flags arrive unmodified in every combination,
// and that State stays error-free in each.
func TestStateThreadsProbedCapabilitiesIntoTheSource(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		updex  bool
		sysext bool
	}{
		{name: "both present", updex: true, sysext: true},
		{name: "updex only", updex: true},
		{name: "sysext only", sysext: true},
		{name: "neither"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			source := &callCountingExtensions{state: sysext.ExtensionsState{Extensions: []sysext.Extension{}}}
			manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, newExtensionRoot(t), fixture.updex, fixture.sysext, false)

			state, err := manager.State(context.Background())

			require.NoError(t, err)
			assert.Equal(t, fixture.updex, source.sawUpdexAvailable)
			assert.Equal(t, fixture.sysext, source.sawSysextAvailable)
			assert.Empty(t, state.RebootReasons)
			assert.False(t, state.RebootRequired)
		})
	}
}

// TestStateWithBothExtensionSourcesFailedStillSucceeds is spec resolution 3's
// core claim: an extension provider that errors must not make Maintenance
// unavailable. The aggregate reports both of its sources unavailable with
// per-source errors (the shape sysext.ExtensionsSource actually produces for a
// command failure, since it never returns a method-level error for one), and
// State still answers with err == nil -- so QueryMaintenanceState, whose
// handler is a bare `return manager.State(ctx)`, stays a 200. The
// extension-derived reason is skipped; RebootRequired is computed purely from
// the OS marker and the completed reboot-requiring job.
func TestStateWithBothExtensionSourcesFailedStillSucceeds(t *testing.T) {
	root := newRebootMarkerRoot(t)
	now := time.Now().UTC()
	// newRebootMarkerRoot reports 60s of uptime, so a job that finished 30s ago
	// is genuinely post-boot and its reboot-requiring outcome counts.
	finished := now.Add(-30 * time.Second)
	source := &callCountingExtensions{state: failedSourcesState()}
	manager := NewSystemManager(
		source,
		fakeJobs{records: []jobs.Job{{Action: "update", Status: jobs.StatusSucceeded, RebootRequired: true, FinishedAt: &finished}}},
		nil, &fakeRunner{}, root, true, true, false,
	)
	manager.now = func() time.Time { return now }

	state, err := manager.State(context.Background())

	require.NoError(t, err, "an extension source failure must never fail State")
	assert.Equal(t, 1, source.calls)
	assert.ElementsMatch(t, []string{
		"The operating system requested a reboot.",
		"A completed extension update requires activation by reboot.",
	}, state.RebootReasons, "only the non-extension reasons may survive a failed extension read")
	assert.True(t, state.RebootRequired)
}

// TestStateWithExtensionSourceMethodErrorStillSucceeds covers the other half of
// the same guarantee. sysext.ExtensionsSource documents that its error result
// is reserved for conditions outside per-source reporting and is never returned
// for a source-level failure, but the contract still obliges callers to check
// it: maintenance handles one by degrading its extension contribution to
// nothing, exactly as hostImageState does for a failed host-image read, rather
// than propagating it.
func TestStateWithExtensionSourceMethodErrorStillSucceeds(t *testing.T) {
	root := newRebootMarkerRoot(t)
	source := &callCountingExtensions{err: errors.New("extensions state: unusable"), state: mergedButDisabledState()}
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, root, true, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err, "an extension source error must never fail State")
	assert.Equal(t, 1, source.calls)
	assert.NotContains(t, state.RebootReasons, "docker is disabled but remains active until reboot.")
	assert.Equal(t, []string{"The operating system requested a reboot."}, state.RebootReasons)
	assert.True(t, state.RebootRequired)
}

// TestStateSkipsExtensionReasonsWhenUpdexDidNotAnswer is the partial-degrade
// case, and the one that distinguishes a real implementation from a plausible
// one: systemd-sysext answers fully (so docker really is Merged) while updex
// does not (so Enabled is the Go zero value, not a fact). "Disabled" and
// "unknown" are the identical false on the wire here, and only
// UpdexAvailable/UpdexError separate them.
//
// Both ways updex can fail to answer are covered, because they produce
// different states: absent leaves UpdexError empty, while attempted-and-failed
// populates it. In neither may maintenance claim docker is disabled -- doing so
// is precisely the extension failure leaking into Maintenance that spec
// resolution 3 forbids. RebootRequired must still be computed from the
// non-extension reasons, so State stays useful rather than merely non-erroring.
func TestStateSkipsExtensionReasonsWhenUpdexDidNotAnswer(t *testing.T) {
	// One merged, updex-managed extension whose Enabled field nothing populated.
	// Under a healthy aggregate this exact extension does produce the reason
	// (see TestStateDerivesRebootReasonFromMergedButDisabledExtension), which is
	// what makes its absence below attributable to the guard and not to the
	// fixture being inert.
	extensions := []sysext.Extension{{Installed: true, Managed: true, Merged: true, Name: "docker"}}

	for _, fixture := range []struct {
		name  string
		state sysext.ExtensionsState
	}{
		{
			name:  "updex absent",
			state: sysext.ExtensionsState{Extensions: extensions, SysextAvailable: true},
		},
		{
			name: "updex attempted and failed",
			state: sysext.ExtensionsState{
				Extensions:      extensions,
				SysextAvailable: true,
				UpdexError:      "run updex features list: exit status 1",
			},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			source := &callCountingExtensions{state: fixture.state}
			manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, newRebootMarkerRoot(t), true, true, false)

			state, err := manager.State(context.Background())

			require.NoError(t, err, "a half-answered aggregate must never fail State")
			assert.NotContains(t, state.RebootReasons, "docker is disabled but remains active until reboot.",
				"Enabled is unknown, not false, when updex did not answer")
			assert.Equal(t, []string{"The operating system requested a reboot."}, state.RebootReasons,
				"only the non-extension reasons may survive an unanswered updex")
			assert.True(t, state.RebootRequired, "RebootRequired still follows the non-extension reasons")
		})
	}
}

// TestStateSkipsMergedExtensionUpdexDoesNotManage covers the same
// unknown-versus-false distinction one level down, per extension rather than
// per source. Both sources answered here, so the aggregate is entirely healthy
// -- but it is a *union*, and this extension was installed and merged straight
// through systemd-sysext with no updex definition behind it. Its Enabled is
// false because updex has nothing to say about it, not because anyone disabled
// it.
//
// This is also the behavior-parity case for the chunk: the List()-based code
// this replaced iterated updex's feature list alone, so an unmanaged extension
// could never reach the merged-but-disabled check. Reporting one now would be a
// new false reboot reason introduced by the switch to the union aggregate.
func TestStateSkipsMergedExtensionUpdexDoesNotManage(t *testing.T) {
	source := &callCountingExtensions{state: sysext.ExtensionsState{
		Extensions: []sysext.Extension{
			{Installed: true, Merged: true, Name: "vendor-blob"},
			{Enabled: true, Installed: true, Managed: true, Merged: true, Name: "docker"},
		},
		SysextAvailable: true,
		UpdexAvailable:  true,
	}}
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, newExtensionRoot(t), true, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.NotContains(t, state.RebootReasons, "vendor-blob is disabled but remains active until reboot.",
		"an extension updex does not manage has no known enabled-state")
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
}

// TestStateWithNoExtensionSourceInjectedStillSucceeds pins the nil-source
// backstop, mirroring hostImageState's: a manager constructed without an
// extensions seam contributes no extension-derived reasons and never panics.
func TestStateWithNoExtensionSourceInjectedStillSucceeds(t *testing.T) {
	manager := NewSystemManager(nil, fakeJobs{}, nil, &fakeRunner{}, newExtensionRoot(t), true, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
}

// TestExtensionAggregateIsCachedForOneMinute pins the pre-existing 1-minute
// cache across the new aggregate: repeated State calls inside the window read
// the source once and still derive the same reboot reason from the cached
// inventory, and the first call past the window reads it again. The cache is
// this manager's alone -- the sysext source it wraps has none -- so a
// concurrent QueryExtensionsState is unaffected either way.
func TestExtensionAggregateIsCachedForOneMinute(t *testing.T) {
	root := newExtensionRoot(t)
	source := &callCountingExtensions{state: mergedButDisabledState()}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, root, true, true, false)
	manager.now = func() time.Time { return now }

	first, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Contains(t, first.RebootReasons, "docker is disabled but remains active until reboot.")
	require.Equal(t, 1, source.calls)

	now = now.Add(59 * time.Second)
	cached, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, source.calls, "a second read inside the window must reuse the cached aggregate")
	assert.Contains(t, cached.RebootReasons, "docker is disabled but remains active until reboot.")

	now = now.Add(2 * time.Second)
	refreshed, err := manager.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, source.calls, "the first read past the window must refresh the aggregate")
	assert.Contains(t, refreshed.RebootReasons, "docker is disabled but remains active until reboot.")
}

// failingRunner fails every command, standing in for a host whose updex and
// systemd-sysext are installed but cannot answer.
type failingRunner struct{}

func (failingRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("exit status 1")
}

// TestSystemManagerConsumesTheRealSysextManager proves the seam State depends
// on is the one cmd/pilothoused actually passes it -- the same concrete
// *extctl.SystemManager instance registerExtensions serves
// broker.QueryExtensionsState from -- and that spec resolution 3 holds through
// that real implementation rather than only through a hand-written fake: with
// every command failing, the aggregate reports both sources errored, and
// State still answers with err == nil.
func TestSystemManagerConsumesTheRealSysextManager(t *testing.T) {
	var source sysext.ExtensionsSource = extctl.NewSystemManager(failingRunner{}, "", "updex")

	// The real source's own contract first: a command failure is reported in
	// the state, never as the method's error.
	aggregate, err := source.State(context.Background(), true, true)
	require.NoError(t, err)
	require.NotEmpty(t, aggregate.UpdexError)
	require.NotEmpty(t, aggregate.SysextError)

	manager := NewSystemManager(source, fakeJobs{}, nil, &fakeRunner{}, newExtensionRoot(t), true, true, false)

	state, err := manager.State(context.Background())

	require.NoError(t, err, "a real failing updex/systemd-sysext must not fail State")
	assert.Empty(t, state.RebootReasons)
	assert.False(t, state.RebootRequired)
}

// callCountingHostImage is the host-image analogue of callCountingExtensions: it
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
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, true)

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
		fakeExtensions{},
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
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, true)

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
			manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, &callCountingHostImage{status: status}, &fakeRunner{}, root, true, true, true)

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
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, false)

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
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, source, &fakeRunner{}, root, true, true, true)

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
	manager := NewSystemManager(fakeExtensions{}, fakeJobs{}, source, &fakeRunner{}, newExtensionRoot(t), true, true, true)

	state, err := manager.State(context.Background())

	require.NoError(t, err)
	assert.Nil(t, state.SoftRebootCapable)
	assert.Empty(t, state.RebootReasons)
}
