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

	for _, testCase := range []struct {
		name       string
		signal     syscall.Signal
		exitStatus int
	}{
		{name: "INT", signal: syscall.SIGINT, exitStatus: 130},
		{name: "TERM", signal: syscall.SIGTERM, exitStatus: 143},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testUCoreOrchestratorSignal(
				t,
				functions,
				testCase.signal,
				testCase.exitStatus,
			)
		})
	}
}

func testUCoreOrchestratorSignal(
	t *testing.T,
	functions map[string]string,
	signal syscall.Signal,
	exitStatus int,
) {
	t.Helper()

	sandbox := t.TempDir()
	childPIDPath := filepath.Join(sandbox, "child.pid")
	probePath := filepath.Join(sandbox, "signal-probe.sh")
	probe := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
readonly LOG_KIB=64
readonly LOG_BYTES=$((LOG_KIB * 1024))
workspace="$1"
current_phase_pid=""
%s
%s
%s
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
run_bounded signal-probe 30s bash -c \
    'printf "%%s\n" "$BASHPID" >"$1"; exec sleep 30' probe "$2"
`, functions["log"], functions["handle_signal"], functions["run_bounded"])
	require.NoError(t, os.WriteFile(probePath, []byte(probe), 0o700))

	command := exec.Command("bash", probePath, sandbox, childPIDPath)
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
		if childPIDBytes, readErr := os.ReadFile(childPIDPath); readErr == nil {
			if childPID, parseErr := strconv.Atoi(strings.TrimSpace(string(childPIDBytes))); parseErr == nil {
				_ = syscall.Kill(-childPID, syscall.SIGKILL)
			}
		}
	})

	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		childPIDBytes, readErr := os.ReadFile(childPIDPath)
		if readErr == nil {
			var parseErr error
			childPID, parseErr = strconv.Atoi(strings.TrimSpace(string(childPIDBytes)))
			require.NoError(t, parseErr)
			break
		}
		require.True(t, errors.Is(readErr, os.ErrNotExist), readErr)
		time.Sleep(10 * time.Millisecond)
	}
	require.NotZero(t, childPID, "the bounded phase never reported its child PID")

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

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if killErr := syscall.Kill(childPID, 0); errors.Is(killErr, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("phase child %d survived orchestrator %s forwarding", childPID, signal)
}
