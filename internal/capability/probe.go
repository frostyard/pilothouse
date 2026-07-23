package capability

import "context"

// Config carries every runtime input Probe needs to run every probe in this
// package, exactly mirroring flags and constructed values cmd/pilothoused
// already has -- no new command-line flag exists, or is needed, for any
// capability probed by this package. Docker and incus need no
// configuration here: both probes reuse the same fixed inputs
// (dockerclient.FromEnv, the default local incus socket) main.go already
// constructs their clients from unconditionally today.
type Config struct {
	// PodmanSocket is the already-configured --podman-socket path.
	PodmanSocket string
	// Updex is the already-configured --updex executable path; empty
	// resolves via PATH lookup, matching that flag's own default and
	// ProbeUpdex's behavior.
	Updex string
}

// probeFn is a single capability probe bound to the run's Config: given the
// run context, it returns the Set of capabilities it found present, never
// erroring. Every real probe in this package (ProbeSystemd, ProbeJournald,
// ProbeUpdex, ProbeSysext, ProbeBootc, ProbeRPMOStree, ProbePodman,
// ProbeDocker, ProbeIncus) is adapted to this shape by probes below.
type probeFn func(ctx context.Context, config Config) Set

// probes is the ordered list of every probe Probe composes. It is a
// package variable -- rather than a literal directly inside Probe -- solely
// so a test can temporarily substitute a fake table (standing in for the
// real, host-dependent probes) and then call the real, exported Probe
// itself. That proves Probe's actual composition/wiring under
// partial-success and all-fail fixtures, deterministically, rather than
// only proving a same-shaped merge helper behaves correctly on fixture
// Sets it never obtained by calling any real probe.
var probes = []probeFn{
	func(ctx context.Context, config Config) Set { return ProbeSystemd(ctx) },
	func(ctx context.Context, config Config) Set { return ProbeJournald() },
	func(ctx context.Context, config Config) Set { return ProbeUpdex(ctx, ExecRunner{}, config.Updex) },
	func(ctx context.Context, config Config) Set { return ProbeSysext(ctx, ExecRunner{}) },
	func(ctx context.Context, config Config) Set { return ProbeBootc(ctx, ExecRunner{}) },
	func(ctx context.Context, config Config) Set { return ProbeRPMOStree(ctx, ExecRunner{}) },
	func(ctx context.Context, config Config) Set { return ProbePodman(ctx, config.PodmanSocket) },
	func(ctx context.Context, config Config) Set { return ProbeDocker(ctx) },
	func(ctx context.Context, config Config) Set { return ProbeIncus(ctx) },
}

// Probe runs every probe in this package -- systemd (plus, sharing its
// connection, the automatic-update pairs), journald, updex, systemd-sysext,
// bootc, rpm-ostree, and the three container engines -- and returns their
// union. It has no error return: every probe it composes is itself
// designed to never fail fatally (see probe_systemd.go, probe_journald.go,
// probe_exec.go, probe_engines.go), narrowing the result on any failure
// instead of erroring, so Probe never fails either -- a host with every
// capability absent or unreachable simply yields an empty Set, never an
// error. This is the single entry point cmd/pilothoused calls once at
// startup; nothing here is cached, so every restart re-probes from
// scratch.
func Probe(ctx context.Context, config Config) Set {
	sets := make([]Set, 0, len(probes))
	for _, p := range probes {
		sets = append(sets, p(ctx, config))
	}
	return unionSets(sets...)
}

// unionSets combines any number of Sets into one, present iff present in at
// least one input. It is the testable core of Probe's aggregation logic,
// isolated from the individual probes themselves so a test can assert the
// aggregator's behavior (empty in, empty out; a representative partial mix
// in, exactly that mix out) using plain fixture Sets, without depending on
// any real systemd/D-Bus/journal/engine state.
func unionSets(sets ...Set) Set {
	var ids []ID
	for _, s := range sets {
		ids = append(ids, s.List()...)
	}
	return New(ids...)
}
