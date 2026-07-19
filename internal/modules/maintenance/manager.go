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

type State struct {
	Jobs           []Job                    `json:"jobs"`
	OSVersion      string                   `json:"os_version"`
	RebootReasons  []string                 `json:"reboot_reasons"`
	RebootRequired bool                     `json:"reboot_required"`
	Updates        []sysext.AvailableUpdate `json:"updates"`
}

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
	cacheFeatures []sysext.Feature
	cacheUpdates  []sysext.AvailableUpdate
	cacheAt       time.Time
	jobs          JobSource
	mu            sync.Mutex
	now           func() time.Time
	root          string
	runner        Runner
	updates       UpdateSource
}

func NewSystemManager(updates UpdateSource, jobSource JobSource, runner Runner, root string) *SystemManager {
	if root == "" {
		root = "/"
	}
	return &SystemManager{jobs: jobSource, now: time.Now, root: root, runner: runner, updates: updates}
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
	state := State{Jobs: make([]Job, 0, len(jobRecords)), OSVersion: m.osVersion(), Updates: available}
	for _, job := range jobRecords {
		state.Jobs = append(state.Jobs, Job{ID: job.ID, Action: job.Action, Resource: job.Resource, Status: job.Status, ErrorCategory: job.ErrorCategory, RebootRequired: job.RebootRequired})
	}
	if _, err := os.Stat(m.path("run/reboot-required")); err == nil {
		state.RebootReasons = append(state.RebootReasons, "The operating system requested a reboot.")
	} else if !os.IsNotExist(err) {
		return State{}, fmt.Errorf("inspect reboot marker: %w", err)
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

func (m *SystemManager) extensionState(ctx context.Context) ([]sysext.AvailableUpdate, []sysext.Feature, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cacheAt.IsZero() && m.now().Sub(m.cacheAt) < time.Minute {
		return slices.Clone(m.cacheUpdates), slices.Clone(m.cacheFeatures), nil
	}
	available, err := m.updates.Check(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("check extension updates: %w", err)
	}
	features, err := m.updates.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect extension state: %w", err)
	}
	m.cacheUpdates = slices.Clone(available)
	m.cacheFeatures = slices.Clone(features)
	m.cacheAt = m.now()
	return available, features, nil
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
