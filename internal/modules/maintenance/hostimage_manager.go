package maintenance

import (
	"context"
)

// bootcCommand and rpmOStreeCommand name the only two executables this
// package ever runs, and statusSubcommand/jsonFlag the only arguments it ever
// passes them: `bootc status --json` and `rpm-ostree status --json`, both
// read-only. Every argument is a separate argv element handed to the injected
// Runner -- never a command string, never a shell -- and no other subcommand
// is reachable from here, least of all a lifecycle one. HostImageManager runs
// each command at most once per Status call and runs nothing else.
const (
	bootcCommand     = "bootc"
	jsonFlag         = "--json"
	rpmOStreeCommand = "rpm-ostree"
	statusSubcommand = "status"
)

// HostImageSource is the read-only host-image seam: everything that needs the
// host's image picture -- today only cmd/pilothoused's registerHostImage,
// which serves broker.QueryHostImageStatus from it -- depends on this
// interface rather than on the concrete manager, exactly as the rest of the
// daemon depends on maintenance.Manager. It exposes no mutation of any kind,
// by construction.
type HostImageSource interface {
	Status(context.Context) (HostImageStatus, error)
}

// HostImageManager reports the host's image picture by running
// `bootc status --json` and `rpm-ostree status --json` through an injected
// Runner and parsing their output with this package's pure parsers.
//
// bootcAvailable and rpmOstreeAvailable are the probed capability facts
// (capability.Bootc / capability.RPMOStree) threaded in from
// cmd/pilothoused's startup probe. A source whose flag is false is never
// executed at all -- not attempted and failed, simply not attempted -- so a
// host without rpm-ostree spends no time on it and reports no error about it.
type HostImageManager struct {
	bootcAvailable     bool
	rpmOstreeAvailable bool
	runner             Runner
}

// NewHostImageManager constructs a host-image reporter. It never runs
// anything at construction time: both commands are deferred to Status, so a
// manager may be built regardless of which sources the host has.
func NewHostImageManager(runner Runner, bootcAvailable, rpmOstreeAvailable bool) *HostImageManager {
	return &HostImageManager{bootcAvailable: bootcAvailable, rpmOstreeAvailable: rpmOstreeAvailable, runner: runner}
}

// Status reports the raw host-image facts: the booted, staged, and rollback
// deployments bootc identifies, rpm-ostree's supplementary version/checksum
// detail for those same deployments, soft-reboot eligibility when bootc
// exposes it, and one availability/error pair per source. It deliberately
// computes no reboot-required posture -- that stays SystemManager.State's job
// -- and exposes no lifecycle mutation.
//
// Failure handling is per-source and symmetric. A source that is available
// but whose command fails to run, or whose output fails to parse, sets its
// own Available field to false and its own Error field to the failure's
// message; the other source is unaffected and its data is still reported.
// Neither failure mode is propagated as Status's own error: a host where
// bootc answers and rpm-ostree does not (or the reverse) still has a usable,
// honest report, and collapsing that into a method-level error would throw it
// away. Status therefore never returns a non-nil error today -- the error
// result exists for conditions outside per-source reporting, of which this
// design has none -- and callers must still check it rather than assume.
//
// A source whose availability flag is false leaves its Available field false
// and its Error field empty: "never attempted" and "attempted and failed" are
// different facts and stay distinguishable.
func (m *HostImageManager) Status(ctx context.Context) (HostImageStatus, error) {
	var bootc HostImageStatus
	if m.bootcAvailable {
		parsed, err := m.bootcStatus(ctx)
		if err != nil {
			// Leave BootcAvailable false: bootc is present on the host but
			// its answer is unusable, so there is no bootc-sourced data to
			// report, only a reason why.
			bootc = HostImageStatus{BootcError: err.Error()}
		} else {
			bootc = parsed
		}
	}
	var supplement rpmOStreeSupplement
	var rpmOStreeAvailable bool
	var rpmOStreeError string
	if m.rpmOstreeAvailable {
		parsed, err := m.rpmOStreeStatus(ctx)
		if err != nil {
			rpmOStreeError = err.Error()
		} else {
			supplement = parsed
			rpmOStreeAvailable = true
		}
	}
	// MergeHostImage carries bootc's availability/error through and always
	// returns the rpm-ostree pair zeroed (it cannot know whether rpm-ostree
	// ran), so this caller -- the one that actually ran the command -- owns
	// setting them, after the merge.
	status := MergeHostImage(bootc, supplement)
	status.RPMOStreeAvailable = rpmOStreeAvailable
	status.RPMOStreeError = rpmOStreeError
	return status, nil
}

// bootcStatus runs `bootc status --json` once and parses it. It is the only
// place in the tree that invokes bootc.
func (m *HostImageManager) bootcStatus(ctx context.Context) (HostImageStatus, error) {
	output, err := m.runner.Run(ctx, bootcCommand, statusSubcommand, jsonFlag)
	if err != nil {
		return HostImageStatus{}, err
	}
	return ParseBootcStatus(output)
}

// rpmOStreeStatus runs `rpm-ostree status --json` once and parses it. It is
// the only place in the tree that invokes rpm-ostree.
func (m *HostImageManager) rpmOStreeStatus(ctx context.Context) (rpmOStreeSupplement, error) {
	output, err := m.runner.Run(ctx, rpmOStreeCommand, statusSubcommand, jsonFlag)
	if err != nil {
		return rpmOStreeSupplement{}, err
	}
	return ParseRPMOStreeStatus(output)
}
