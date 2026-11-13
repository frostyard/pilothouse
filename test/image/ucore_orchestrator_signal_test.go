package image_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"
)

func TestUCoreOrchestratorForwardsSignalsAndWaits(t *testing.T) {
	source, err := os.ReadFile("ucore-image-test.sh")
	require.NoError(t, err)

	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(bytes.NewReader(source), "ucore-image-test.sh")
	require.NoError(t, err)

	functions := map[string]string{}
	syntax.Walk(file, func(node syntax.Node) bool {
		declaration, ok := node.(*syntax.FuncDecl)
		if !ok || declaration.Name == nil {
			return true
		}
		switch declaration.Name.Value {
		case "log", "handle_signal", "stop_phase_group", "run_bounded":
			var rendered bytes.Buffer
			require.NoError(t, syntax.NewPrinter().Print(&rendered, declaration))
			functions[declaration.Name.Value] = rendered.String()
		}
		return true
	})
	require.Len(t, functions, 4)

	// The defect of #255 needs 2 conditions. bash 5.3 keeps SIGINT ignored
	// through exec in the asynchronous subshell of run_bounded (bash 5.2
	// restored the default), and the timeout must keep that SIG_IGN: uutils
	// timeout (Ubuntu 26.04) does, GNU timeout installs its own handler. The
	// keeps-ignored shim models uutils timeout, so on bash 5.3 or later the
	// INT cases fail without the fix whatever timeout the host has.
	for _, keepIgnored := range []bool{false, true} {
		for _, cleanupActive := range []bool{false, true} {
			for _, delayGroup := range []bool{false, true} {
				for _, testCase := range []struct {
					name       string
					signal     syscall.Signal
					exitStatus int
				}{
					{name: "INT", signal: syscall.SIGINT, exitStatus: 130},
					{name: "TERM", signal: syscall.SIGTERM, exitStatus: 143},
				} {
					name := fmt.Sprintf(
						"%s/timeout=%s/cleanup=%t/before-group=%t",
						testCase.name,
						timeoutVariantName(keepIgnored),
						cleanupActive,
						delayGroup,
					)
					t.Run(name, func(t *testing.T) {
						testUCoreOrchestratorSignal(
							t,
							functions,
							testCase.signal,
							testCase.exitStatus,
							cleanupActive,
							delayGroup,
							keepIgnored,
						)
					})
				}
			}
		}
	}
}

// TestUCoreRunBoundedRestoresDefaultInterruptAndQuit checks the signal
// dispositions that the phase command starts with. Without job control,
// bash starts the asynchronous subshell of run_bounded with SIGINT and
// SIGQUIT ignored, and from bash 5.3 exec keeps them ignored; run_bounded
// must restore both defaults whatever timeout implementation the host has
// (#255). On bash 5.2 the case passes without the fix too.
func TestUCoreRunBoundedRestoresDefaultInterruptAndQuit(t *testing.T) {
	source, err := os.ReadFile("ucore-image-test.sh")
	require.NoError(t, err)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(bytes.NewReader(source), "ucore-image-test.sh")
	require.NoError(t, err)

	functions := map[string]string{}
	syntax.Walk(file, func(node syntax.Node) bool {
		declaration, ok := node.(*syntax.FuncDecl)
		if !ok || declaration.Name == nil {
			return true
		}
		switch declaration.Name.Value {
		case "log", "handle_signal", "stop_phase_group", "run_bounded":
			var rendered bytes.Buffer
			require.NoError(t, syntax.NewPrinter().Print(&rendered, declaration))
			functions[declaration.Name.Value] = rendered.String()
		}
		return true
	})
	require.Len(t, functions, 4)

	for _, keepIgnored := range []bool{false, true} {
		t.Run("timeout="+timeoutVariantName(keepIgnored), func(t *testing.T) {
			sandbox := t.TempDir()
			ignoredPath := filepath.Join(sandbox, "sigignore")
			probePath := filepath.Join(sandbox, "disposition-probe.sh")
			scriptDir, getwdErr := os.Getwd()
			require.NoError(t, getwdErr)
			probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
readonly LOG_BYTES=65536
readonly SCRIPT_DIR=%q
workspace="$1"
current_phase_pid=""
cleanup_active=0
termination_status=0
%s
%s
%s
%s
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
run_bounded disposition-probe 30s bash -c \
    'exec grep "^SigIgn:" /proc/self/status >"$1"' probe "$2"
`, scriptDir, functions["log"], functions["handle_signal"],
				functions["stop_phase_group"], functions["run_bounded"])
			require.NoError(t, os.WriteFile(probePath, []byte(probe), 0o700))

			command := exec.Command("bash", probePath, sandbox, ignoredPath)
			if keepIgnored {
				command.Env = append(os.Environ(),
					"PATH="+keepIgnoredTimeoutDir(t, sandbox)+":"+os.Getenv("PATH"))
			}
			output, runErr := command.CombinedOutput()
			require.NoError(t, runErr, string(output))

			// bash ignores SIGQUIT for itself, so the probe execs grep and
			// reads the dispositions of a process that is not a shell.
			recorded, readErr := os.ReadFile(ignoredPath)
			require.NoError(t, readErr, string(output))
			fields := strings.Fields(string(recorded))
			require.Len(t, fields, 2, string(recorded))
			ignored, parseErr := strconv.ParseUint(fields[1], 16, 64)
			require.NoError(t, parseErr)
			for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGQUIT} {
				require.Zerof(t, ignored&(1<<(uint(signal)-1)),
					"the phase command must not start with %s ignored (SigIgn %s)",
					signal, fields[1])
			}
		})
	}
}

func timeoutVariantName(keepIgnored bool) string {
	if keepIgnored {
		return "keeps-ignored"
	}
	return "host"
}

// keepIgnoredTimeoutDir writes a timeout shim that drops its options and
// duration and execs the command unchanged, so the command keeps every
// inherited signal disposition. uutils coreutils timeout 0.10.0 (Ubuntu
// 26.04) behaves this way for an ignored SIGINT; GNU timeout does not. The
// shim does not enforce the duration: callers end the phase themselves.
// /bin/sh must keep an ignored signal through exec, as POSIX requires.
func keepIgnoredTimeoutDir(t *testing.T, sandbox string) string {
	t.Helper()

	shimDir := filepath.Join(sandbox, "timeout-shim")
	require.NoError(t, os.Mkdir(shimDir, 0o700))
	shim := `#!/bin/sh
while [ "$#" -gt 0 ]; do
    case "$1" in
        --*) shift ;;
        *) shift; break ;;
    esac
done
exec "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(shimDir, "timeout"), []byte(shim), 0o700))
	return shimDir
}

func TestUCoreVMReadonlyEnvironmentRejectsArithmeticMutation(t *testing.T) {
	for _, name := range []string{"PATH", "CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF"} {
		t.Run(name, func(t *testing.T) {
			script := `
set -eu
PATH=/usr/bin:/bin
readonly PATH
CONTAINERS_CONF=/dev/null
CONTAINERS_STORAGE_CONF=/var/tmp/pilothouse-image-test/storage.conf
export CONTAINERS_CONF CONTAINERS_STORAGE_CONF
readonly CONTAINERS_CONF CONTAINERS_STORAGE_CONF
(($MUTATION_TARGET=1))
`
			command := exec.Command("bash", "-c", script)
			command.Env = append(os.Environ(), "MUTATION_TARGET="+name)
			output, err := command.CombinedOutput()
			require.Error(t, err,
				"bash arithmetic assignment must not mutate readonly %s", name)
			require.Contains(t, string(output), "readonly variable")
		})
	}
}

func TestUCoreOrchestratorCleanupRequiresWorkspaceOwnership(t *testing.T) {
	source, err := os.ReadFile("ucore-image-test.sh")
	require.NoError(t, err)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(bytes.NewReader(source), "ucore-image-test.sh")
	require.NoError(t, err)

	var cleanupFunction string
	syntax.Walk(file, func(node syntax.Node) bool {
		declaration, ok := node.(*syntax.FuncDecl)
		if !ok || declaration.Name == nil || declaration.Name.Value != "cleanup" {
			return true
		}
		var rendered bytes.Buffer
		require.NoError(t, syntax.NewPrinter().Print(&rendered, declaration))
		cleanupFunction = rendered.String()
		return false
	})
	require.NotEmpty(t, cleanupFunction)

	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprintf("owned=%t", owned), func(t *testing.T) {
			sandbox := t.TempDir()
			workspace := filepath.Join(sandbox, "workspace")
			require.NoError(t, os.Mkdir(workspace, 0o700))
			markerPath := filepath.Join(workspace, "foreign-marker")
			require.NoError(t, os.WriteFile(markerPath, []byte("keep\n"), 0o600))
			resetMarkerPath := filepath.Join(sandbox, "reset-called")
			statePath := filepath.Join(sandbox, "workspace-created")
			probePath := filepath.Join(sandbox, "cleanup-probe.sh")
			probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
workspace="$1"
reset_marker="$2"
state_path="$3"
workspace_created=%d
cleanup_active=0
current_phase_pid=""
reset_private_store() {
    printf 'reset\n' >"$reset_marker"
}
%s
cleanup
printf '%%s\n' "$workspace_created" >"$state_path"
`, boolInt(owned), cleanupFunction)
			require.NoError(t, os.WriteFile(probePath, []byte(probe), 0o700))

			output, runErr := exec.Command(
				"bash",
				probePath,
				workspace,
				resetMarkerPath,
				statePath,
			).CombinedOutput()
			require.NoError(t, runErr, string(output))

			state, readErr := os.ReadFile(statePath)
			require.NoError(t, readErr)
			if owned {
				require.Equal(t, "0\n", string(state))
				_, statErr := os.Stat(workspace)
				require.ErrorIs(t, statErr, os.ErrNotExist)
				resetMarker, resetErr := os.ReadFile(resetMarkerPath)
				require.NoError(t, resetErr)
				require.Equal(t, "reset\n", string(resetMarker))
				return
			}

			require.Equal(t, "0\n", string(state))
			marker, markerErr := os.ReadFile(markerPath)
			require.NoError(t, markerErr,
				"cleanup must not remove a workspace this invocation did not create")
			require.Equal(t, "keep\n", string(marker))
			_, resetErr := os.Stat(resetMarkerPath)
			require.ErrorIs(t, resetErr, os.ErrNotExist,
				"cleanup must not reset storage under an unowned workspace")
		})
	}
}

func TestUCoreOrchestratorCleanupPreservesWorkspaceForSurvivingReset(t *testing.T) {
	source, err := os.ReadFile("ucore-image-test.sh")
	require.NoError(t, err)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(bytes.NewReader(source), "ucore-image-test.sh")
	require.NoError(t, err)

	var cleanupFunction string
	syntax.Walk(file, func(node syntax.Node) bool {
		declaration, ok := node.(*syntax.FuncDecl)
		if !ok || declaration.Name == nil || declaration.Name.Value != "cleanup" {
			return true
		}
		var rendered bytes.Buffer
		require.NoError(t, syntax.NewPrinter().Print(&rendered, declaration))
		cleanupFunction = rendered.String()
		return false
	})
	require.NotEmpty(t, cleanupFunction)

	sandbox := t.TempDir()
	workspace := filepath.Join(sandbox, "workspace")
	require.NoError(t, os.Mkdir(workspace, 0o700))
	markerPath := filepath.Join(workspace, "survivor-marker")
	require.NoError(t, os.WriteFile(markerPath, []byte("keep\n"), 0o600))
	resultPath := filepath.Join(sandbox, "cleanup-result")
	probePath := filepath.Join(sandbox, "surviving-reset-probe.sh")
	probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
workspace="$1"
result_path="$2"
workspace_created=1
cleanup_active=0
current_phase_pid=""
reset_private_store() {
    current_phase_pid="surviving-reset-group"
    return 1
}
%s
cleanup_result=0
cleanup || cleanup_result=$?
printf '%%s\n' "$cleanup_result" >"$result_path"
`, cleanupFunction)
	require.NoError(t, os.WriteFile(probePath, []byte(probe), 0o700))

	output, runErr := exec.Command("bash", probePath, workspace, resultPath).CombinedOutput()
	require.NoError(t, runErr, string(output))
	result, readErr := os.ReadFile(resultPath)
	require.NoError(t, readErr)
	require.Equal(t, "1\n", string(result))
	marker, readErr := os.ReadFile(markerPath)
	require.NoError(t, readErr,
		"cleanup must preserve a workspace while its reset group survives")
	require.Equal(t, "keep\n", string(marker))
}

func TestUCoreOrchestratorCapsOnlyRetainedOutput(t *testing.T) {
	source, err := os.ReadFile("ucore-image-test.sh")
	require.NoError(t, err)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(bytes.NewReader(source), "ucore-image-test.sh")
	require.NoError(t, err)

	functions := map[string]string{}
	syntax.Walk(file, func(node syntax.Node) bool {
		declaration, ok := node.(*syntax.FuncDecl)
		if !ok || declaration.Name == nil {
			return true
		}
		switch declaration.Name.Value {
		case "log", "handle_signal", "stop_phase_group", "run_bounded":
			var rendered bytes.Buffer
			require.NoError(t, syntax.NewPrinter().Print(&rendered, declaration))
			functions[declaration.Name.Value] = rendered.String()
		}
		return true
	})
	require.Len(t, functions, 4)

	sandbox := t.TempDir()
	artifactPath := filepath.Join(sandbox, "large-artifact")
	probePath := filepath.Join(sandbox, "output-cap-probe.sh")
	scriptDir, err := os.Getwd()
	require.NoError(t, err)
	probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
readonly LOG_BYTES=65536
readonly SCRIPT_DIR=%q
workspace="$1"
current_phase_pid=""
cleanup_active=0
termination_status=0
%s
%s
%s
%s
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
run_bounded output-cap-probe 30s bash -c \
    'dd if=/dev/zero of="$1" bs=1M count=5 status=none
     dd if=/dev/zero bs=1M count=5 status=none' probe "$2"
`, scriptDir, functions["log"], functions["handle_signal"], functions["stop_phase_group"], functions["run_bounded"])
	require.NoError(t, os.WriteFile(probePath, []byte(probe), 0o700))

	output, runErr := exec.Command("bash", probePath, sandbox, artifactPath).CombinedOutput()
	require.NoError(t, runErr, string(output))

	artifact, statErr := os.Stat(artifactPath)
	require.NoError(t, statErr)
	require.EqualValues(t, 5*1024*1024, artifact.Size(),
		"the retained-output cap must not become a phase-wide file-size limit")
	phaseLog, statErr := os.Stat(filepath.Join(sandbox, "output-cap-probe.log"))
	require.NoError(t, statErr)
	require.EqualValues(t, 65536, phaseLog.Size(),
		"the retained phase log must stay at its configured bound")
}

func TestUCoreOrchestratorRejectsSurvivingPhaseDescendants(t *testing.T) {
	source, err := os.ReadFile("ucore-image-test.sh")
	require.NoError(t, err)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(bytes.NewReader(source), "ucore-image-test.sh")
	require.NoError(t, err)

	functions := map[string]string{}
	syntax.Walk(file, func(node syntax.Node) bool {
		declaration, ok := node.(*syntax.FuncDecl)
		if !ok || declaration.Name == nil {
			return true
		}
		switch declaration.Name.Value {
		case "log", "handle_signal", "stop_phase_group", "run_bounded":
			var rendered bytes.Buffer
			require.NoError(t, syntax.NewPrinter().Print(&rendered, declaration))
			functions[declaration.Name.Value] = rendered.String()
		}
		return true
	})
	require.Len(t, functions, 4)

	for _, testCase := range []struct {
		name             string
		childRedirection string
	}{
		{name: "inherits-output", childRedirection: ""},
		{name: "closes-output", childRedirection: ">/dev/null 2>&1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sandbox := t.TempDir()
			childPIDPath := filepath.Join(sandbox, "descendant.pid")
			probePath := filepath.Join(sandbox, "descendant-probe.sh")
			scriptDir, getwdErr := os.Getwd()
			require.NoError(t, getwdErr)
			probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
readonly LOG_BYTES=65536
readonly SCRIPT_DIR=%q
workspace="$1"
current_phase_pid=""
cleanup_active=0
termination_status=0
%s
%s
%s
%s
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
phase_status=0
run_bounded descendant-probe 1s bash -c \
    'printf "diagnostic-marker\n"
     sleep 30 %s & printf "%%s\n" "$!" >"$1"' probe "$2" ||
    phase_status=$?
((phase_status != 0))
`, scriptDir, functions["log"], functions["handle_signal"], functions["stop_phase_group"], functions["run_bounded"], testCase.childRedirection)
			require.NoError(t, os.WriteFile(probePath, []byte(probe), 0o700))

			started := time.Now()
			output, runErr := exec.Command("bash", probePath, sandbox, childPIDPath).CombinedOutput()
			require.NoError(t, runErr, string(output))
			require.Less(t, time.Since(started), 8*time.Second,
				"the bounded phase must not wait indefinitely for a surviving descendant")

			childPID := waitForPIDFile(t, childPIDPath)
			require.ErrorIs(t, syscall.Kill(childPID, 0), syscall.ESRCH,
				"the surviving same-group descendant must be terminated and reaped")
			phaseLog, readErr := os.ReadFile(filepath.Join(sandbox, "descendant-probe.log"))
			require.NoError(t, readErr)
			require.Contains(t, string(phaseLog), "diagnostic-marker\n",
				"soft termination must let the collector flush bounded diagnostics")
		})
	}
}

func testUCoreOrchestratorSignal(
	t *testing.T,
	functions map[string]string,
	signal syscall.Signal,
	exitStatus int,
	cleanupActive bool,
	delayGroup bool,
	keepIgnored bool,
) {
	t.Helper()

	sandbox := t.TempDir()
	childPIDPath := filepath.Join(sandbox, "child.pid")
	groupDelayPIDPath := filepath.Join(sandbox, "group-delay.pid")
	cleanupContinuedPath := filepath.Join(sandbox, "cleanup-continued")
	probePath := filepath.Join(sandbox, "signal-probe.sh")
	scriptDir, err := os.Getwd()
	require.NoError(t, err)
	probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
readonly LOG_KIB=64
readonly LOG_BYTES=$((LOG_KIB * 1024))
readonly SCRIPT_DIR=%q
workspace="$1"
current_phase_pid=""
cleanup_active=%d
termination_status=0
%s
%s
%s
%s
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
phase_status=0
run_bounded signal-probe 30s bash -c \
    'printf "%%s\n" "$BASHPID" >"$1"; exec sleep 30' probe "$2" ||
    phase_status=$?
if ((cleanup_active != 0)); then
    printf 'continued\n' >"$3"
    exit "$termination_status"
fi
exit "$phase_status"
`, scriptDir, boolInt(cleanupActive), functions["log"], functions["handle_signal"], functions["stop_phase_group"], functions["run_bounded"])
	require.NoError(t, os.WriteFile(probePath, []byte(probe), 0o700))

	command := exec.Command(
		"bash",
		probePath,
		sandbox,
		childPIDPath,
		cleanupContinuedPath,
	)
	realSetsid, lookErr := exec.LookPath("setsid")
	require.NoError(t, lookErr)
	wrapperDir := filepath.Join(sandbox, "bin")
	require.NoError(t, os.Mkdir(wrapperDir, 0o700))
	wrapperPath := filepath.Join(wrapperDir, "setsid")
	wrapper := `#!/bin/sh
printf '%s\n' "$$" >"$SIGNAL_GROUP_DELAY_PID_PATH"
if [ "$SIGNAL_DELAY_GROUP" = 1 ]; then
    sleep 0.1
fi
exec "$SIGNAL_REAL_SETSID" "$@"
`
	require.NoError(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o700))
	path := wrapperDir + ":" + os.Getenv("PATH")
	if keepIgnored {
		path = keepIgnoredTimeoutDir(t, sandbox) + ":" + path
	}
	command.Env = append(
		os.Environ(),
		"PATH="+path,
		"SIGNAL_DELAY_GROUP="+strconv.Itoa(boolInt(delayGroup)),
		"SIGNAL_GROUP_DELAY_PID_PATH="+groupDelayPIDPath,
		"SIGNAL_REAL_SETSID="+realSetsid,
	)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		for _, pidPath := range []string{childPIDPath, groupDelayPIDPath} {
			if pidBytes, readErr := os.ReadFile(pidPath); readErr == nil {
				if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes))); parseErr == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			}
		}
	})

	signalTargetPIDPath := childPIDPath
	if delayGroup {
		signalTargetPIDPath = groupDelayPIDPath
	}
	signalTargetPID := waitForPIDFile(t, signalTargetPIDPath)

	require.NoError(t, command.Process.Signal(signal))
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- command.Wait()
	}()

	select {
	case waitErr := <-waitResult:
		var exitError *exec.ExitError
		require.ErrorAs(t, waitErr, &exitError,
			"the orchestrator probe must report its signal-derived failure")
		require.Equal(t, exitStatus, exitError.ExitCode())
	case <-time.After(5 * time.Second):
		t.Fatalf(
			"orchestrator did not forward %s and exit promptly\nstdout:\n%s\nstderr:\n%s",
			signal,
			stdout.String(),
			stderr.String(),
		)
	}

	if cleanupActive {
		continued, readErr := os.ReadFile(cleanupContinuedPath)
		require.NoError(t, readErr,
			"a follow-on signal must return to the cleanup frame")
		require.Equal(t, "continued\n", string(continued))
	} else {
		_, readErr := os.Stat(cleanupContinuedPath)
		require.ErrorIs(t, readErr, os.ErrNotExist)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if killErr := syscall.Kill(signalTargetPID, 0); errors.Is(killErr, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("phase process %d survived orchestrator %s forwarding", signalTargetPID, signal)
}

// waitForPIDFile polls for a PID that a shell fixture writes with
// `printf '%s\n' "$pid" >path`. The redirection creates the file empty
// before printf writes it, so an empty or partial read is normal; only
// newline-terminated content is a complete PID record. Parsing earlier
// fails on "" or, worse, returns a truncated PID.
func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pidBytes, readErr := os.ReadFile(path)
		if readErr == nil && bytes.HasSuffix(pidBytes, []byte("\n")) {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			require.NoError(t, parseErr)
			return pid
		}
		if readErr != nil {
			require.True(t, errors.Is(readErr, os.ErrNotExist), readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the bounded phase never reported its process PID in %s", path)
	return 0
}

func TestWaitForPIDFileWaitsForCompleteRecord(t *testing.T) {
	// Reproduces the 2026-10-24 nightly flake deterministically: the shell
	// redirection creates the PID file empty, then printf fills it.
	path := filepath.Join(t.TempDir(), "child.pid")
	writerDone := make(chan error, 1)
	go func() {
		file, err := os.Create(path)
		if err != nil {
			writerDone <- err
			return
		}
		for _, chunk := range []string{"", "12", "345\n"} {
			if _, err := file.WriteString(chunk); err != nil {
				_ = file.Close()
				writerDone <- err
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		writerDone <- file.Close()
	}()

	require.Equal(t, 12345, waitForPIDFile(t, path),
		"an empty or partial PID file must be polled again, not parsed")
	require.NoError(t, <-writerDone)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
