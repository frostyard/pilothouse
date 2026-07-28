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
		case "log", "handle_signal", "run_bounded":
			var rendered bytes.Buffer
			require.NoError(t, syntax.NewPrinter().Print(&rendered, declaration))
			functions[declaration.Name.Value] = rendered.String()
		}
		return true
	})
	require.Len(t, functions, 3)

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
					"%s/cleanup=%t/before-group=%t",
					testCase.name,
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
					)
				})
			}
		}
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

func testUCoreOrchestratorSignal(
	t *testing.T,
	functions map[string]string,
	signal syscall.Signal,
	exitStatus int,
	cleanupActive bool,
	delayGroup bool,
) {
	t.Helper()

	sandbox := t.TempDir()
	childPIDPath := filepath.Join(sandbox, "child.pid")
	groupDelayPIDPath := filepath.Join(sandbox, "group-delay.pid")
	cleanupContinuedPath := filepath.Join(sandbox, "cleanup-continued")
	probePath := filepath.Join(sandbox, "signal-probe.sh")
	probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
readonly LOG_KIB=64
readonly LOG_BYTES=$((LOG_KIB * 1024))
workspace="$1"
current_phase_pid=""
cleanup_active=%d
termination_status=0
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
`, boolInt(cleanupActive), functions["log"], functions["handle_signal"], functions["run_bounded"])
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
	command.Env = append(
		os.Environ(),
		"PATH="+wrapperDir+":"+os.Getenv("PATH"),
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

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pidBytes, readErr := os.ReadFile(path)
		if readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			require.NoError(t, parseErr)
			return pid
		}
		require.True(t, errors.Is(readErr, os.ErrNotExist), readErr)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the bounded phase never reported its process PID in %s", path)
	return 0
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
