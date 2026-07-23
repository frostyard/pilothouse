package capability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// call records one CommandRunner.Run invocation, including the context it
// was given, so tests can assert both the exact argv and the applied
// deadline.
type call struct {
	ctx  context.Context
	name string
	args []string
}

// fakeCommandRunner is a CommandRunner test double: it never shells out --
// it just records invocations and returns configured canned results.
type fakeCommandRunner struct {
	output []byte
	err    error
	calls  []call
}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{ctx: ctx, name: name, args: append([]string(nil), args...)})
	return f.output, f.err
}

// assertBoundedTimeout asserts ctx carries a deadline no later than
// execProbeTimeout from start, and that some positive budget remains --
// i.e. the probe applied its own bounded timeout to the context it handed
// the runner, rather than passing the caller's (possibly undeadlined)
// context straight through.
func assertBoundedTimeout(t *testing.T, ctx context.Context, start time.Time) {
	t.Helper()
	const slack = 500 * time.Millisecond // wall-clock tolerance between start and the WithTimeout call
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "probe must attach a deadline to the context passed to the runner")
	assert.LessOrEqual(t, deadline.Sub(start), execProbeTimeout+slack, "deadline must not exceed the spec's 5-second figure")
	assert.Greater(t, time.Until(deadline), time.Duration(0), "deadline must still be in the future")
}

func TestProbeUpdexPresentOnSuccess(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("usage: updex ...")}
	s := ProbeUpdex(context.Background(), runner, "updex")

	assert.True(t, s.Has(Updex))
	assert.ElementsMatch(t, []ID{Updex}, s.List())
}

func TestProbeUpdexAbsentOnNonZeroExit(t *testing.T) {
	runner := &fakeCommandRunner{err: errors.New("exit status 127")}
	s := ProbeUpdex(context.Background(), runner, "updex")

	assert.False(t, s.Has(Updex))
	assert.Empty(t, s.List())
}

func TestProbeUpdexInvocationIsExactlyHelp(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("usage: updex ...")}
	ProbeUpdex(context.Background(), runner, "updex")

	require.Len(t, runner.calls, 1)
	assert.Equal(t, "updex", runner.calls[0].name)
	assert.Equal(t, []string{"--help"}, runner.calls[0].args,
		"probe must not invoke --json/features or pass a definitions-root argument")
}

func TestProbeUpdexResolvesEmptyExecutableToPathLookup(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("usage: updex ...")}
	s := ProbeUpdex(context.Background(), runner, "")

	require.Len(t, runner.calls, 1)
	assert.Equal(t, "updex", runner.calls[0].name)
	assert.True(t, s.Has(Updex))
}

func TestProbeUpdexUsesConfiguredExecutable(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("usage: updex ...")}
	ProbeUpdex(context.Background(), runner, "/opt/bin/updex")

	require.Len(t, runner.calls, 1)
	assert.Equal(t, "/opt/bin/updex", runner.calls[0].name)
	assert.Equal(t, []string{"--help"}, runner.calls[0].args)
}

func TestProbeUpdexAppliesBoundedTimeout(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("usage: updex ...")}
	start := time.Now()
	ProbeUpdex(context.Background(), runner, "updex")

	require.Len(t, runner.calls, 1)
	assertBoundedTimeout(t, runner.calls[0].ctx, start)
}

func TestProbeSysextPresentOnSuccess(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("NAME TYPE PATH\n")}
	s := ProbeSysext(context.Background(), runner)

	assert.True(t, s.Has(Sysext))
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "systemd-sysext", runner.calls[0].name)
	assert.Equal(t, []string{"list"}, runner.calls[0].args)
}

func TestProbeSysextAbsentOnNonZeroExit(t *testing.T) {
	runner := &fakeCommandRunner{err: errors.New("exec: \"systemd-sysext\": executable file not found in $PATH")}
	s := ProbeSysext(context.Background(), runner)

	assert.False(t, s.Has(Sysext))
	assert.Empty(t, s.List())
}

func TestProbeSysextAppliesBoundedTimeout(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("NAME TYPE PATH\n")}
	start := time.Now()
	ProbeSysext(context.Background(), runner)

	require.Len(t, runner.calls, 1)
	assertBoundedTimeout(t, runner.calls[0].ctx, start)
}

func TestProbeBootcPresentOnSuccessWithJSONOutput(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte(`{"status":{}}`)}
	s := ProbeBootc(context.Background(), runner)

	assert.True(t, s.Has(Bootc))
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "bootc", runner.calls[0].name)
	assert.Equal(t, []string{"status", "--json"}, runner.calls[0].args)
}

func TestProbeBootcAbsentOnNonZeroExit(t *testing.T) {
	runner := &fakeCommandRunner{err: errors.New("exit status 1")}
	s := ProbeBootc(context.Background(), runner)

	assert.False(t, s.Has(Bootc))
	assert.Empty(t, s.List())
}

func TestProbeBootcAbsentOnNonJSONOutput(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("bootc is not supported on this host\n")}
	s := ProbeBootc(context.Background(), runner)

	assert.False(t, s.Has(Bootc))
	assert.Empty(t, s.List())
}

func TestProbeBootcAppliesBoundedTimeout(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte(`{"status":{}}`)}
	start := time.Now()
	ProbeBootc(context.Background(), runner)

	require.Len(t, runner.calls, 1)
	assertBoundedTimeout(t, runner.calls[0].ctx, start)
}

func TestProbeRPMOStreePresentOnSuccessWithJSONOutput(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte(`{"deployments":[]}`)}
	s := ProbeRPMOStree(context.Background(), runner)

	assert.True(t, s.Has(RPMOStree))
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "rpm-ostree", runner.calls[0].name)
	assert.Equal(t, []string{"status", "--json"}, runner.calls[0].args)
}

func TestProbeRPMOStreeAbsentOnNonZeroExit(t *testing.T) {
	runner := &fakeCommandRunner{err: errors.New("exit status 1")}
	s := ProbeRPMOStree(context.Background(), runner)

	assert.False(t, s.Has(RPMOStree))
	assert.Empty(t, s.List())
}

func TestProbeRPMOStreeAbsentOnNonJSONOutput(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("State: idle\n")}
	s := ProbeRPMOStree(context.Background(), runner)

	assert.False(t, s.Has(RPMOStree))
	assert.Empty(t, s.List())
}

func TestProbeRPMOStreeAppliesBoundedTimeout(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte(`{"deployments":[]}`)}
	start := time.Now()
	ProbeRPMOStree(context.Background(), runner)

	require.Len(t, runner.calls, 1)
	assertBoundedTimeout(t, runner.calls[0].ctx, start)
}

func TestExecRunnerDoesNotUseAShell(t *testing.T) {
	// A regression guard: ExecRunner must run the named binary directly via
	// exec.CommandContext, never through /bin/sh -c or similar, so shell
	// metacharacters in an argument are never interpreted. "echo hi; echo
	// bye" as a single argv element must be treated as one literal
	// argument to a (nonexistent) command named that whole string, which
	// fails to be found -- proving no shell is involved.
	var runner CommandRunner = ExecRunner{}
	_, err := runner.Run(context.Background(), "echo hi; echo bye")
	assert.Error(t, err)
}
