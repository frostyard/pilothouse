package workflowcheck

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The guard for core ADR-0073 (baseline row B12): ci.yml's jobs together run
// every part of `make ci`, so no gate can live in the Makefile alone.

var (
	ciTargetLine   = regexp.MustCompile(`^ci\s*:`)
	ciAssignment   = regexp.MustCompile(`^ci\s*:=`)
	makeCallRecipe = regexp.MustCompile(`^make (\S+)$`)
)

// ciGateGaps reports, in the order the rule states, the parts of the Makefile's
// `ci` target that no workflow `run:` command covers.
func ciGateGaps(makeText, workflowText string) ([]string, error) {
	prerequisites, recipe, err := parseCITarget(makeText)
	if err != nil {
		return nil, err
	}

	commands, err := workflowRunCommands(workflowText)
	if err != nil {
		return nil, err
	}

	targets := make(map[string]bool)
	for _, command := range commands {
		for _, target := range makeTargets(command) {
			targets[target] = true
		}
	}
	// A `make ci` run command covers everything.
	if targets["ci"] {
		return nil, nil
	}

	var gaps []string
	for _, prerequisite := range prerequisites {
		if !targets[prerequisite] {
			gaps = append(gaps, "make "+prerequisite)
		}
	}

	for _, command := range recipe {
		if match := makeCallRecipe.FindStringSubmatch(command); match != nil {
			if !targets[match[1]] {
				gaps = append(gaps, command)
			}
			continue
		}
		covered := false
		for _, workflowCommand := range commands {
			if workflowCommand == command {
				covered = true
				break
			}
		}
		if !covered {
			gaps = append(gaps, command)
		}
	}

	return gaps, nil
}

// parseCITarget returns the prerequisites of the first `ci:` line and its
// recipe commands, with leading `@`/`-` stripped and `$(MAKE)` read as `make`.
func parseCITarget(makeText string) ([]string, []string, error) {
	lines := strings.Split(makeText, "\n")
	start := -1
	for i, line := range lines {
		if ciAssignment.MatchString(line) {
			continue
		}
		if ciTargetLine.MatchString(line) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, nil, errors.New("Makefile: no `ci:` target")
	}

	header := ciTargetLine.ReplaceAllString(lines[start], "")
	if hash := strings.Index(header, "#"); hash >= 0 {
		header = header[:hash]
	}
	prerequisites := strings.Fields(header)

	var recipe []string
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, "\t") {
			break
		}
		command := strings.TrimLeft(trimmed, "@-")
		command = strings.ReplaceAll(command, "$(MAKE)", "make")
		command = strings.TrimSpace(command)
		if command == "" || strings.HasPrefix(command, "#") {
			continue
		}
		recipe = append(recipe, command)
	}

	return prerequisites, recipe, nil
}

// workflowRunCommands lists every command of every `run:` block: one command
// per line, without empty lines and comment lines.
func workflowRunCommands(workflowText string) ([]string, error) {
	var workflow testWorkflow
	if err := yaml.Unmarshal([]byte(workflowText), &workflow); err != nil {
		return nil, fmt.Errorf("ci.yml: parse workflow: %w", err)
	}

	var commands []string
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			for _, line := range strings.Split(step.Run, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				commands = append(commands, trimmed)
			}
		}
	}
	return commands, nil
}

// makeTargets lists the targets one command runs: every segment between shell
// separators whose first word (after leading `NAME=value` assignments) is
// `make`, minus its flags. A `-C`/`-f` call names another makefile or
// directory, so it runs no target of this repository.
func makeTargets(command string) []string {
	segments := strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case '&', '|', ';':
			return true
		}
		return false
	})

	var targets []string
	for _, segment := range segments {
		fields := strings.Fields(segment)
		for len(fields) > 0 && isAssignment(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "make", "$(MAKE)", "${MAKE}":
		default:
			continue
		}

		otherMakefile := false
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-C") || strings.HasPrefix(field, "-f") ||
				strings.HasPrefix(field, "--directory") || strings.HasPrefix(field, "--file") ||
				strings.HasPrefix(field, "--makefile") {
				otherMakefile = true
				break
			}
		}
		if otherMakefile {
			continue
		}

		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-") || strings.Contains(field, "=") {
				continue
			}
			targets = append(targets, field)
		}
	}

	return targets
}

func isAssignment(field string) bool {
	equals := strings.Index(field, "=")
	return equals > 0
}

func readCIGateFiles(t *testing.T) (string, string) {
	t.Helper()
	makeData, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	workflowData, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(makeData), string(workflowData)
}

func TestCIWorkflowRunsEveryPartOfMakeCI(t *testing.T) {
	makeText, workflowText := readCIGateFiles(t)

	gaps, err := ciGateGaps(makeText, workflowText)
	if err != nil {
		t.Fatalf("ciGateGaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("ci.yml does not run every part of `make ci` (core ADR-0073, baseline row B12); uncovered: %s",
			strings.Join(gaps, ", "))
	}

	prerequisites, recipe, err := parseCITarget(makeText)
	if err != nil {
		t.Fatalf("parse Makefile ci target: %v", err)
	}
	if want := []string{"verify"}; !equalStrings(prerequisites, want) {
		t.Errorf("Makefile `ci` prerequisites = %#v, want %#v", prerequisites, want)
	}
	for _, command := range recipe {
		if !makeCallRecipe.MatchString(command) {
			t.Errorf("Makefile ci recipe line %q is not a bare `make <target>` call. Keep every ci recipe line a bare `$(MAKE) <target>` call so ci.yml can run it as `make <target>` (core ADR-0073, baseline row B12); any other line would have to be copied verbatim into ci.yml.", command)
		}
	}
}

func TestCIGateGapsMutations(t *testing.T) {
	makeText, workflowText := readCIGateFiles(t)

	removeOnce := func(t *testing.T, text, old string) string {
		t.Helper()
		if !strings.Contains(text, old) {
			t.Fatalf("mutation text not found: %q", old)
		}
		return strings.Replace(text, old, "", 1)
	}

	cases := []struct {
		name           string
		mutateMake     func(*testing.T, string) string
		mutateWorkflow func(*testing.T, string) string
		wantGap        string
	}{
		{
			name: "make verify step",
			mutateWorkflow: func(t *testing.T, text string) string {
				return removeOnce(t, text, "        run: make verify\n")
			},
			wantGap: "make verify",
		},
		{
			name: "build runs make build",
			mutateWorkflow: func(t *testing.T, text string) string {
				return removeOnce(t, text, "make -o generate build")
			},
			wantGap: "make build",
		},
		{
			name: "corrections step",
			mutateWorkflow: func(t *testing.T, text string) string {
				return removeOnce(t, text, "        run: make corrections-base BASE=\"$BASE\"\n")
			},
			wantGap: "make corrections-base",
		},
		{
			name: "make race step",
			mutateWorkflow: func(t *testing.T, text string) string {
				return removeOnce(t, text, "        run: make race\n")
			},
			wantGap: "make race",
		},
		{
			name: "non-make ci recipe line",
			mutateMake: func(t *testing.T, text string) string {
				removeOnce(t, text, "\t$(MAKE) build\n")
				return strings.Replace(text, "\t$(MAKE) build\n",
					"\t$(MAKE) build\n\t@echo \"all CI gates passed\"\n", 1)
			},
			wantGap: `echo "all CI gates passed"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutatedMake := makeText
			if tc.mutateMake != nil {
				mutatedMake = tc.mutateMake(t, mutatedMake)
			}
			mutatedWorkflow := workflowText
			if tc.mutateWorkflow != nil {
				mutatedWorkflow = tc.mutateWorkflow(t, mutatedWorkflow)
			}

			gaps, err := ciGateGaps(mutatedMake, mutatedWorkflow)
			if err != nil {
				t.Fatalf("ciGateGaps: %v", err)
			}
			for _, gap := range gaps {
				if gap == tc.wantGap {
					return
				}
			}
			t.Errorf("gaps = %#v, want %q after mutation %q", gaps, tc.wantGap, tc.name)
		})
	}
}

func TestCIGateGapsReader(t *testing.T) {
	const makeText = "ci: verify ## doc\n\t$(MAKE) race\n"

	workflow := func(run string) string {
		return "jobs:\n  j:\n    steps:\n      - run: " + run + "\n"
	}

	cases := []struct {
		name         string
		makeText     string
		workflowText string
		wantGaps     []string
	}{
		{
			name:         "comment and step name do not count",
			makeText:     makeText,
			workflowText: "jobs:\n  j:\n    steps:\n      - name: make verify\n        run: \"# make verify\"\n      - run: \"# make race\"\n",
			wantGaps:     []string{"make verify", "make race"},
		},
		{
			name:         "assignment after the command",
			makeText:     makeText,
			workflowText: workflow("make verify GOFLAGS=-x && make race"),
			wantGaps:     nil,
		},
		{
			name:         "assignment before the command",
			makeText:     "ci: race\n\t$(MAKE) verify\n",
			workflowText: workflow("FOO=1 make race && FOO=1 make verify"),
			wantGaps:     nil,
		},
		{
			name:         "other makefile does not count",
			makeText:     "ci: verify\n\t$(MAKE) verify\n",
			workflowText: workflow("make -C other verify"),
			wantGaps:     []string{"make verify", "make verify"},
		},
		{
			name:         "several targets in one segment",
			makeText:     makeText,
			workflowText: workflow("echo hi && make verify race"),
			wantGaps:     nil,
		},
		{
			name:         "make ci covers everything",
			makeText:     "ci: verify build\n\t$(MAKE) test\n\t@echo done\n",
			workflowText: workflow("make ci"),
			wantGaps:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gaps, err := ciGateGaps(tc.makeText, tc.workflowText)
			if err != nil {
				t.Fatalf("ciGateGaps: %v", err)
			}
			if !equalStrings(gaps, tc.wantGaps) {
				t.Errorf("gaps = %#v, want %#v", gaps, tc.wantGaps)
			}
		})
	}

	t.Run("no ci target", func(t *testing.T) {
		if _, err := ciGateGaps("check: verify\n\t$(MAKE) test\n", workflow("make verify test")); err == nil {
			t.Error("ciGateGaps() succeeded without a `ci:` target, want an error")
		}
	})
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
