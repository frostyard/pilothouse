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

func TestProbeHasNoErrorReturnAndCompletesWithoutPanic(t *testing.T) {
	// Probe(ctx, config) returns a single value -- this call itself is the
	// proof there is no error return to check. It also proves Probe
	// completes (doesn't panic or hang) composing every real probe in this
	// package against whatever engines/tools this test host happens to
	// have or lack; it deliberately does not assert which capabilities end
	// up present, since that varies by host -- the aggregation logic
	// itself (empty in/out, partial in/out) is covered in isolation by the
	// unionSets fixture tests above. The podman socket and updex path are
	// pointed at guaranteed-nonexistent paths so at least those two
	// probes' failure branches are deterministically exercised too,
	// regardless of host.
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
