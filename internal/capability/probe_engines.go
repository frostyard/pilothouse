package capability

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	dockerclient "github.com/moby/moby/client"
)

// podmanAPIPrefix and incusLocalSocket mirror the equivalent constants in
// internal/modules/podman and internal/modules/incus. This package
// deliberately builds its own minimal, read-only clients here rather than
// importing those module packages: both modules' generated views import
// internal/web, and internal/web now imports internal/capability (for
// capability.Set, via platform.Host.Capabilities) -- importing either
// module package from here would form an import cycle
// (capability -> modules/{podman,incus} -> web -> platform -> capability).
const podmanAPIPrefix = "/v5.0.0/libpod"
const incusLocalSocket = "/var/lib/incus/unix.socket"

// engineProbeTimeout bounds every engine reachability probe in this file,
// matching the spec's 5-second figure for command-based probes (see
// probe_systemd.go's dbusProbeTimeout and probe_exec.go's execProbeTimeout
// for the equivalent bound on the other probe kinds). The spec's explicit
// timeout language is scoped to command probes; this applies the same
// bound to engine reachability for consistency (see plan.md's ambiguity
// item 7), so a hung or misbehaving engine socket can never block daemon
// startup.
const engineProbeTimeout = 5 * time.Second

// podmanClient is the subset of a podman API client's shape a reachability
// probe needs: a lightweight version call, plus releasing the client's idle
// connections when done. *podmanProbeClient satisfies it directly.
type podmanClient interface {
	Version(ctx context.Context) (string, error)
	Close()
}

// podmanProbeClient is a minimal, read-only podman API client scoped to
// exactly what ProbePodman needs (a version check over the configured unix
// socket). It intentionally does not reuse internal/modules/podman.APIClient
// (see the import-cycle note above this file's constants).
type podmanProbeClient struct {
	http *http.Client
}

func newPodmanProbeClient(socket string) podmanClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &podmanProbeClient{http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

func (c *podmanProbeClient) Close() { c.http.CloseIdleConnections() }

func (c *podmanProbeClient) Version(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://podman"+podmanAPIPrefix+"/version", nil)
	if err != nil {
		return "", err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", &podmanProbeStatusError{status: response.Status}
	}
	var value struct {
		Version string
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		return "", err
	}
	return value.Version, nil
}

// podmanProbeStatusError reports a non-2xx podman API response; the probe
// only cares that the call failed, so this carries just enough detail to be
// a useful error string.
type podmanProbeStatusError struct{ status string }

func (e *podmanProbeStatusError) Error() string { return "podman API " + e.status }

// ProbePodman probes the podman capability: present iff the configured
// --podman-socket responds to a Version call within engineProbeTimeout.
// newPodmanProbeClient never itself returns an error -- it only builds an
// HTTP client bound to the socket path, performing no I/O -- so every
// failure mode (including an entirely unreachable socket) surfaces at the
// Version call, never at construction, and is never fatal or propagated.
func ProbePodman(ctx context.Context, socket string) Set {
	return probePodman(ctx, socket, func(socket string) podmanClient { return newPodmanProbeClient(socket) })
}

// probePodman is the testable core of ProbePodman: newClient is injected so
// tests can exercise both branches (a fake client whose Version succeeds or
// fails) without a real podman socket.
func probePodman(ctx context.Context, socket string, newClient func(string) podmanClient) Set {
	client := newClient(socket)
	defer client.Close()

	probeCtx, cancel := context.WithTimeout(ctx, engineProbeTimeout)
	defer cancel()

	if _, err := client.Version(probeCtx); err != nil {
		return New()
	}
	return New(Podman)
}

// dockerClient is the subset of *dockerclient.Client a reachability probe
// needs: a ping call, plus releasing the client's transport when done.
type dockerClient interface {
	Ping(ctx context.Context, options dockerclient.PingOptions) (dockerclient.PingResult, error)
	Close() error
}

// ProbeDocker probes the docker capability: present iff a
// dockerclient.FromEnv-constructed client's Ping succeeds within
// engineProbeTimeout. Unlike podman, docker client construction genuinely
// can fail (e.g. a malformed DOCKER_HOST) -- that failure is treated as
// "docker absent," never propagated as fatal, exactly like an unreachable
// socket at Ping time.
func ProbeDocker(ctx context.Context) Set {
	return probeDocker(ctx, newDockerClient)
}

// newDockerClient is the production docker client constructor: it builds a
// client from the environment, the same way cmd/pilothoused already does.
func newDockerClient() (dockerClient, error) {
	return dockerclient.New(dockerclient.FromEnv)
}

// probeDocker is the testable core of ProbeDocker: newClient is injected so
// tests can exercise a client-construction error, a Ping failure, and a
// Ping success without touching a real docker daemon.
func probeDocker(ctx context.Context, newClient func() (dockerClient, error)) Set {
	client, err := newClient()
	if err != nil {
		return New()
	}
	defer func() { _ = client.Close() }()

	probeCtx, cancel := context.WithTimeout(ctx, engineProbeTimeout)
	defer cancel()

	if _, err := client.Ping(probeCtx, dockerclient.PingOptions{}); err != nil {
		return New()
	}
	return New(Docker)
}

// incusClient is the subset of an incus client's shape a reachability probe
// needs: a server-info call. *incusProbeClient satisfies it directly, and
// unlike podman/docker, incus client construction never performs I/O or
// takes a configurable socket path -- the default local socket is fixed,
// per the spec.
type incusClient interface {
	Server(ctx context.Context) (*api.Server, error)
}

// incusProbeClient is a minimal, read-only incus client scoped to exactly
// what ProbeIncus needs (a server-info call over the fixed local socket).
// It intentionally does not reuse internal/modules/incus.LocalClient (see
// the import-cycle note above this file's constants).
type incusProbeClient struct{}

func newIncusProbeClient() incusClient { return &incusProbeClient{} }

func (c *incusProbeClient) Server(ctx context.Context) (*api.Server, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	server, err := incusclient.ConnectIncusUnixWithContext(ctx, incusLocalSocket, &incusclient.ConnectionArgs{
		HTTPClient: httpClient, SkipGetEvents: true, SkipGetServer: true,
	})
	if err != nil {
		return nil, err
	}
	value, _, err := server.GetServer()
	return value, err
}

// ProbeIncus probes the incus capability: present iff the default local
// socket responds to a Server call within engineProbeTimeout.
func ProbeIncus(ctx context.Context) Set {
	return probeIncus(ctx, newIncusProbeClient())
}

// probeIncus is the testable core of ProbeIncus: client is injected so
// tests can exercise both branches (a fake client whose Server call
// succeeds or fails) without a real incus socket.
func probeIncus(ctx context.Context, client incusClient) Set {
	probeCtx, cancel := context.WithTimeout(ctx, engineProbeTimeout)
	defer cancel()

	if _, err := client.Server(probeCtx); err != nil {
		return New()
	}
	return New(Incus)
}
