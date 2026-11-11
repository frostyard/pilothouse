package workflowcheck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// arm64PortsWorkflows lists every workflow that adds the ports.ubuntu.com arm64
// source to a runner.
var arm64PortsWorkflows = []string{"ci.yml", "packaging.yml", "release.yml", "snapshot.yml"}

const (
	// arm64PortsAssignment reads the runner's Ubuntu release, which the ports
	// source must use as its suite.
	arm64PortsAssignment = `codename="$(. /etc/os-release && printf '%s' "$VERSION_CODENAME")"`
	arm64PortsSources    = "/etc/apt/sources.list.d/arm64-ports.sources"
	arm64PortsTeePrefix  = "sudo tee " + arm64PortsSources
	arm64PortsSuites     = "Suites: ${codename} ${codename}-updates ${codename}-security"
	// arm64PortsRunner is the one runner image whose ports archive and glibc
	// the cross-built binaries are made for.
	arm64PortsRunner = "ubuntu-24.04"
)

// TestArm64PortsSuiteFollowsRunnerRelease guards the arm64 ports source: its
// Suites must come from the runner's own Ubuntu release. A hard-coded suite
// makes apt resolve the arm64 packages against a different release than the
// runner's amd64 ones, and the versioned dependencies then collide
// (`libpam0g:amd64 Breaks libpam0g:arm64`), which fails the arm64 build.
//
// Each workflow is decoded first and only the `run` text of the one step that
// writes the source file is checked, so a commented-out or neighbouring line
// cannot satisfy the guard
// (docs/design/engineering-lessons.md#scope-guard-assertions-to-the-exact-region-under-test).
func TestArm64PortsSuiteFollowsRunnerRelease(t *testing.T) {
	for _, name := range arm64PortsWorkflows {
		path := filepath.Join("..", "..", ".github", "workflows", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		var workflow testWorkflow
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatalf("decode %s as YAML: %v", path, err)
		}

		type portsStep struct {
			job  string
			run  string
			runs string
		}
		var found []portsStep
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if strings.Contains(step.Run, arm64PortsSources) {
					found = append(found, portsStep{job: jobName, run: step.Run, runs: job.RunsOn})
				}
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s: found %d steps whose run text names %s, want exactly 1", name, len(found), arm64PortsSources)
		}
		if err := checkArm64PortsRun(found[0].run); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		// The ports archive follows the runner's release and the cgo binaries
		// link against its glibc, so the job must pin the image the ports
		// source and the release targets were chosen for.
		if found[0].runs != arm64PortsRunner {
			t.Errorf("%s: job %q runs on %q, want the pinned %q: an unpinned runner moves the ports suite and the glibc under the release",
				name, found[0].job, found[0].runs, arm64PortsRunner)
		}
	}

	t.Logf("checked %d workflow files for the arm64 ports source", len(arm64PortsWorkflows))
}

// checkArm64PortsRun checks one step's `run` text: it must assign the runner's
// codename on an executable line before writing the ports source, and the
// heredoc that becomes the source file must carry that codename in every suite.
func checkArm64PortsRun(run string) error {
	var problems []error

	lines := strings.Split(run, "\n")
	assignment, tee := -1, -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		// A shell comment is inert: only executable lines can set the suite.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if assignment < 0 && trimmed == arm64PortsAssignment {
			assignment = index
		}
		if tee < 0 && strings.HasPrefix(trimmed, arm64PortsTeePrefix) {
			tee = index
		}
	}

	switch {
	case assignment < 0:
		problems = append(problems, fmt.Errorf("no executable line assigns codename from VERSION_CODENAME (%s), so the ports suite would be empty", arm64PortsAssignment))
	case tee < 0:
		problems = append(problems, fmt.Errorf("no executable line writes %s", arm64PortsSources))
	case assignment > tee:
		problems = append(problems, fmt.Errorf("codename is assigned on line %d, after the line that writes the source file on line %d", assignment+1, tee+1))
	}

	if tee < 0 {
		return errors.Join(problems...)
	}

	if trimmed := strings.TrimSpace(lines[tee]); !strings.HasSuffix(trimmed, "<<EOF") {
		problems = append(problems, fmt.Errorf("the %s heredoc must end with an unquoted <<EOF so ${codename} expands, got %q", arm64PortsSources, trimmed))
	}

	body, err := heredocBody(lines, tee)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	suites := false
	for number, line := range body {
		trimmed := strings.TrimSpace(line)
		if trimmed == arm64PortsSuites {
			suites = true
			continue
		}
		if !strings.HasPrefix(trimmed, "Suites:") {
			continue
		}
		for _, suite := range strings.Fields(strings.TrimPrefix(trimmed, "Suites:")) {
			if suite == "${codename}" || strings.HasPrefix(suite, "${codename}-") {
				continue
			}
			problems = append(problems, fmt.Errorf("line %d of the source file has the literal suite %q; every suite must follow the runner's Ubuntu release", tee+number+2, suite))
		}
	}
	if !suites {
		problems = append(problems, fmt.Errorf("the source file has no %q line", arm64PortsSuites))
	}

	return errors.Join(problems...)
}

// heredocBody returns the lines between the heredoc opener on line index and
// its EOF terminator.
func heredocBody(lines []string, index int) ([]string, error) {
	for end := index + 1; end < len(lines); end++ {
		if strings.TrimSpace(lines[end]) == "EOF" {
			return lines[index+1 : end], nil
		}
	}
	return nil, fmt.Errorf("the heredoc opened for %s has no EOF terminator", arm64PortsSources)
}

func TestCheckArm64PortsRun(t *testing.T) {
	const valid = `sudo dpkg --add-architecture arm64
sudo sed -i 's|^Types: deb|Types: deb\nArchitectures: amd64|' /etc/apt/sources.list.d/ubuntu.sources
# The ports suite must match the runner's Ubuntu release, or apt
# mixes amd64 and arm64 package versions.
codename="$(. /etc/os-release && printf '%s' "$VERSION_CODENAME")"
sudo tee /etc/apt/sources.list.d/arm64-ports.sources >/dev/null <<EOF
Types: deb
URIs: http://ports.ubuntu.com/ubuntu-ports
Suites: ${codename} ${codename}-updates ${codename}-security
Components: main universe
Architectures: arm64
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF
sudo apt-get update`

	withoutAssignment := strings.Replace(valid, arm64PortsAssignment+"\n", "", 1)

	cases := []struct {
		name string
		run  string
		want string
	}{
		{
			name: "codename from the runner release",
			run:  valid,
		},
		{
			name: "assignment commented out",
			run:  strings.Replace(valid, arm64PortsAssignment, "# "+arm64PortsAssignment, 1),
			want: "no executable line assigns codename",
		},
		{
			name: "assignment missing from this step",
			run:  withoutAssignment,
			want: "no executable line assigns codename",
		},
		{
			name: "assignment after the source file is written",
			run:  strings.Replace(withoutAssignment, "sudo apt-get update", arm64PortsAssignment+"\nsudo apt-get update", 1),
			want: "after the line that writes the source file",
		},
		{
			name: "quoted heredoc delimiter",
			run:  strings.Replace(valid, "<<EOF", "<<'EOF'", 1),
			want: "unquoted <<EOF",
		},
		{
			name: "literal noble suite",
			run:  strings.Replace(valid, arm64PortsSuites, "Suites: noble noble-updates noble-security", 1),
			want: `literal suite "noble"`,
		},
		{
			name: "source file written by no step",
			run:  strings.Replace(valid, arm64PortsTeePrefix, "sudo tee /etc/apt/sources.list.d/other.sources", 1),
			want: "no executable line writes",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := checkArm64PortsRun(testCase.run)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("checker rejected a valid step: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checker accepted a step it must reject: want an error containing %q", testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not contain %q", err, testCase.want)
			}
		})
	}
}
