package capability

import (
	"context"
	"time"

	"github.com/frostyard/pilothouse/internal/modules/incus"
	"github.com/frostyard/pilothouse/internal/modules/podman"
	"github.com/lxc/incus/v7/shared/api"
	dockerclient "github.com/moby/moby/client"
)

// engineProbeTimeout bounds every engine reachability probe in this file,
// matching the spec's 5-second figure for command-based probes (see
// probe_systemd.go's dbusProbeTimeout and probe_exec.go's execProbeTimeout
// for the equivalent bound on the other probe kinds). The spec's explicit
// timeout language is scoped to command probes; this applies the same
// bound to engine reachability for consistency (see plan.md's ambiguity
// item 7), so a hung or misbehaving engine socket can never block daemon
// startup.
const engineProbeTimeout = 5 * time.Second

// podmanClient is the subset of podman.Client's shape a reachability probe
// needs: a lightweight version call, plus releasing the client's idle
// connections when done. *podman.APIClient satisfies it directly.
type podmanClient interface {
	Version(ctx context.Context) (string, error)
	Close()
}

// ProbePodman probes the podman capability: present iff the configured
// --podman-socket responds to a Version call within engineProbeTimeout.
// podman.NewAPIClient never itself returns an error -- it only builds an
// HTTP client bound to the socket path, performing no I/O -- so every
// failure mode (including an entirely unreachable socket) surfaces at the
// Version call, never at construction, and is never fatal or propagated.
func ProbePodman(ctx context.Context, socket string) Set {
	return probePodman(ctx, socket, func(socket string) podmanClient { return podman.NewAPIClient(socket) })
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

// incusClient is the subset of incus.Client's shape a reachability probe
// needs: a server-info call. *incus.LocalClient satisfies it directly, and
// unlike podman/docker, incus client construction (incus.NewLocalClient)
// never performs I/O or takes a configurable socket path -- the default
// local socket is fixed, per the spec.
type incusClient interface {
	Server(ctx context.Context) (*api.Server, error)
}

// ProbeIncus probes the incus capability: present iff the default local
// socket responds to a Server call within engineProbeTimeout.
func ProbeIncus(ctx context.Context) Set {
	return probeIncus(ctx, incus.NewLocalClient())
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
