package capability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUnionSetsEmptyWhenEveryProbeFails(t *testing.T) {
	// Mirrors Probe's aggregation with a fixture where every individual
	// probe came back absent (i.e. every probe failed or was
	// unreachable) -- proving the aggregator never turns "every probe
	// absent" into an error, only ever into an empty Set.
	s := unionSets(New(), New(), New(), New(), New(), New(), New(), New(), New())

	assert.Empty(t, s.List())
}

func TestUnionSetsPartialSuccessContainsExactlySucceedingIDs(t *testing.T) {
	// A representative partial fixture, in the same shape (and order) as
	// Probe's own composition: systemd succeeds together with one
	// automatic-update pair sharing its connection, journald succeeds, one
	// engine succeeds; updex/sysext/bootc/rpm-ostree and the other two
	// engines fail/are absent.
	s := unionSets(
		New(Systemd, AutoupdateRPMOStree),
		New(Journald),
		New(), // updex absent
		New(), // sysext absent
		New(), // bootc absent
		New(), // rpm-ostree absent
		New(Podman),
		New(), // docker absent
		New(), // incus absent
	)

	assert.ElementsMatch(t, []ID{Systemd, AutoupdateRPMOStree, Journald, Podman}, s.List())
}

func TestUnionSetsAllPresent(t *testing.T) {
	all := []ID{
		Systemd, Journald, Updex, Sysext, Bootc, RPMOStree,
		AutoupdateRPMOStree, AutoupdateBootc, Podman, Docker, Incus,
	}
	s := unionSets(New(all...))

	assert.ElementsMatch(t, all, s.List())
}

func TestUnionSetsDeduplicatesAcrossSets(t *testing.T) {
	// The same ID reported present by more than one input Set (which
	// never genuinely happens for distinct probes, but the aggregator
	// must not assume that) still appears exactly once.
	s := unionSets(New(Systemd), New(Systemd, Journald))

	assert.Equal(t, []ID{Journald, Systemd}, s.List())
}

// withFakeProbes temporarily swaps the package-level probes table for fake,
// so a test can call the real, exported Probe itself against a
// deterministic fixture, then restores the real table afterward. This is
// what lets the tests below prove Probe's own composition/wiring -- that it
// actually calls every entry in probes and unions their results -- rather
// than only proving unionSets merges pre-built Sets correctly.
func withFakeProbes(t *testing.T, fakes []probeFn) {
	t.Helper()
	original := probes
	probes = fakes
	t.Cleanup(func() { probes = original })
}

func TestProbeAllFailingYieldsEmptySet(t *testing.T) {
	// Every entry in the fake table reports absent, mirroring a host where
	// every real probe (systemd, journald, updex, sysext, bootc,
	// rpm-ostree, podman, docker, incus) is unreachable or absent. Unlike
	// relying on this test host's real state, swapping the table makes the
	// all-absent outcome deterministic regardless of what systemd/docker/
	// incus/etc. this host actually has.
	withFakeProbes(t, []probeFn{
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
		func(context.Context, Config) Set { return New() },
	})

	s := Probe(context.Background(), Config{})

	assert.Empty(t, s.List())
}

func TestProbePartialSuccessContainsExactlySucceedingIDs(t *testing.T) {
	// A representative partial fixture, in the same shape (and order) as
	// the real probes table: systemd succeeds together with one
	// automatic-update pair sharing its connection, journald succeeds, one
	// engine succeeds; updex/sysext/bootc/rpm-ostree and the other two
	// engines fail/are absent. Calling the real Probe (with the table
	// swapped) proves Probe itself wires up and unions every probe's
	// result, not just that a merge helper can combine Sets handed to it
	// directly.
	withFakeProbes(t, []probeFn{
		func(context.Context, Config) Set { return New(Systemd, AutoupdateRPMOStree) },
		func(context.Context, Config) Set { return New(Journald) },
		func(context.Context, Config) Set { return New() }, // updex absent
		func(context.Context, Config) Set { return New() }, // sysext absent
		func(context.Context, Config) Set { return New() }, // bootc absent
		func(context.Context, Config) Set { return New() }, // rpm-ostree absent
		func(context.Context, Config) Set { return New(Podman) },
		func(context.Context, Config) Set { return New() }, // docker absent
		func(context.Context, Config) Set { return New() }, // incus absent
	})

	s := Probe(context.Background(), Config{})

	assert.ElementsMatch(t, []ID{Systemd, AutoupdateRPMOStree, Journald, Podman}, s.List())
}

func TestProbePassesConfigThroughToEachProbe(t *testing.T) {
	// Proves Probe hands its config argument through to every probe in the
	// table (not a zero-value Config), by asserting each fake observes the
	// exact PodmanSocket/Updex values passed to Probe.
	want := Config{PodmanSocket: "/custom/podman.sock", Updex: "/custom/updex"}
	var got []Config
	withFakeProbes(t, []probeFn{
		func(_ context.Context, config Config) Set { got = append(got, config); return New() },
		func(_ context.Context, config Config) Set { got = append(got, config); return New() },
	})

	Probe(context.Background(), want)

	for _, config := range got {
		assert.Equal(t, want, config)
	}
	assert.Len(t, got, 2)
}

func TestProbeHasNoErrorReturnAndCompletesWithoutPanic(t *testing.T) {
	// Probe(ctx, config) returns a single value -- this call itself is the
	// proof there is no error return to check. It also proves Probe
	// completes (doesn't panic or hang) composing every real probe in this
	// package against whatever engines/tools this test host happens to
	// have or lack; it deliberately does not assert which capabilities end
	// up present, since that varies by host -- the deterministic empty/
	// partial fixtures above already cover the aggregation logic itself.
	// The podman socket and updex path are pointed at guaranteed-
	// nonexistent paths so at least those two real probes' failure
	// branches are exercised too, regardless of host.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	config := Config{
		PodmanSocket: filepath.Join(t.TempDir(), "missing-podman.sock"),
		Updex:        filepath.Join(t.TempDir(), "missing-updex"),
	}

	var s Set
	assert.NotPanics(t, func() {
		s = Probe(ctx, config)
	})
	assert.False(t, s.Has(Podman), "the configured podman socket is guaranteed unreachable")
	assert.False(t, s.Has(Updex), "the configured updex path is guaranteed nonexistent")
}
