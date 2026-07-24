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

type JobSource interface {
	List(context.Context, jobs.Filter) ([]jobs.Job, error)
	RebootRequiredSince(context.Context, time.Time) (bool, error)
}

// State is the aggregated maintenance posture: recent jobs, the OS version,
// and — the part this package alone owns — the reboot-required posture with
// its structured reasons.
//
// Extension inventory and extension update availability are deliberately not
// here. They belong to the Extensions module (broker.QueryExtensionsState,
// served from sysext.ExtensionsSource), which owns both reporting them and
// acting on them; maintenance consumes the same aggregate only to derive one
// reboot reason from it (a merged-but-disabled extension stays active until
// reboot).
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
	Jobs              []Job    `json:"jobs"`
	OSVersion         string   `json:"os_version"`
	RebootReasons     []string `json:"reboot_reasons"`
	RebootRequired    bool     `json:"reboot_required"`
	SoftRebootCapable *bool    `json:"soft_reboot_capable,omitempty"`
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
	cacheAt         time.Time
	cacheExtensions []sysext.Extension
	extensions      sysext.ExtensionsSource
	hostImage       HostImageSource
	jobs            JobSource
	mu              sync.Mutex
	now             func() time.Time
	root            string
	runner          Runner
	sysextAvailable bool
	updexAvailable  bool
}

// NewSystemManager constructs a maintenance manager.
//
// updexAvailable, sysextAvailable, and bootcAvailable report whether the updex
// executable, systemd-sysext, and bootc are present on this host (as probed by
// internal/capability). All three follow one convention: a source whose flag is
// false is never invoked, and State degrades by omitting that source's
// contribution rather than returning an error. bootcAvailable is enforced here
// (hostImageState skips the read outright); updexAvailable and sysextAvailable
// are threaded verbatim into sysext.ExtensionsSource.State, which enforces the
// same rule per source on this manager's behalf. All three are independent of
// the systemd capability that gates registerMaintenance's registration entirely.
//
// extensions is the read-only extensions seam (the same sysext manager
// cmd/pilothoused serves broker.QueryExtensionsState from, as one daemon-
// internal instance rather than a second broker round trip). State consults it
// for exactly one fact: which extensions are merged but disabled, and thus
// remain active until reboot. It is never used to mutate anything — the
// interface has no mutating method — and may be nil, which State treats as
// "no extension-derived reboot reasons".
//
// The two consumers share the interface and the instance, not a result:
// sysext.SystemManager.State has no cache of its own, so a QueryExtensionsState
// and a QueryMaintenanceState in the same minute may run it twice (only the
// 1-minute cache in extensionState below is a real cache, and it is this
// manager's alone).
//
// hostImage is the read-only host-image seam (the same HostImageManager
// cmd/pilothoused serves QueryHostImageStatus from). State consults it only
// when bootcAvailable is true, and only to read two facts: whether a staged
// deployment exists (a reboot-required reason) and the host's soft-reboot
// eligibility (informational). It is never used to mutate anything — the
// interface has no mutating method — and may be nil, which State treats
// exactly like bootcAvailable being false.
func NewSystemManager(extensions sysext.ExtensionsSource, jobSource JobSource, hostImage HostImageSource, runner Runner, root string, updexAvailable, sysextAvailable, bootcAvailable bool) *SystemManager {
	if root == "" {
		root = "/"
	}
	return &SystemManager{bootcAvailable: bootcAvailable, extensions: extensions, hostImage: hostImage, jobs: jobSource, now: time.Now, root: root, runner: runner, sysextAvailable: sysextAvailable, updexAvailable: updexAvailable}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	extensions := m.extensionState(ctx)
	jobRecords, err := m.jobs.List(ctx, jobs.Filter{Limit: 20})
	if err != nil {
		return State{}, fmt.Errorf("list maintenance jobs: %w", err)
	}
	// Normalize slice fields to non-nil so they serialize as JSON `[]` rather
	// than `null` over the broker protocol. Downstream JSON consumers should
	// not have to special-case null vs empty array.
	state := State{Jobs: make([]Job, 0, len(jobRecords)), OSVersion: m.osVersion(), RebootReasons: []string{}}
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
	for _, extension := range extensions {
		if extension.Merged && !extension.Enabled {
			state.RebootReasons = append(state.RebootReasons, extension.Name+" is disabled but remains active until reboot.")
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

// extensionState reads the shared extensions aggregate and returns the union
// inventory State derives its merged-but-disabled reboot reasons from. It
// cannot fail: every extension failure mode degrades to "no extension-derived
// reboot reasons" rather than to an error, which is what keeps
// QueryMaintenanceState a 200 on a host whose updex or systemd-sysext cannot
// answer.
//
// There is no per-capability branching here on purpose. The probed
// updexAvailable/sysextAvailable flags are threaded straight into
// sysext.ExtensionsSource.State, which owns the never-attempt rule for both
// sources: a source whose tool the host does not have is never invoked, and a
// source that is invoked and fails sets only its own *Available/*Error pair in
// the returned ExtensionsState, leaving the other source's data intact. That
// contract is why State returns no error for a source-level failure, and
// extensionState inherits the guarantee rather than restating it.
//
// The err result is still checked, not ignored: sysext's contract reserves it
// for conditions outside per-source reporting, and the honest handling of one
// here is the same degrade — drop the extension contribution for this call,
// exactly as hostImageState does for a failed host-image read. Nothing is
// cached in that case, so the next call retries.
//
// The 1-minute cache is unchanged in behavior and is this manager's alone: the
// sysext source it wraps has no cache, so a concurrent QueryExtensionsState
// runs its own independent read.
func (m *SystemManager) extensionState(ctx context.Context) []sysext.Extension {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.extensions == nil {
		return nil
	}
	if !m.cacheAt.IsZero() && m.now().Sub(m.cacheAt) < time.Minute {
		return slices.Clone(m.cacheExtensions)
	}
	state, err := m.extensions.State(ctx, m.updexAvailable, m.sysextAvailable)
	if err != nil {
		return nil
	}
	m.cacheExtensions = slices.Clone(state.Extensions)
	m.cacheAt = m.now()
	return state.Extensions
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
//     be read, the same way an unreadable extension source omits the
//     merged-but-disabled reboot reasons rather than failing State.
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
