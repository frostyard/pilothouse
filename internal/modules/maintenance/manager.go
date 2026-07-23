package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type UpdateSource interface {
	Check(context.Context) ([]sysext.AvailableUpdate, error)
	List(context.Context) ([]sysext.Feature, error)
}

type JobSource interface {
	List(context.Context, jobs.Filter) ([]jobs.Job, error)
	RebootRequiredSince(context.Context, time.Time) (bool, error)
}

// State is the aggregated maintenance posture: recent jobs, the OS version,
// available extension updates, and — the part this package alone owns — the
// reboot-required posture with its structured reasons.
//
// SoftRebootCapable is the exception to that ownership: it is not computed
// here at all, only copied verbatim from the host-image source's
// HostImageStatus.SoftRebootCapable (see hostImageState). It is three-state —
// non-nil true/false when the host's bootc reports soft-reboot eligibility,
// nil when it does not report it (an older bootc, a host without bootc, or a
// bootc that failed to answer) — and it is purely informational: it never
// makes RebootRequired true on its own, and Pilothouse still exposes only the
// existing full reboot action.
type State struct {
	Jobs              []Job                    `json:"jobs"`
	OSVersion         string                   `json:"os_version"`
	RebootReasons     []string                 `json:"reboot_reasons"`
	RebootRequired    bool                     `json:"reboot_required"`
	SoftRebootCapable *bool                    `json:"soft_reboot_capable,omitempty"`
	Updates           []sysext.AvailableUpdate `json:"updates"`
}

// stagedHostImageReason is the reboot reason a staged host-image deployment
// contributes. Assembling it here — inside the one type that owns
// reboot-required posture — is deliberate: QueryHostImageStatus reports only
// the raw fact that a staged deployment exists, and this is the single place
// in the tree where that fact becomes a reboot reason.
const stagedHostImageReason = "A staged host image deployment requires activation by reboot."

type Job struct {
	Action         string `json:"action"`
	ErrorCategory  string `json:"error_category,omitempty"`
	ID             uint64 `json:"id"`
	RebootRequired bool   `json:"reboot_required"`
	Resource       string `json:"resource"`
	Status         string `json:"status"`
}

type Manager interface {
	Reboot(context.Context) error
	State(context.Context) (State, error)
}

type SystemManager struct {
	bootcAvailable  bool
	cacheFeatures   []sysext.Feature
	cacheUpdates    []sysext.AvailableUpdate
	cacheAt         time.Time
	hostImage       HostImageSource
	jobs            JobSource
	mu              sync.Mutex
	now             func() time.Time
	root            string
	runner          Runner
	sysextAvailable bool
	updates         UpdateSource
	updexAvailable  bool
}

// NewSystemManager constructs a maintenance manager.
//
// updexAvailable, sysextAvailable, and bootcAvailable report whether the updex
// executable, systemd-sysext, and bootc are present on this host (as probed by
// internal/capability). All three follow one convention: a source whose flag is
// false is never called at all, and State degrades by omitting that source's
// contribution rather than returning an error. They are independent of the
// systemd capability that gates registerMaintenance's registration entirely.
//
// hostImage is the read-only host-image seam (the same HostImageManager
// cmd/pilothoused serves QueryHostImageStatus from). State consults it only
// when bootcAvailable is true, and only to read two facts: whether a staged
// deployment exists (a reboot-required reason) and the host's soft-reboot
// eligibility (informational). It is never used to mutate anything — the
// interface has no mutating method — and may be nil, which State treats
// exactly like bootcAvailable being false.
func NewSystemManager(updates UpdateSource, jobSource JobSource, hostImage HostImageSource, runner Runner, root string, updexAvailable, sysextAvailable, bootcAvailable bool) *SystemManager {
	if root == "" {
		root = "/"
	}
	return &SystemManager{bootcAvailable: bootcAvailable, hostImage: hostImage, jobs: jobSource, now: time.Now, root: root, runner: runner, sysextAvailable: sysextAvailable, updates: updates, updexAvailable: updexAvailable}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	available, features, err := m.extensionState(ctx)
	if err != nil {
		return State{}, err
	}
	jobRecords, err := m.jobs.List(ctx, jobs.Filter{Limit: 20})
	if err != nil {
		return State{}, fmt.Errorf("list maintenance jobs: %w", err)
	}
	// Normalize slice fields to non-nil so they serialize as JSON `[]` rather
	// than `null` over the broker protocol. Downstream JSON consumers should
	// not have to special-case null vs empty array (the same contract updex's
	// feature output now follows).
	if available == nil {
		available = []sysext.AvailableUpdate{}
	}
	state := State{Jobs: make([]Job, 0, len(jobRecords)), OSVersion: m.osVersion(), Updates: available, RebootReasons: []string{}}
	for _, job := range jobRecords {
		state.Jobs = append(state.Jobs, Job{ID: job.ID, Action: job.Action, Resource: job.Resource, Status: job.Status, ErrorCategory: job.ErrorCategory, RebootRequired: job.RebootRequired})
	}
	if _, err := os.Stat(m.path("run/reboot-required")); err == nil {
		state.RebootReasons = append(state.RebootReasons, "The operating system requested a reboot.")
	} else if !os.IsNotExist(err) {
		return State{}, fmt.Errorf("inspect reboot marker: %w", err)
	}
	// One host-image read per State call feeds two independent results: a
	// staged deployment is a reboot reason alongside the marker file and the
	// completed-job signal below, while soft-reboot eligibility is copied
	// through untouched and never affects RebootRequired.
	staged, softRebootCapable := m.hostImageState(ctx)
	state.SoftRebootCapable = softRebootCapable
	if staged {
		state.RebootReasons = append(state.RebootReasons, stagedHostImageReason)
	}
	for _, feature := range features {
		if feature.Merged && !feature.Enabled {
			state.RebootReasons = append(state.RebootReasons, feature.Name+" is disabled but remains active until reboot.")
		}
	}
	bootedAt, err := m.bootedAt()
	if err != nil {
		return State{}, err
	}
	rebootRequired, err := m.jobs.RebootRequiredSince(ctx, bootedAt)
	if err != nil {
		return State{}, fmt.Errorf("inspect completed maintenance jobs: %w", err)
	}
	if rebootRequired {
		state.RebootReasons = append(state.RebootReasons, "A completed extension update requires activation by reboot.")
	}
	state.RebootRequired = len(state.RebootReasons) > 0
	return state, nil
}

// extensionState reads updex/sysext-derived data, degrading gracefully when
// either dependency is absent rather than returning an error:
//
//   - updex and sysext both present: unchanged behavior -- Check() populates
//     Updates and List() populates Features (which drives merged-but-disabled
//     reboot reasons in State).
//   - updex present, sysext absent: Check() still runs (it never touches
//     systemd-sysext) so Updates still populates; List()'s installed/merged
//     status is meaningless without systemd-sysext, so it is skipped entirely
//     and Features is omitted.
//   - updex absent (sysext present or absent): neither Check() nor List() can
//     enumerate feature definitions without updex, so both are skipped and
//     Updates/Features are both omitted. This is a known limitation of
//     today's sysext.SystemManager (enumeration is updex-only), not an error.
//
// In no combination does extensionState return an error because of a missing
// updex/sysext capability.
func (m *SystemManager) extensionState(ctx context.Context) ([]sysext.AvailableUpdate, []sysext.Feature, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.updexAvailable {
		return nil, nil, nil
	}
	if !m.cacheAt.IsZero() && m.now().Sub(m.cacheAt) < time.Minute {
		return slices.Clone(m.cacheUpdates), slices.Clone(m.cacheFeatures), nil
	}
	available, err := m.updates.Check(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("check extension updates: %w", err)
	}
	var features []sysext.Feature
	if m.sysextAvailable {
		features, err = m.updates.List(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect extension state: %w", err)
		}
	}
	m.cacheUpdates = slices.Clone(available)
	m.cacheFeatures = slices.Clone(features)
	m.cacheAt = m.now()
	return available, features, nil
}

// hostImageState reads the host-image source at most once per State call and
// returns the two independent facts State takes from it: whether a staged
// deployment exists, and the host's soft-reboot eligibility copied verbatim.
//
// It follows exactly the degrade convention extensionState established for
// updex/sysext — an absent source is skipped, never errored:
//
//   - bootcAvailable false (or no source injected): Status is never invoked at
//     all. Not attempted and failed, simply not attempted. No staged reason is
//     added and SoftRebootCapable stays nil, whatever the source would have
//     returned had it been asked.
//   - bootcAvailable true and Status fails: the failure contributes nothing to
//     this call — no staged reason, and SoftRebootCapable nil — and is not
//     propagated. Source availability and per-source errors are
//     QueryHostImageStatus's to report (HostImageStatus carries a
//     BootcAvailable/BootcError pair for exactly that); the aggregate
//     maintenance posture must stay answerable when one of its inputs cannot
//     be read, the same way a missing updex omits Updates rather than failing
//     State.
//   - bootcAvailable true and Status succeeds: a non-nil Staged deployment
//     means a staged host image is waiting for activation, and
//     SoftRebootCapable is copied through byte-for-byte — including nil, which
//     means "this bootc does not report eligibility" and must never be
//     synthesized into an explicit false.
//
// The two results are deliberately independent. Soft-reboot eligibility is
// informational: it is reported whether or not anything is staged, and it
// never by itself makes a reboot required.
func (m *SystemManager) hostImageState(ctx context.Context) (bool, *bool) {
	if !m.bootcAvailable || m.hostImage == nil {
		return false, nil
	}
	status, err := m.hostImage.Status(ctx)
	if err != nil {
		return false, nil
	}
	return status.Staged != nil, status.SoftRebootCapable
}

func (m *SystemManager) Reboot(ctx context.Context) error {
	_, err := m.runner.Run(ctx, "systemctl", "reboot", "--no-wall", "--no-block")
	return err
}

func (m *SystemManager) bootedAt() (time.Time, error) {
	value, err := os.ReadFile(m.path("proc/uptime"))
	if err != nil {
		return time.Time{}, fmt.Errorf("read system uptime: %w", err)
	}
	fields := strings.Fields(string(value))
	if len(fields) == 0 {
		return time.Time{}, fmt.Errorf("parse system uptime: missing value")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse system uptime: %w", err)
	}
	return m.now().Add(-time.Duration(seconds * float64(time.Second))), nil
}

func (m *SystemManager) osVersion() string {
	value, err := os.ReadFile(m.path("etc/os-release"))
	if err != nil {
		return "Linux"
	}
	var pretty, image string
	for _, line := range strings.Split(string(value), "\n") {
		if parsed, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			pretty = strings.Trim(parsed, `"`)
		}
		if parsed, ok := strings.CutPrefix(line, "IMAGE_VERSION="); ok {
			image = strings.Trim(parsed, `"`)
		}
	}
	if image != "" {
		return pretty + " · image " + image
	}
	if pretty != "" {
		return pretty
	}
	return "Linux"
}

func (m *SystemManager) path(path string) string {
	return filepath.Join(m.root, path)
}
