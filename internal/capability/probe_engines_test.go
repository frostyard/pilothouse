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
	s := probeDocker(context.Background(), func() (dockerClient, error) { return fake, nil })

	assert.True(t, s.Has(Docker))
	assert.ElementsMatch(t, []ID{Docker}, s.List())
	assert.True(t, fake.closed, "the client must be closed once the probe is done with it")
}

func TestProbeDockerAbsentOnPingError(t *testing.T) {
	fake := &fakeDockerClient{err: errors.New("failed to connect to the docker API")}
	s := probeDocker(context.Background(), func() (dockerClient, error) { return fake, nil })

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
	assert.True(t, fake.closed, "the client must be closed even when the probe fails")
}

func TestProbeDockerAbsentOnClientConstructionError(t *testing.T) {
	s := probeDocker(context.Background(), func() (dockerClient, error) {
		return nil, errors.New("unable to parse docker host")
	})

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

func TestProbeDockerAppliesBoundedTimeout(t *testing.T) {
	fake := &fakeDockerClient{}
	start := time.Now()
	probeDocker(context.Background(), func() (dockerClient, error) { return fake, nil })

	require.NotNil(t, fake.ctx)
	assertBoundedEngineTimeout(t, fake.ctx, start)
}

func TestProbeDockerAbsentOnRealClientConstructionError(t *testing.T) {
	// Real ProbeDocker (no fake): a malformed DOCKER_HOST fails at
	// dockerclient.New itself, before any Ping is attempted -- proving the
	// construction-error branch is reachable through the real production
	// path, not only through an injected fake.
	t.Setenv("DOCKER_HOST", "not a valid host")
	s := ProbeDocker(context.Background())

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

func TestProbeDockerAbsentOnRealUnreachableSocket(t *testing.T) {
	// Real ProbeDocker against a DOCKER_HOST unix socket path that is
	// guaranteed never to exist, independent of whatever docker daemon (if
	// any) this test host actually has.
	socket := filepath.Join(t.TempDir(), "missing-docker.sock")
	t.Setenv("DOCKER_HOST", "unix://"+socket)
	s := ProbeDocker(context.Background())

	assert.False(t, s.Has(Docker))
	assert.Empty(t, s.List())
}

// --- incus ---

// fakeIncusClient implements incusClient end-to-end with no real socket
// involved. incus.NewLocalClient's default socket path is fixed (not
// configurable), and this test host may or may not have a real incus
// socket, so the incus probe is exercised entirely through fakes -- both
// branches are still the full success/failure path, since probeIncus never
// does anything with a successful *api.Server response beyond checking the
// error.
type fakeIncusClient struct {
	server *api.Server
	err    error
	ctx    context.Context
}

func (f *fakeIncusClient) Server(ctx context.Context) (*api.Server, error) {
	f.ctx = ctx
	return f.server, f.err
}

func TestProbeIncusPresentOnSuccess(t *testing.T) {
	fake := &fakeIncusClient{server: &api.Server{}}
	s := probeIncus(context.Background(), fake)

	assert.True(t, s.Has(Incus))
	assert.ElementsMatch(t, []ID{Incus}, s.List())
}

func TestProbeIncusAbsentOnServerError(t *testing.T) {
	fake := &fakeIncusClient{err: errors.New("dial unix /var/lib/incus/unix.socket: connect: no such file or directory")}
	s := probeIncus(context.Background(), fake)

	assert.False(t, s.Has(Incus))
	assert.Empty(t, s.List())
}

func TestProbeIncusAppliesBoundedTimeout(t *testing.T) {
	fake := &fakeIncusClient{server: &api.Server{}}
	start := time.Now()
	probeIncus(context.Background(), fake)

	require.NotNil(t, fake.ctx)
	assertBoundedEngineTimeout(t, fake.ctx, start)
}
