package incus

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const (
	logsTailLines = 200
	logsMaxBytes  = 256 * 1024
)

// Log sources. The broker query accepts exactly these two values and
// nothing else, so no caller-supplied filename ever reaches the Incus API.
const (
	// SourceConsole is the instance's console ring buffer — the boot and
	// console output an operator actually wants to read.
	SourceConsole = "console"
	// SourceLog is the supervisor logfile: lxc.log for a container,
	// qemu.log for a virtual machine, matching what `incus info
	// --show-log` resolves "default" to.
	SourceLog = "log"
)

type LogLine struct {
	Message string `json:"message"`
}

// Logs is the bounded tail of one log source for one instance. A source
// that produced no content — a console ring buffer that was never enabled,
// a supervisor log an instance has not written yet — returns an empty
// Lines rather than an error, matching how the page distinguishes "no
// output" from "could not read".
type Logs struct {
	Lines   []LogLine `json:"lines"`
	Name    string    `json:"name"`
	Project string    `json:"project"`
	Source  string    `json:"source"`
}

func validSource(source string) bool {
	return source == SourceConsole || source == SourceLog
}

// logfileName resolves the supervisor logfile for an instance type, the
// same mapping the Incus CLI applies for `--show-log default`.
func logfileName(instanceType string) string {
	if instanceType == "virtual-machine" {
		return "qemu.log"
	}
	return "lxc.log"
}

// boundedLines keeps the final logsTailLines lines of a stream, capped at
// logsMaxBytes in total, so an unbounded or hostile logfile cannot exhaust
// broker memory. A single line longer than the cap is dropped rather than
// truncated, since a partial line is worse than a missing one.
type boundedLines struct {
	bytes int
	lines []LogLine
}

func newBoundedLines() *boundedLines {
	return &boundedLines{lines: make([]LogLine, 0, logsTailLines)}
}

func (bounded *boundedLines) add(message string) {
	if len(message) > logsMaxBytes {
		return
	}
	bounded.lines = append(bounded.lines, LogLine{Message: message})
	bounded.bytes += len(message)
	for len(bounded.lines) > logsTailLines || bounded.bytes > logsMaxBytes {
		bounded.bytes -= len(bounded.lines[0].Message)
		bounded.lines = bounded.lines[1:]
	}
}

// readLines reads reader to completion, keeping only the bounded tail. A
// line longer than bufio's buffer is accumulated across reads and dropped
// whole once it exceeds the byte cap.
func readLines(reader io.Reader) []LogLine {
	buffered := bufio.NewReader(reader)
	bounded := newBoundedLines()
	var raw strings.Builder
	discard := false
	for {
		fragment, err := buffered.ReadSlice('\n')
		if !discard {
			if raw.Len()+len(fragment) <= logsMaxBytes {
				_, _ = raw.Write(fragment)
			} else {
				raw.Reset()
				discard = true
			}
		}
		if len(fragment) > 0 && fragment[len(fragment)-1] == '\n' {
			if !discard {
				bounded.add(strings.TrimRight(raw.String(), "\r\n"))
			}
			raw.Reset()
			discard = false
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) && !discard && raw.Len() > 0 {
				bounded.add(strings.TrimRight(raw.String(), "\r\n"))
			}
			return bounded.lines
		}
	}
}
