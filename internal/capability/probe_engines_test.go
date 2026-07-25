package capability

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
	dockerclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertBoundedEngineTimeout asserts ctx carries a deadline no later than
// engineProbeTimeout from start, and that some positive budget remains --
// i.e. the probe applied its own bounded timeout to the context it handed
// the client, rather than passing the caller's (possibly undeadlined)
// context straight through. Mirrors probe_exec_test.go's
// assertBoundedTimeout, scoped to engineProbeTimeout instead of
// execProbeTimeout.
func assertBoundedEngineTimeout(t *testing.T, ctx context.Context, start time.Time) {
	t.Helper()
	const slack = 500 * time.Millisecond
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "probe must attach a deadline to the context passed to the client")
	assert.LessOrEqual(t, deadline.Sub(start), engineProbeTimeout+slack, "deadline must not exceed the spec's 5-second figure")
	assert.Greater(t, time.Until(deadline), time.Duration(0), "deadline must still be in the future")
}

// --- podman ---

// fakePodmanClient implements podmanClient end-to-end with no real socket
// involved: Version returns a canned result, and Close is observable, so a
// test can prove both the success and failure branches of probePodman
// without podman.NewAPIClient or any real network I/O.
type fakePodmanClient struct {
	version string
	err     error
	ctx     context.Context
	closed  bool
}

func (f *fakePodmanClient) Version(ctx context.Context) (string, error) {
	f.ctx = ctx
	return f.version, f.err
}

func (f *fakePodmanClient) Close() { f.closed = true }

func TestProbePodmanPresentOnSuccess(t *testing.T) {
	fake := &fakePodmanClient{version: "5.0.0"}
	s := probePodman(context.Background(), "/run/podman/podman.sock", func(string) podmanClient { return fake })

	assert.True(t, s.Has(Podman))
	assert.ElementsMatch(t, []ID{Podman}, s.List())
	assert.True(t, fake.closed, "the client must be closed once the probe is done with it")
}

func TestProbePodmanAbsentOnVersionError(t *testing.T) {
	fake := &fakePodmanClient{err: errors.New("dial unix /run/podman/podman.sock: connect: connection refused")}
	s := probePodman(context.Background(), "/run/podman/podman.sock", func(string) podmanClient { return fake })

	assert.False(t, s.Has(Podman))
	assert.Empty(t, s.List())
	assert.True(t, fake.closed, "the client must be closed even when the probe fails")
}

func TestProbePodmanUsesConfiguredSocket(t *testing.T) {
	var gotSocket string
	fake := &fakePodmanClient{version: "5.0.0"}
	probePodman(context.Background(), "/custom/podman.sock", func(socket string) podmanClient {
		gotSocket = socket
		return fake
	})

	assert.Equal(t, "/custom/podman.sock", gotSocket)
}

func TestProbePodmanAppliesBoundedTimeout(t *testing.T) {
	fake := &fakePodmanClient{version: "5.0.0"}
	start := time.Now()
	probePodman(context.Background(), "/run/podman/podman.sock", func(string) podmanClient { return fake })

	require.NotNil(t, fake.ctx)
	assertBoundedEngineTimeout(t, fake.ctx, start)
}

func TestProbePodmanAbsentAndNeverConstructsClientWhenUnconfigured(t *testing.T) {
	// The --podman-socket flag defaults to empty. An unconfigured podman
	// must be reported absent *without* the probe building a client or
	// dialling: a host that merely happens to have a socket at some
	// well-known path must not enable the engine. The injected constructor
	// is wired to return a client whose Version succeeds, so this test
	// cannot pass merely because a dial was attempted and failed -- the
	// only way the Set stays empty is if the constructor is never reached.
	constructed := false
	fake := &fakePodmanClient{version: "5.0.0"}
	s := probePodman(context.Background(), "", func(string) podmanClient {
		constructed = true
		return fake
	})

	assert.False(t, constructed, "an unconfigured podman socket must never construct a probe client")
	assert.False(t, s.Has(Podman))
	assert.Empty(t, s.List())
}

func TestProbePodmanExportedAbsentWhenUnconfigured(t *testing.T) {
	// The same guard through the exported production entry point, with no
	// injection at all.
	s := ProbePodman(context.Background(), "")

	assert.False(t, s.Has(Podman))
	assert.Empty(t, s.List())
}

func TestProbePodmanUnconfiguredKeepsPodmanOutOfComposedProbe(t *testing.T) {
	// The guard through the production composition path: Probe's entry in
	// probes passes Config.PodmanSocket straight to ProbePodman, so an
	// empty Config.PodmanSocket must leave Podman out of the composed Set.
	s := Probe(context.Background(), Config{})

	assert.False(t, s.Has(Podman))
}

func TestProbePodmanAbsentOnUnreachableSocket(t *testing.T) {
	// Real ProbePodman (no fake) against a socket path that is guaranteed
	// never to exist: podman.NewAPIClient never errors at construction, so
	// this exercises the true unreachable-socket failure mode at the
	// Version call itself.
	socket := filepath.Join(t.TempDir(), "missing-podman.sock")
	s := ProbePodman(context.Background(), socket)

	assert.False(t, s.Has(Podman))
	assert.Empty(t, s.List())
}

// --- docker ---

// fakeDockerClient implements dockerClient end-to-end with no real socket
// involved.
type fakeDockerClient struct {
	result dockerclient.PingResult
	err    error
	ctx    context.Context
	closed bool
}

func (f *fakeDockerClient) Ping(ctx context.Context, _ dockerclient.PingOptions) (dockerclient.PingResult, error) {
	f.ctx = ctx
	return f.result, f.err
}

func (f *fakeDockerClient) Close() error {
	f.closed = true
	return nil
}

func TestProbeDockerPresentOnSuccess(t *testing.T) {
	fake := &fakeDockerClient{}
	s := probeDocker(context.Background(), "unix:///var/run/docker.sock", func(string) (dockerClient, error) { return fake, nil })

	assert.True(t, s.Has(Docker))
	assert.ElementsMatch(t, []ID{Docker}, s.List())
	assert.True(t, fake.closed, "the client must be closed once the probe is done with it")
}

func TestProbeDockerAbsentOnPingError(t *testing.T) {
	fake := &fakeDockerClient{err: errors.New("failed to connect to the docker API")}
	s := probeDocker(context.Background(), "unix:///var/run/docker.sock", func(string) (dockerClient, error) { return fake, nil })

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
	assert.True(t, fake.closed, "the client must be closed even when the probe fails")
}

func TestProbeDockerAbsentOnClientConstructionError(t *testing.T) {
	s := probeDocker(context.Background(), "unix:///var/run/docker.sock", func(string) (dockerClient, error) {
		return nil, errors.New("unable to parse docker host")
	})

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

func TestProbeDockerUsesConfiguredEndpoint(t *testing.T) {
	// The configured endpoint -- not the ambient environment -- is what the
	// client is constructed from, so DOCKER_HOST is set to a different value
	// here to prove the constructor receives the flag's value verbatim.
	t.Setenv("DOCKER_HOST", "unix:///env/should/be/ignored.sock")
	var gotEndpoint string
	fake := &fakeDockerClient{}
	probeDocker(context.Background(), "unix:///custom/docker.sock", func(endpoint string) (dockerClient, error) {
		gotEndpoint = endpoint
		return fake, nil
	})

	assert.Equal(t, "unix:///custom/docker.sock", gotEndpoint)
}

func TestProbeDockerAppliesBoundedTimeout(t *testing.T) {
	fake := &fakeDockerClient{}
	start := time.Now()
	probeDocker(context.Background(), "unix:///var/run/docker.sock", func(string) (dockerClient, error) { return fake, nil })

	require.NotNil(t, fake.ctx)
	assertBoundedEngineTimeout(t, fake.ctx, start)
}

func TestProbeDockerAbsentAndNeverConstructsClientWhenUnconfigured(t *testing.T) {
	// The --docker flag defaults to empty. An unconfigured docker must be
	// reported absent *without* the probe building a client or dialling --
	// and that must hold even when the process environment carries a
	// perfectly usable DOCKER_HOST, proving the capability no longer follows
	// the SDK's implicit environment resolution. The injected constructor is
	// wired to return a client whose Ping succeeds, so this test cannot pass
	// merely because a dial was attempted and failed: the only way the Set
	// stays empty is if the constructor is never reached at all.
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	constructed := false
	fake := &fakeDockerClient{}
	s := probeDocker(context.Background(), "", func(string) (dockerClient, error) {
		constructed = true
		return fake, nil
	})

	assert.False(t, constructed, "an unconfigured docker endpoint must never construct a probe client")
	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

func TestProbeDockerExportedAbsentWhenUnconfigured(t *testing.T) {
	// The same guard through the exported production entry point, with no
	// injection at all, and with DOCKER_HOST set in the test process's
	// environment.
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	s := ProbeDocker(context.Background(), "")

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

func TestProbeDockerUnconfiguredKeepsDockerOutOfComposedProbe(t *testing.T) {
	// The guard through the production composition path: Probe's entry in
	// probes passes Config.DockerEndpoint straight to ProbeDocker, so an
	// empty Config.DockerEndpoint must leave Docker out of the composed Set
	// even with DOCKER_HOST exported.
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	s := Probe(context.Background(), Config{})

	assert.False(t, s.Has(Docker))
}

func TestProbeDockerAbsentOnRealClientConstructionError(t *testing.T) {
	// Real ProbeDocker (no fake): a malformed endpoint fails at
	// dockerclient.New itself, before any Ping is attempted -- proving the
	// endpoint-driven construction-error branch is reachable through the real
	// production path, not only through an injected fake. DOCKER_HOST is set
	// to a valid-looking value that would *succeed* construction, so this can
	// only fail because the flag's endpoint (never the environment) is what
	// the client is built from.
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	s := ProbeDocker(context.Background(), "not a valid host")

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

func TestProbeDockerAbsentOnRealUnreachableSocket(t *testing.T) {
	// Real ProbeDocker against a configured unix socket path that is
	// guaranteed never to exist, independent of whatever docker daemon (if
	// any) this test host actually has: construction succeeds from the
	// endpoint and the failure surfaces at the Ping call.
	socket := filepath.Join(t.TempDir(), "missing-docker.sock")
	s := ProbeDocker(context.Background(), "unix://"+socket)

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

// --- incus ---

// fakeIncusClient implements incusClient end-to-end with no real socket
// involved. incus.NewLocalClient's default socket path is fixed (not
// configurable -- the --incus flag gates whether it is probed, never where),
// and this test host may or may not have a real incus socket, so the
// enabled probe's success and failure branches are exercised entirely
// through fakes -- both are still the full path, since probeIncus never does
// anything with a successful *api.Server response beyond checking the error.
// It counts Server calls so the disabled branch can assert the socket is
// never contacted at all, rather than only that the resulting Set is empty.
type fakeIncusClient struct {
	server *api.Server
	err    error
	ctx    context.Context
	calls  int
}

func (f *fakeIncusClient) Server(ctx context.Context) (*api.Server, error) {
	f.calls++
	f.ctx = ctx
	return f.server, f.err
}

func TestProbeIncusPresentOnSuccess(t *testing.T) {
	fake := &fakeIncusClient{server: &api.Server{}}
	s := probeIncus(context.Background(), true, fake)

	assert.True(t, s.Has(Incus))
	assert.ElementsMatch(t, []ID{Incus}, s.List())
	assert.Equal(t, 1, fake.calls)
}

func TestProbeIncusAbsentOnServerError(t *testing.T) {
	fake := &fakeIncusClient{err: errors.New("dial unix /var/lib/incus/unix.socket: connect: no such file or directory")}
	s := probeIncus(context.Background(), true, fake)

	assert.False(t, s.Has(Incus))
	assert.Empty(t, s.List())
	assert.Equal(t, 1, fake.calls)
}

func TestProbeIncusAppliesBoundedTimeout(t *testing.T) {
	fake := &fakeIncusClient{server: &api.Server{}}
	start := time.Now()
	probeIncus(context.Background(), true, fake)

	require.NotNil(t, fake.ctx)
	assertBoundedEngineTimeout(t, fake.ctx, start)
}

func TestProbeIncusAbsentAndNeverCallsClientWhenDisabled(t *testing.T) {
	// The --incus flag defaults to false. A not-opted-in incus must be
	// reported absent *without* the probe contacting the local socket at
	// all. The injected fake is wired to succeed, so this test cannot pass
	// merely because a dial was attempted and failed: the only way the Set
	// stays empty is if Server is never reached -- asserted directly as a
	// zero call count, so mere socket reachability can no longer enable the
	// capability without the flag.
	fake := &fakeIncusClient{server: &api.Server{}}
	s := probeIncus(context.Background(), false, fake)

	assert.Equal(t, 0, fake.calls, "a disabled incus must never invoke the client's Server call")
	assert.Nil(t, fake.ctx)
	assert.False(t, s.Has(Incus))
	assert.Empty(t, s.List())
}

func TestProbeIncusExportedAbsentWhenDisabled(t *testing.T) {
	// The same guard through the exported production entry point, with no
	// injection at all: whatever this test host's /var/lib/incus/unix.socket
	// answers (or does not answer), a false flag keeps incus absent.
	s := ProbeIncus(context.Background(), false)

	assert.False(t, s.Has(Incus))
	assert.Empty(t, s.List())
}

func TestProbeIncusDisabledKeepsIncusOutOfComposedProbe(t *testing.T) {
	// The guard through the production composition path: Probe's entry in
	// probes passes Config.IncusEnabled straight to ProbeIncus, so a
	// zero-value Config (the flag left at its false default) must leave
	// Incus out of the composed Set on every host, including one with a
	// live incus socket.
	s := Probe(context.Background(), Config{})

	assert.False(t, s.Has(Incus))
}
