package maintenance

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostImageCall records one invocation of the injected Runner, so a test can
// assert not just what HostImageManager parsed but exactly which command lines
// it ran -- and, just as importantly, which it did not.
type hostImageCall struct {
	args []string
	name string
}

// fakeHostImageRunner answers per executable name: outputs[name] is returned
// on success, errors[name] (when set) is returned instead. An executable with
// no entry in either map returns empty output and no error, which reads as
// malformed to both parsers -- so a test that forgets to stub a command it
// expects to run fails loudly rather than silently passing.
type fakeHostImageRunner struct {
	calls  []hostImageCall
	errors map[string]error
	output map[string]string
}

func (r *fakeHostImageRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, hostImageCall{args: args, name: name})
	if err := r.errors[name]; err != nil {
		return nil, err
	}
	return []byte(r.output[name]), nil
}

// commandLines flattens the recorded calls into "name arg arg" strings for
// readable assertions about the full set of commands a Status call ran.
func (r *fakeHostImageRunner) commandLines() []string {
	lines := make([]string, 0, len(r.calls))
	for _, call := range r.calls {
		line := call.name
		for _, arg := range call.args {
			line += " " + arg
		}
		lines = append(lines, line)
	}
	return lines
}

// bothSourcesRunner stubs both executables with the matching pair of fixtures
// used throughout hostimage_test.go: two views of the same host, digest for
// digest.
func bothSourcesRunner() *fakeHostImageRunner {
	return &fakeHostImageRunner{output: map[string]string{
		"bootc":      bootcBootedStagedRollbackSoftReboot,
		"rpm-ostree": rpmOStreeBootedStagedRollback,
	}}
}

// TestHostImageManagerBootcOnly is the Snosi/bootc-without-rpm-ostree fixture:
// only bootc runs, only bootc-sourced fields are populated, and the untouched
// second source reports neither availability nor an error (it was never
// attempted).
func TestHostImageManagerBootcOnly(t *testing.T) {
	runner := bothSourcesRunner()
	manager := NewHostImageManager(runner, true, false)

	status, err := manager.Status(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"bootc status --json"}, runner.commandLines(), "an unavailable rpm-ostree must never be invoked")
	assert.True(t, status.BootcAvailable)
	assert.Empty(t, status.BootcError)
	require.NotNil(t, status.Booted)
	assert.Equal(t, "quay.io/frostyard/snosi:stable", status.Booted.Image)
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", status.Booted.Digest)
	require.NotNil(t, status.Staged)
	assert.Equal(t, "sha256:2222222222222222222222222222222222222222222222222222222222222222", status.Staged.Digest)
	require.NotNil(t, status.Rollback)
	assert.Equal(t, "sha256:3333333333333333333333333333333333333333333333333333333333333333", status.Rollback.Digest)
	require.NotNil(t, status.SoftRebootCapable)
	assert.True(t, *status.SoftRebootCapable)
	for name, deployment := range map[string]*Deployment{"booted": status.Booted, "staged": status.Staged, "rollback": status.Rollback} {
		assert.Empty(t, deployment.Version, "%s version is rpm-ostree-sourced and rpm-ostree never ran", name)
		assert.Empty(t, deployment.Checksum, "%s checksum is rpm-ostree-sourced and rpm-ostree never ran", name)
	}
	assert.False(t, status.RPMOStreeAvailable, "rpm-ostree was never attempted, so it is not available")
	assert.Empty(t, status.RPMOStreeError, "not attempting a source is not an error about it")
}

// TestHostImageManagerBootcPlusRPMOStree is the uCore fixture: both sources
// run, and the result carries bootc's deployment identity plus rpm-ostree's
// supplementary detail per MergeHostImage's contract, with both availability
// flags true and neither error set.
func TestHostImageManagerBootcPlusRPMOStree(t *testing.T) {
	runner := bothSourcesRunner()
	manager := NewHostImageManager(runner, true, true)

	status, err := manager.Status(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"bootc status --json", "rpm-ostree status --json"}, runner.commandLines())
	assert.True(t, status.BootcAvailable)
	assert.Empty(t, status.BootcError)
	assert.True(t, status.RPMOStreeAvailable)
	assert.Empty(t, status.RPMOStreeError)

	require.NotNil(t, status.Booted)
	assert.Equal(t, "quay.io/frostyard/snosi:stable", status.Booted.Image, "bootc owns the image reference")
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", status.Booted.Digest)
	assert.Equal(t, "41.20260701.0", status.Booted.Version, "rpm-ostree supplies the version")
	assert.Equal(t, "aaaa111111111111111111111111111111111111111111111111111111111111", status.Booted.Checksum)

	require.NotNil(t, status.Staged)
	assert.Equal(t, "sha256:2222222222222222222222222222222222222222222222222222222222222222", status.Staged.Digest)
	assert.Equal(t, "41.20260715.0", status.Staged.Version)
	assert.Equal(t, "bbbb222222222222222222222222222222222222222222222222222222222222", status.Staged.Checksum)

	require.NotNil(t, status.Rollback)
	assert.Equal(t, "sha256:3333333333333333333333333333333333333333333333333333333333333333", status.Rollback.Digest)
	assert.Equal(t, "41.20260610.0", status.Rollback.Version)
	assert.Equal(t, "cccc333333333333333333333333333333333333333333333333333333333333", status.Rollback.Checksum)

	require.NotNil(t, status.SoftRebootCapable)
	assert.True(t, *status.SoftRebootCapable)
}

// TestHostImageManagerRPMOStreeOnly proves rpm-ostree cannot stand alone as a
// host-image report even when it is the only source present: it runs, it is
// reported available, and it still contributes no deployment, because bootc
// owns deployment identity outright.
func TestHostImageManagerRPMOStreeOnly(t *testing.T) {
	runner := bothSourcesRunner()
	manager := NewHostImageManager(runner, false, true)

	status, err := manager.Status(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"rpm-ostree status --json"}, runner.commandLines(), "an unavailable bootc must never be invoked")
	assert.False(t, status.BootcAvailable)
	assert.Empty(t, status.BootcError, "bootc was never attempted, so there is no bootc failure to report")
	assert.True(t, status.RPMOStreeAvailable)
	assert.Empty(t, status.RPMOStreeError)
	assert.Nil(t, status.Booted, "rpm-ostree may supplement deployments, never invent them")
	assert.Nil(t, status.Staged)
	assert.Nil(t, status.Rollback)
	assert.Nil(t, status.SoftRebootCapable)
}

// TestHostImageManagerNeitherSourceAvailable is the Snosi-without-bootc case
// the spec calls out: host-image state is omitted rather than failing, and
// nothing at all is executed.
func TestHostImageManagerNeitherSourceAvailable(t *testing.T) {
	runner := bothSourcesRunner()
	manager := NewHostImageManager(runner, false, false)

	status, err := manager.Status(context.Background())
	require.NoError(t, err)

	assert.Empty(t, runner.commandLines(), "no source is available, so no command may run")
	assert.Equal(t, HostImageStatus{}, status, "an absent host-image stack yields an omitted, not errored, report")
}

// TestHostImageManagerBootcExecFailure covers bootc present but unrunnable
// (non-zero exit, missing binary, context cancellation): the failure degrades
// bootc alone, is reported on the result rather than as the method's error,
// and leaves rpm-ostree's half of the report intact.
func TestHostImageManagerBootcExecFailure(t *testing.T) {
	runner := bothSourcesRunner()
	runner.errors = map[string]error{"bootc": errors.New("bootc: exit status 1")}
	manager := NewHostImageManager(runner, true, true)

	status, err := manager.Status(context.Background())
	require.NoError(t, err, "a source-level failure is reported on the result, never as the method's error")

	assert.Equal(t, []string{"bootc status --json", "rpm-ostree status --json"}, runner.commandLines(), "one source failing must not stop the other from being read")
	assert.False(t, status.BootcAvailable)
	assert.Contains(t, status.BootcError, "exit status 1")
	assert.Nil(t, status.Booted, "a failed bootc read contributes no deployments")
	assert.True(t, status.RPMOStreeAvailable, "rpm-ostree is unaffected by bootc's failure")
	assert.Empty(t, status.RPMOStreeError)
}

// TestHostImageManagerBootcMalformedOutput is the second, distinct bootc
// failure mode: the command ran fine but said something unusable. It must take
// the same BootcAvailable=false/BootcError path as an exec failure.
func TestHostImageManagerBootcMalformedOutput(t *testing.T) {
	runner := bothSourcesRunner()
	runner.output["bootc"] = `{"kind":"NotBootcHost"}`
	manager := NewHostImageManager(runner, true, true)

	status, err := manager.Status(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"bootc status --json", "rpm-ostree status --json"}, runner.commandLines())
	assert.False(t, status.BootcAvailable)
	assert.Contains(t, status.BootcError, "parse bootc status")
	assert.Nil(t, status.Booted)
	assert.True(t, status.RPMOStreeAvailable)
	assert.Empty(t, status.RPMOStreeError)
}

// TestHostImageManagerRPMOStreeExecFailure mirrors the bootc exec-failure test
// exactly, for the second source: rpm-ostree is advertised but its command
// fails, which degrades rpm-ostree alone and leaves every bootc-sourced field
// untouched.
func TestHostImageManagerRPMOStreeExecFailure(t *testing.T) {
	runner := bothSourcesRunner()
	runner.errors = map[string]error{"rpm-ostree": errors.New("rpm-ostree: exit status 1")}
	manager := NewHostImageManager(runner, true, true)

	status, err := manager.Status(context.Background())
	require.NoError(t, err, "a source-level failure is reported on the result, never as the method's error")

	assert.Equal(t, []string{"bootc status --json", "rpm-ostree status --json"}, runner.commandLines())
	assert.False(t, status.RPMOStreeAvailable)
	assert.Contains(t, status.RPMOStreeError, "exit status 1")

	assert.True(t, status.BootcAvailable, "bootc is unaffected by rpm-ostree's failure")
	assert.Empty(t, status.BootcError)
	require.NotNil(t, status.Booted)
	assert.Equal(t, "quay.io/frostyard/snosi:stable", status.Booted.Image)
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", status.Booted.Digest)
	require.NotNil(t, status.Staged)
	assert.Equal(t, "sha256:2222222222222222222222222222222222222222222222222222222222222222", status.Staged.Digest)
	require.NotNil(t, status.Rollback)
	assert.Equal(t, "sha256:3333333333333333333333333333333333333333333333333333333333333333", status.Rollback.Digest)
	require.NotNil(t, status.SoftRebootCapable)
	assert.True(t, *status.SoftRebootCapable)
	assert.Empty(t, status.Booted.Version, "the failed source's supplementary detail is simply absent")
	assert.Empty(t, status.Booted.Checksum)
}

// TestHostImageManagerRPMOStreeMalformedOutput is the second, distinct
// rpm-ostree failure mode, symmetric to bootc's: a successful command whose
// output cannot be read takes the same RPMOStreeAvailable=false/RPMOStreeError
// path as an exec failure, and bootc's half of the report survives.
func TestHostImageManagerRPMOStreeMalformedOutput(t *testing.T) {
	runner := bothSourcesRunner()
	runner.output["rpm-ostree"] = `{"transaction": null}`
	manager := NewHostImageManager(runner, true, true)

	status, err := manager.Status(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"bootc status --json", "rpm-ostree status --json"}, runner.commandLines())
	assert.False(t, status.RPMOStreeAvailable)
	assert.Contains(t, status.RPMOStreeError, "parse rpm-ostree status")

	assert.True(t, status.BootcAvailable, "bootc is unaffected by rpm-ostree's malformed output")
	assert.Empty(t, status.BootcError)
	require.NotNil(t, status.Booted)
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", status.Booted.Digest)
	assert.Empty(t, status.Booted.Version)
	assert.Empty(t, status.Booted.Checksum)
}

// TestHostImageManagerBothSourcesFail closes the failure matrix: with both
// sources advertised and both failing, each reports its own error, the report
// is empty rather than wrong, and Status still returns no error of its own.
func TestHostImageManagerBothSourcesFail(t *testing.T) {
	runner := bothSourcesRunner()
	runner.errors = map[string]error{"bootc": errors.New("bootc: exit status 1"), "rpm-ostree": errors.New("rpm-ostree: exit status 2")}
	manager := NewHostImageManager(runner, true, true)

	status, err := manager.Status(context.Background())
	require.NoError(t, err)

	assert.False(t, status.BootcAvailable)
	assert.Contains(t, status.BootcError, "exit status 1")
	assert.False(t, status.RPMOStreeAvailable)
	assert.Contains(t, status.RPMOStreeError, "exit status 2")
	assert.Nil(t, status.Booted)
	assert.Nil(t, status.Staged)
	assert.Nil(t, status.Rollback)
}

// TestHostImageManagerRunsOnlyReadOnlyStatusCommands is the mechanical
// counterpart to hostimage.go's own no-execution test: across every
// availability combination, the injected Runner only ever sees
// `bootc status --json` and `rpm-ostree status --json`, each at most once --
// no second subcommand, no lifecycle verb, no repeat invocation. Combined with
// the import check below (nothing in this file can reach exec or a shell), the
// injected Runner is provably the only way a command leaves this package.
func TestHostImageManagerRunsOnlyReadOnlyStatusCommands(t *testing.T) {
	for _, combination := range []struct {
		bootc     bool
		name      string
		rpmOStree bool
		want      []string
	}{
		{name: "neither", want: []string{}},
		{name: "bootc only", bootc: true, want: []string{"bootc status --json"}},
		{name: "rpm-ostree only", rpmOStree: true, want: []string{"rpm-ostree status --json"}},
		{name: "both", bootc: true, rpmOStree: true, want: []string{"bootc status --json", "rpm-ostree status --json"}},
	} {
		t.Run(combination.name, func(t *testing.T) {
			runner := bothSourcesRunner()
			manager := NewHostImageManager(runner, combination.bootc, combination.rpmOStree)

			_, err := manager.Status(context.Background())
			require.NoError(t, err)

			assert.Equal(t, combination.want, runner.commandLines())
		})
	}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "hostimage_manager.go", nil, parser.ParseComments)
	require.NoError(t, err)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		assert.Equal(t, "context", path, "hostimage_manager.go must reach the host only through its injected Runner")
	}
}
