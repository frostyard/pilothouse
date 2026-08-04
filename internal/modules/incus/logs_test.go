package incus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsResolvesFilenameFromInstanceType proves the supervisor logfile is
// derived from the resolved instance's own type rather than from anything
// the caller supplies: a container reads lxc.log, a virtual machine reads
// qemu.log, and no other filename is ever requested.
func TestLogsResolvesFilenameFromInstanceType(t *testing.T) {
	client := stateClient()
	manager := NewSystemManager(client)

	logs, err := manager.Logs(context.Background(), "production", "api", SourceLog)
	require.NoError(t, err)
	assert.Contains(t, client.actions, "logfile production api lxc.log")
	assert.Equal(t, SourceLog, logs.Source)
	assert.Equal(t, []LogLine{{Message: "lxc supervisor line"}}, logs.Lines)

	client = stateClient()
	_, err = NewSystemManager(client).Logs(context.Background(), "production", "worker-vm", SourceLog)
	require.NoError(t, err)
	assert.Contains(t, client.actions, "logfile production worker-vm qemu.log")
}

func TestLogsReadsConsoleSource(t *testing.T) {
	client := stateClient()
	logs, err := NewSystemManager(client).Logs(context.Background(), "production", "api", SourceConsole)
	require.NoError(t, err)
	assert.Contains(t, client.actions, "console production api")
	assert.Equal(t, SourceConsole, logs.Source)
	assert.Equal(t, []LogLine{{Message: "console boot line"}, {Message: "console ready"}}, logs.Lines)
	for _, action := range client.actions {
		assert.False(t, strings.HasPrefix(action, "logfile "), "the console source must not read a logfile")
	}
}

// TestLogsRejectsUnsupportedSource proves the source selector is a closed
// enumeration checked before anything is read, so no caller-supplied value
// can become a filename.
func TestLogsRejectsUnsupportedSource(t *testing.T) {
	for _, source := range []string{"", "lxc.log", "../../etc/passwd", "LOG", "qemu.log", "default"} {
		client := stateClient()
		_, err := NewSystemManager(client).Logs(context.Background(), "production", "api", source)
		assert.EqualError(t, err, "unsupported log source", "source %q", source)
		for _, action := range client.actions {
			assert.False(t, strings.HasPrefix(action, "logfile ") || strings.HasPrefix(action, "console "),
				"source %q must be rejected before any read, got %q", source, action)
		}
	}
}

func TestLogsValidatesNameAndProject(t *testing.T) {
	manager := NewSystemManager(stateClient())

	_, err := manager.Logs(context.Background(), "production", "../default/api", SourceConsole)
	assert.EqualError(t, err, "invalid instance name")

	_, err = manager.Logs(context.Background(), "missing", "api", SourceConsole)
	assert.EqualError(t, err, "project is not available")
}

func TestLogsPropagatesReadFailure(t *testing.T) {
	client := stateClient()
	client.consoleError = errors.New("console unavailable")
	_, err := NewSystemManager(client).Logs(context.Background(), "production", "api", SourceConsole)
	assert.EqualError(t, err, "console unavailable")
}

func TestLogsEmptySourceYieldsNoLines(t *testing.T) {
	client := stateClient()
	client.consoleLog = ""
	logs, err := NewSystemManager(client).Logs(context.Background(), "production", "api", SourceConsole)
	require.NoError(t, err)
	assert.Empty(t, logs.Lines)
}

// TestReadLinesKeepsBoundedTail covers the three bounds the reader has to
// hold at once: the line count, the total byte cap, and an oversized single
// line being dropped whole rather than truncated.
func TestReadLinesKeepsBoundedTail(t *testing.T) {
	var builder strings.Builder
	for index := range 500 {
		builder.WriteString(strings.Repeat("x", 10))
		builder.WriteString(string(rune('a' + index%26)))
		builder.WriteString("\n")
	}
	lines := readLines(strings.NewReader(builder.String()))
	require.Len(t, lines, logsTailLines, "only the last %d lines are kept", logsTailLines)
	assert.Equal(t, strings.Repeat("x", 10)+string(rune('a'+499%26)), lines[len(lines)-1].Message,
		"the tail keeps the newest line, not the oldest")

	total := 0
	for _, line := range lines {
		total += len(line.Message)
	}
	assert.LessOrEqual(t, total, logsMaxBytes)

	oversized := strings.Repeat("y", logsMaxBytes+1) + "\nkept\n"
	lines = readLines(strings.NewReader(oversized))
	assert.Equal(t, []LogLine{{Message: "kept"}}, lines, "an over-cap line is dropped whole, not truncated")

	assert.Equal(t, []LogLine{{Message: "no trailing newline"}}, readLines(strings.NewReader("no trailing newline")))
	assert.Empty(t, readLines(strings.NewReader("")))
}

func TestReadLinesTrimsCarriageReturns(t *testing.T) {
	assert.Equal(t, []LogLine{{Message: "windows"}, {Message: "unix"}}, readLines(strings.NewReader("windows\r\nunix\n")))
}

func TestSourceHelpers(t *testing.T) {
	assert.True(t, validSource(SourceConsole))
	assert.True(t, validSource(SourceLog))
	assert.False(t, validSource("other"))

	assert.Equal(t, SourceLog, sourceAlternate(SourceConsole))
	assert.Equal(t, SourceConsole, sourceAlternate(SourceLog))

	assert.Equal(t, "qemu.log", logfileName("virtual-machine"))
	assert.Equal(t, "lxc.log", logfileName("container"))
	// Only the exact virtual-machine type reads qemu.log; anything else,
	// including an unset type, falls back to the container logfile.
	assert.Equal(t, "lxc.log", logfileName(""))
	assert.Equal(t, "lxc.log", logfileName("Virtual machine"))
}
