package workflowcheck

import (
	"fmt"
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
		if err := validateGoTestCommand(command); err != nil {
			t.Errorf(".github/workflows/test.yml: %v", err)
		}
	}
	for _, command := range makeCommands {
		if err := validateGoTestCommand(command); err != nil {
			t.Errorf("Makefile: %v", err)
		}
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
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "run:") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "run:"))
		}
		if strings.Contains(trimmed, "go test ") {
			commands = append(commands, trimmed)
		}
	}
	return commands
}

func validateGoTestCommand(command string) error {
	fields := strings.Fields(command)
	commandIndex := -1
	for i := 0; i+1 < len(fields); i++ {
		if fields[i+1] == "test" && (fields[i] == "go" || fields[i] == "$(GO)") {
			commandIndex = i
			break
		}
	}
	if commandIndex < 0 {
		return fmt.Errorf("command does not contain go test or $(GO) test: %s", command)
	}

	for i := 0; i < commandIndex; i++ {
		field := fields[i]
		if field == "run:" {
			continue
		}
		equals := strings.Index(field, "=")
		if equals <= 0 {
			return fmt.Errorf("field before go test must be a NAME=value assignment, got %q: %s", field, command)
		}
	}

	allowedFlags := map[string]bool{
		"v": true, "race": true, "short": true,
		"coverprofile": true, "covermode": true, "tags": true,
	}

	var packagePatterns []string
	var tagValue string
	var tagCount int
	skipNextValue := false
	for i := commandIndex + 2; i < len(fields); i++ {
		field := fields[i]
		if skipNextValue {
			skipNextValue = false
			continue
		}
		if !strings.HasPrefix(field, "-") {
			packagePatterns = append(packagePatterns, field)
			continue
		}

		name, value, hasEquals := normalizeTestFlag(field)
		if !allowedFlags[name] {
			return fmt.Errorf("go test command has forbidden flag name %q (from %q): %s", name, field, command)
		}

		if name == "tags" {
			tagCount++
			if tagCount != 1 {
				return fmt.Errorf("go test command must have exactly one -tags flag: %s", command)
			}
			if hasEquals {
				tagValue = value
			} else {
				if i+1 >= len(fields) {
					return fmt.Errorf("go test command has -tags without a value: %s", command)
				}
				tagValue = fields[i+1]
				skipNextValue = true
			}
			if tagValue != "sdjournal" {
				return fmt.Errorf("go test command has unsupported -tags value %q: %s", tagValue, command)
			}
			continue
		}

		if (name == "coverprofile" || name == "covermode") && !hasEquals {
			if i+1 >= len(fields) {
				return fmt.Errorf("go test command has -%s without a value: %s", name, command)
			}
			skipNextValue = true
		}
	}

	if len(packagePatterns) != 1 {
		return fmt.Errorf("go test command must have exactly 1 package pattern, found %d: %s", len(packagePatterns), command)
	}
	if tagCount == 0 {
		if packagePatterns[0] != "./..." {
			return fmt.Errorf("go test command package pattern must be exactly ./..., got %q: %s", packagePatterns[0], command)
		}
		return nil
	}
	if packagePatterns[0] != "./internal/modules/logs/journal/..." {
		return fmt.Errorf("sdjournal go test command package pattern must be exactly ./internal/modules/logs/journal/...: %s", command)
	}
	return nil
}

func normalizeTestFlag(field string) (name string, value string, hasEquals bool) {
	name = field
	if strings.HasPrefix(name, "--") {
		name = strings.TrimPrefix(name, "--")
	} else if strings.HasPrefix(name, "-") {
		name = strings.TrimPrefix(name, "-")
	}
	if equals := strings.Index(name, "="); equals >= 0 {
		value = name[equals+1:]
		name = name[:equals]
		hasEquals = true
	}
	name = strings.TrimPrefix(name, "test.")
	return name, value, hasEquals
}

func TestValidateGoTestCommand(t *testing.T) {
	rejectionCases := []struct {
		name    string
		command string
	}{
		{name: "short run", command: `go test -run "^TestI" ./...`},
		{name: "short run equals", command: `go test -run=Foo ./...`},
		{name: "short skip", command: `go test -skip Integration ./...`},
		{name: "short skip equals", command: `go test -skip=Foo ./...`},
		{name: "test run", command: `go test -test.run Foo ./...`},
		{name: "test run equals", command: `go test -test.run=Foo ./...`},
		{name: "test skip", command: `go test -test.skip Foo ./...`},
		{name: "test skip equals", command: `go test --test.skip=Foo ./...`},
		{name: "test run double hyphen", command: `go test --test.run=Foo ./...`},
		{name: "long run", command: `go test --run Foo ./...`},
		{name: "long run equals", command: `go test --run=Foo ./...`},
		{name: "long skip", command: `go test --skip Foo ./...`},
		{name: "long skip equals", command: `go test --skip=Foo ./...`},
		{name: "list flag", command: `go test -list . ./...`},
		{name: "c flag", command: `go test -c ./...`},
		{name: "count flag", command: `go test -count=0 ./...`},
		{name: "wrong tags", command: `go test -tags foo ./...`},
		{name: "internal package", command: `go test ./internal/...`},
		{name: "command package", command: `go test ./cmd/...`},
		{name: "bare package", command: `go test example.com/package`},
		{name: "two package patterns", command: `go test ./... ./packaging/...`},
		{name: "sdjournal wrong package", command: `go test -tags sdjournal ./...`},
		{name: "other tags", command: `go test -tags other ./...`},
		{name: "tags equals sdjournal wrong package", command: `go test -tags=sdjournal ./...`},
		{name: "no package pattern", command: `go test -short`},
	}
	for _, tc := range rejectionCases {
		t.Run("reject_"+tc.name, func(t *testing.T) {
			if err := validateGoTestCommand(tc.command); err == nil {
				t.Errorf("validateGoTestCommand(%q) succeeded, want an error", tc.command)
			}
		})
	}

	acceptCases := []struct {
		name    string
		command string
	}{
		{name: "test workflow unit", command: `run: go test -v ./... -coverprofile=coverage.out -covermode=atomic`},
		{name: "make race", command: `$(GO) test -race -short ./...`},
		{name: "test workflow sdjournal", command: `go test -v -tags sdjournal ./internal/modules/logs/journal/...`},
		{name: "make sdjournal", command: `$(GO) test -tags sdjournal ./internal/modules/logs/journal/...`},
	}
	for _, tc := range acceptCases {
		t.Run("accept_"+tc.name, func(t *testing.T) {
			if err := validateGoTestCommand(tc.command); err != nil {
				t.Errorf("validateGoTestCommand(%q) error = %v, want nil", tc.command, err)
			}
		})
	}
}

func TestFindGoTestCommandsIgnoresComments(t *testing.T) {
	text := "# go test ./...\n  # go test -run Foo ./...\nrun: go test -v ./...\n"
	commands := findGoTestCommands(text)
	if len(commands) != 1 || commands[0] != "go test -v ./..." {
		t.Errorf("findGoTestCommands() = %#v, want only the real command", commands)
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
