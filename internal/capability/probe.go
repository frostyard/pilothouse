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
	return unionSets(
		ProbeSystemd(ctx),
		ProbeJournald(),
		ProbeUpdex(ctx, ExecRunner{}, config.Updex),
		ProbeSysext(ctx, ExecRunner{}),
		ProbeBootc(ctx, ExecRunner{}),
		ProbeRPMOStree(ctx, ExecRunner{}),
		ProbePodman(ctx, config.PodmanSocket),
		ProbeDocker(ctx),
		ProbeIncus(ctx),
	)
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
