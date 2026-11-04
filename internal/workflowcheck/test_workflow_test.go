package workflowcheck

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type testWorkflowStep struct {
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type testWorkflowJob struct {
	Permissions map[string]string  `yaml:"permissions"`
	Steps       []testWorkflowStep `yaml:"steps"`
}

type testWorkflow struct {
	Permissions yaml.Node                  `yaml:"permissions"`
	Jobs        map[string]testWorkflowJob `yaml:"jobs"`
}

func TestTestGatesRunEveryTest(t *testing.T) {
	workflowData, err := os.ReadFile("../../.github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	makeData, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}

	workflowText := string(workflowData)
	makeText := string(makeData)

	workflowCommands := findGoTestCommands(workflowText)
	makeCommands := findGoTestCommands(strings.ReplaceAll(makeText, "$(GO)", "go"))

	for _, command := range workflowCommands {
		checkGoTestCommand(t, ".github/workflows/test.yml", command)
	}
	for _, command := range makeCommands {
		checkGoTestCommand(t, "Makefile", command)
	}

	if len(workflowCommands) < 2 {
		t.Errorf(".github/workflows/test.yml: found %d go test commands, want at least 2", len(workflowCommands))
	}
	if len(makeCommands) < 2 {
		t.Errorf("Makefile: found %d go test commands, want at least 2", len(makeCommands))
	}

	if !strings.Contains(workflowText, "go test -v -tags sdjournal ./internal/modules/logs/journal/...") {
		t.Error(".github/workflows/test.yml: unit-test must run go test -v -tags sdjournal ./internal/modules/logs/journal/...")
	}
	if !strings.Contains(makeText, "$(GO) test -tags sdjournal ./internal/modules/logs/journal/...") {
		t.Error("Makefile test: must run $(GO) test -tags sdjournal ./internal/modules/logs/journal/...")
	}

	lintStart := strings.Index(makeText, "lint: ## Run linter")
	if lintStart == -1 {
		t.Error("Makefile: lint recipe not found")
	} else {
		lintRecipe := makeText[lintStart:]
		if newline := strings.Index(lintRecipe, "\n"); newline >= 0 {
			lintRecipe = lintRecipe[newline+1:]
		}
		if nextTarget := strings.Index(lintRecipe, "\ndocker-image:"); nextTarget >= 0 {
			lintRecipe = lintRecipe[:nextTarget]
		}
		if strings.Contains(strings.ToLower(lintRecipe), "skipping") {
			t.Error("Makefile lint recipe must not silently skip golangci-lint")
		}
		if !strings.Contains(lintRecipe, "exit 1") {
			t.Error("Makefile lint recipe must exit non-zero when golangci-lint is absent")
		}
	}

	t.Logf("go test commands: test.yml=%d Makefile=%d", len(workflowCommands), len(makeCommands))
}

func findGoTestCommands(text string) []string {
	var commands []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "go test ") {
			commands = append(commands, trimmed)
		}
	}
	return commands
}

func checkGoTestCommand(t *testing.T, file string, command string) {
	t.Helper()
	fields := strings.Fields(command)
	for _, field := range fields {
		if field == "-run" || strings.HasPrefix(field, "-run=") {
			t.Errorf("%s: go test command has forbidden -run flag: %s", file, command)
		}
		if field == "-skip" || strings.HasPrefix(field, "-skip=") {
			t.Errorf("%s: go test command has forbidden -skip flag: %s", file, command)
		}
	}
	if strings.Contains(command, "./internal/...") {
		t.Errorf("%s: go test command uses narrow ./internal/... package set: %s", file, command)
	}
}

func TestTestWorkflowUsesLeastPrivilegeAndPinnedScanner(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}

	var workflow testWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse test workflow: %v", err)
	}
	if workflow.Permissions.Kind != yaml.MappingNode || len(workflow.Permissions.Content) != 0 {
		t.Fatalf("top-level permissions = %#v, want an explicit empty map", workflow.Permissions)
	}

	expected := map[string]map[string]string{
		"lint":      {"contents": "read"},
		"security":  {"contents": "read"},
		"unit-test": {"contents": "read", "id-token": "write"},
		"race-test": {"contents": "read"},
		"verify":    {"contents": "read"},
		"build":     {"contents": "read"},
	}
	if len(workflow.Jobs) != len(expected) {
		t.Fatalf("jobs = %d, want exactly %d", len(workflow.Jobs), len(expected))
	}
	for name, want := range expected {
		job, ok := workflow.Jobs[name]
		if !ok {
			t.Errorf("missing job %q", name)
			continue
		}
		if !reflect.DeepEqual(job.Permissions, want) {
			t.Errorf("job %q permissions = %#v, want %#v", name, job.Permissions, want)
		}
	}

	var scannerInstalls []string
	for _, step := range workflow.Jobs["security"].Steps {
		if strings.Contains(step.Run, "go install golang.org/x/vuln/cmd/govulncheck@") {
			scannerInstalls = append(scannerInstalls, step.Run)
		}
	}
	wantInstall := []string{"go install golang.org/x/vuln/cmd/govulncheck@v1.6.0"}
	if !reflect.DeepEqual(scannerInstalls, wantInstall) {
		t.Errorf("govulncheck installs = %#v, want %#v", scannerInstalls, wantInstall)
	}

	var codecovSteps []testWorkflowStep
	for _, step := range workflow.Jobs["unit-test"].Steps {
		if strings.HasPrefix(step.Uses, "codecov/codecov-action@") {
			codecovSteps = append(codecovSteps, step)
		}
	}
	if len(codecovSteps) != 1 {
		t.Fatalf("Codecov steps = %d, want exactly 1", len(codecovSteps))
	}
	if codecovSteps[0].With["use_oidc"] != "true" {
		t.Errorf("Codecov use_oidc = %q, want true", codecovSteps[0].With["use_oidc"])
	}
}
