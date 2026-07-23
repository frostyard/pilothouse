package capability

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

// execProbeTimeout bounds every exec-based probe in this file, matching the
// spec's 5-second figure for command-based probes (see probe_systemd.go's
// dbusProbeTimeout for the equivalent on the D-Bus-backed probe).
const execProbeTimeout = 5 * time.Second

// defaultUpdex is the executable name resolved via PATH lookup -- matching
// the existing --updex flag's default in cmd/pilothoused/main.go -- used
// when no executable is configured.
const defaultUpdex = "updex"

// CommandRunner runs an external command and returns its combined output.
// It mirrors internal/modules/sysext.CommandRunner's shape but is kept
// local to this package: internal/capability is foundational and must not
// depend on a feature module.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the production CommandRunner. It runs the named command via
// exec.CommandContext -- never a shell -- with every argument passed as a
// separate argv element, never interpolated into a command string.
type ExecRunner struct{}

// Run implements CommandRunner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// ProbeUpdex probes the updex capability: present iff the configured
// executable runs `--help` and exits 0. updex is the executable configured
// via cmd/pilothoused's --updex flag; an empty value falls back to a PATH
// lookup of "updex", matching that flag's own default. This deliberately
// never invokes `--json features` and never passes a definitions-root
// argument -- the probe must not depend on a configured definitions root.
func ProbeUpdex(ctx context.Context, runner CommandRunner, updex string) Set {
	if updex == "" {
		updex = defaultUpdex
	}
	return probeExecSuccess(ctx, runner, Updex, updex, "--help")
}

// ProbeSysext probes the systemd-sysext capability: present iff
// `systemd-sysext list` exits 0.
func ProbeSysext(ctx context.Context, runner CommandRunner) Set {
	return probeExecSuccess(ctx, runner, Sysext, "systemd-sysext", "list")
}

// ProbeBootc probes the bootc capability: present iff `bootc status --json`
// exits 0 and the output parses as JSON.
func ProbeBootc(ctx context.Context, runner CommandRunner) Set {
	return probeExecJSON(ctx, runner, Bootc, "bootc", "status", "--json")
}

// ProbeRPMOStree probes the rpm-ostree capability: present iff `rpm-ostree
// status --json` exits 0 and the output parses as JSON.
func ProbeRPMOStree(ctx context.Context, runner CommandRunner) Set {
	return probeExecJSON(ctx, runner, RPMOStree, "rpm-ostree", "status", "--json")
}

// probeExecSuccess reports id present iff running name with args, bounded
// by execProbeTimeout, exits 0. A non-nil error (including a non-zero
// exit) simply narrows the result -- it is never fatal or propagated,
// matching every other probe in this package.
func probeExecSuccess(ctx context.Context, runner CommandRunner, id ID, name string, args ...string) Set {
	probeCtx, cancel := context.WithTimeout(ctx, execProbeTimeout)
	defer cancel()

	if _, err := runner.Run(probeCtx, name, args...); err != nil {
		return New()
	}
	return New(id)
}

// probeExecJSON reports id present iff running name with args, bounded by
// execProbeTimeout, exits 0 AND the combined output parses as JSON. A
// json.Valid check is sufficient; this deliberately does not depend on any
// specific field of the output.
func probeExecJSON(ctx context.Context, runner CommandRunner, id ID, name string, args ...string) Set {
	probeCtx, cancel := context.WithTimeout(ctx, execProbeTimeout)
	defer cancel()

	output, err := runner.Run(probeCtx, name, args...)
	if err != nil {
		return New()
	}
	if !json.Valid(output) {
		return New()
	}
	return New(id)
}
