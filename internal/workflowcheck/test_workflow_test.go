package workflowcheck

import (
	"errors"
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
	workflowData, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	makeData, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}

	workflowText := string(workflowData)
	makeText := string(makeData)

	if err := checkTestGates(makeText, workflowText); err != nil {
		t.Error(err)
	}

	workflowCommands := findWorkflowGoTestCommands(workflowText)
	makeCommands := findMakefileGoTestCommands(makeText)
	makeTargets := parseMakefileTargets(makeText)

	var workflow testWorkflow
	if err := yaml.Unmarshal([]byte(workflowText), &workflow); err != nil {
		t.Fatal(err)
	}

	t.Logf("go test commands: ci.yml=%d Makefile=%d; required targets: test=%d race=%d; unit-test=%d race-test=%d",
		len(workflowCommands), len(makeCommands),
		len(findGoTestCommands(strings.ReplaceAll(strings.Join(makeTargets["test"], "\n"), "$(GO)", "go"))),
		len(findGoTestCommands(strings.ReplaceAll(strings.Join(makeTargets["race"], "\n"), "$(GO)", "go"))),
		len(findJobGoTestCommands(workflow.Jobs["unit-test"])),
		countMakeRaceSteps(workflow.Jobs["race-test"]))
}

// checkTestGates is a pure check of the Makefile and test workflow texts.
func checkTestGates(makeText, workflowText string) error {
	var errs []error

	makeTargets := parseMakefileTargets(makeText)
	for _, command := range findMakefileGoTestCommands(makeText) {
		if err := validateGoTestCommand(command); err != nil {
			errs = append(errs, fmt.Errorf("Makefile: %w", err))
		}
	}

	for _, command := range findGoTestCommands(strings.ReplaceAll(strings.Join(makeTargets["test"], "\n"), "$(GO)", "go")) {
		if err := validateGoTestCommand(command); err != nil {
			errs = append(errs, fmt.Errorf("Makefile test: %w", err))
		}
	}
	for _, command := range findGoTestCommands(strings.ReplaceAll(strings.Join(makeTargets["race"], "\n"), "$(GO)", "go")) {
		if err := validateGoTestCommand(command); err != nil {
			errs = append(errs, fmt.Errorf("Makefile race: %w", err))
		}
	}

	var testFull, testSdjournal, raceFull int
	for _, command := range findGoTestCommands(strings.ReplaceAll(strings.Join(makeTargets["test"], "\n"), "$(GO)", "go")) {
		shape, err := describeGoTestCommand(command)
		if err == nil {
			if shape.packagePattern == "./..." && !shape.hasTags {
				testFull++
			}
			if shape.packagePattern == "./internal/modules/logs/journal/..." && shape.tags == "sdjournal" {
				testSdjournal++
			}
		}
	}
	for _, command := range findGoTestCommands(strings.ReplaceAll(strings.Join(makeTargets["race"], "\n"), "$(GO)", "go")) {
		shape, err := describeGoTestCommand(command)
		if err == nil && shape.packagePattern == "./..." && !shape.hasTags && shape.hasRace {
			raceFull++
		}
	}
	if testFull != 1 {
		errs = append(errs, fmt.Errorf("Makefile test: found %d full-suite go test commands, want exactly 1", testFull))
	}
	if testSdjournal != 1 {
		errs = append(errs, fmt.Errorf("Makefile test: found %d sdjournal go test commands, want exactly 1", testSdjournal))
	}
	if raceFull != 1 {
		errs = append(errs, fmt.Errorf("Makefile race: found %d race go test ./... commands, want exactly 1", raceFull))
	}

	var workflow testWorkflow
	if err := yaml.Unmarshal([]byte(workflowText), &workflow); err != nil {
		errs = append(errs, fmt.Errorf(".github/workflows/ci.yml: parse workflow: %w", err))
		return errors.Join(errs...)
	}

	for _, command := range findWorkflowGoTestCommands(workflowText) {
		if err := validateGoTestCommand(command); err != nil {
			errs = append(errs, fmt.Errorf(".github/workflows/ci.yml: %w", err))
		}
	}

	unitJob, ok := workflow.Jobs["unit-test"]
	if !ok {
		errs = append(errs, errors.New(".github/workflows/ci.yml: missing unit-test job"))
	} else {
		var unitFull, unitSdjournal int
		for _, command := range findJobGoTestCommands(unitJob) {
			shape, err := describeGoTestCommand(command)
			if err == nil {
				if shape.packagePattern == "./..." && !shape.hasTags {
					unitFull++
				}
				if shape.packagePattern == "./internal/modules/logs/journal/..." && shape.tags == "sdjournal" {
					unitSdjournal++
				}
			}
		}
		if unitFull != 1 {
			errs = append(errs, fmt.Errorf(".github/workflows/ci.yml unit-test: found %d full-suite go test commands, want exactly 1", unitFull))
		}
		if unitSdjournal != 1 {
			errs = append(errs, fmt.Errorf(".github/workflows/ci.yml unit-test: found %d sdjournal go test commands, want exactly 1", unitSdjournal))
		}
	}

	raceJob, ok := workflow.Jobs["race-test"]
	if !ok {
		errs = append(errs, errors.New(".github/workflows/ci.yml: missing race-test job"))
	} else if countMakeRaceSteps(raceJob) != 1 {
		errs = append(errs, fmt.Errorf(".github/workflows/ci.yml race-test: found %d make race steps, want exactly 1", countMakeRaceSteps(raceJob)))
	}

	lintStart := strings.Index(makeText, "lint: ## Run linter")
	if lintStart == -1 {
		errs = append(errs, errors.New("Makefile: lint recipe not found"))
	} else {
		lintRecipe := makeText[lintStart:]
		if newline := strings.Index(lintRecipe, "\n"); newline >= 0 {
			lintRecipe = lintRecipe[newline+1:]
		}
		if nextTarget := strings.Index(lintRecipe, "\ndocker-image:"); nextTarget >= 0 {
			lintRecipe = lintRecipe[:nextTarget]
		}
		if strings.Contains(strings.ToLower(lintRecipe), "skipping") {
			errs = append(errs, errors.New("Makefile lint recipe must not silently skip golangci-lint"))
		}
		if !strings.Contains(lintRecipe, "exit 1") {
			errs = append(errs, errors.New("Makefile lint recipe must exit non-zero when golangci-lint is absent"))
		}
	}

	return errors.Join(errs...)
}

func TestTestGatesMutationRemovesRequiredCommands(t *testing.T) {
	makeData, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	workflowData, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	makeText := string(makeData)
	workflowText := string(workflowData)

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
	}{
		{
			name: "race command",
			mutateMake: func(t *testing.T, text string) string {
				return removeOnce(t, text, "\tumask 0077 && $(GO) test -race -short ./...\n")
			},
		},
		{
			name: "test full-suite command",
			mutateMake: func(t *testing.T, text string) string {
				return removeOnce(t, text, "\t$(GO) test ./...\n")
			},
		},
		{
			name: "test sdjournal command",
			mutateMake: func(t *testing.T, text string) string {
				return removeOnce(t, text, "\t$(GO) test -tags sdjournal ./internal/modules/logs/journal/...\n")
			},
		},
		{
			name: "unit-test full-suite command",
			mutateWorkflow: func(t *testing.T, text string) string {
				return removeOnce(t, text, "        run: go test -v ./... -coverprofile=coverage.out -covermode=atomic\n")
			},
		},
		{
			name: "unit-test sdjournal command",
			mutateWorkflow: func(t *testing.T, text string) string {
				return removeOnce(t, text, "        run: go test -v -tags sdjournal ./internal/modules/logs/journal/...\n")
			},
		},
		{
			name: "race-test make race step",
			mutateWorkflow: func(t *testing.T, text string) string {
				return removeOnce(t, text, "      - name: Run tests with race detector\n        run: make race\n")
			},
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
			if err := checkTestGates(mutatedMake, mutatedWorkflow); err == nil {
				t.Errorf("checkTestGates() succeeded after mutation %q, want an error", tc.name)
			}
		})
	}
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

func parseMakefileTargets(makeText string) map[string][]string {
	targets := make(map[string][]string)
	current := ""
	for _, line := range strings.Split(makeText, "\n") {
		if !strings.HasPrefix(line, "\t") {
			current = ""
			trimmed := strings.TrimSpace(line)
			if colon := strings.Index(trimmed, ":"); colon >= 0 {
				name := strings.TrimSpace(trimmed[:colon])
				if name != "" && !strings.ContainsAny(name, " \t") {
					current = name
					targets[current] = nil
				}
			}
			continue
		}
		if current != "" {
			targets[current] = append(targets[current], strings.TrimPrefix(line, "\t"))
		}
	}
	return targets
}

func findMakefileGoTestCommands(makeText string) []string {
	return findGoTestCommands(strings.ReplaceAll(makeText, "$(GO)", "go"))
}

func findWorkflowGoTestCommands(workflowText string) []string {
	var workflow testWorkflow
	if err := yaml.Unmarshal([]byte(workflowText), &workflow); err != nil {
		return nil
	}
	var commands []string
	for _, job := range workflow.Jobs {
		commands = append(commands, findJobGoTestCommands(job)...)
	}
	return commands
}

func findJobGoTestCommands(job testWorkflowJob) []string {
	var commands []string
	for _, step := range job.Steps {
		commands = append(commands, findGoTestCommands(step.Run)...)
	}
	return commands
}

func countMakeRaceSteps(job testWorkflowJob) int {
	count := 0
	for _, step := range job.Steps {
		for _, line := range strings.Split(step.Run, "\n") {
			if strings.TrimSpace(line) == "make race" {
				count++
			}
		}
	}
	return count
}

type goTestCommandShape struct {
	packagePattern string
	tags           string
	hasTags        bool
	hasRace        bool
}

func describeGoTestCommand(command string) (goTestCommandShape, error) {
	if err := validateGoTestCommand(command); err != nil {
		return goTestCommandShape{}, err
	}
	fields := strings.Fields(command)
	commandIndex := -1
	for i := 0; i+1 < len(fields); i++ {
		if fields[i+1] == "test" && (fields[i] == "go" || fields[i] == "$(GO)") {
			commandIndex = i
			break
		}
	}
	shape := goTestCommandShape{}
	skipNextValue := false
	for i := commandIndex + 2; i < len(fields); i++ {
		field := fields[i]
		if skipNextValue {
			skipNextValue = false
			continue
		}
		if !strings.HasPrefix(field, "-") {
			shape.packagePattern = field
			continue
		}
		name, value, hasEquals := normalizeTestFlag(field)
		if name == "race" {
			shape.hasRace = true
		}
		if name == "tags" {
			shape.hasTags = true
			shape.tags = value
			if !hasEquals {
				if i+1 < len(fields) {
					shape.tags = fields[i+1]
					skipNextValue = true
				}
			}
		}
	}
	return shape, nil
}

func isUmaskMode(field string) bool {
	if len(field) != 4 || field[0] != '0' {
		return false
	}
	for _, digit := range field[1:] {
		if digit < '0' || digit > '7' {
			return false
		}
	}
	return true
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

	allowedAssignments := map[string]bool{
		"CGO_ENABLED": true, "GOOS": true, "GOARCH": true,
		"CC": true, "CGO_LDFLAGS": true,
	}
	// make race runs the suite under a fixed umask, so the guard accepts exactly
	// that prefix ("umask 0077 && ") and nothing else before the command.
	firstField := 0
	if commandIndex >= 3 && fields[0] == "umask" && fields[2] == "&&" && isUmaskMode(fields[1]) {
		firstField = 3
	}
	for i := firstField; i < commandIndex; i++ {
		field := fields[i]
		if field == "run:" {
			continue
		}
		equals := strings.Index(field, "=")
		if equals <= 0 {
			return fmt.Errorf("field before go test must be a NAME=value assignment, got %q: %s", field, command)
		}
		name := field[:equals]
		if !allowedAssignments[name] {
			return fmt.Errorf("go test command has forbidden assignment name %q: %s", name, command)
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
		{name: "GOFLAGS run", command: `GOFLAGS=-run=^$ go test ./...`},
		{name: "GOFLAGS count", command: `GOFLAGS=-count=0 go test ./...`},
		{name: "unknown assignment", command: `FOO=bar go test ./...`},
		{name: "short umask mode", command: `umask 77 && go test ./...`},
		{name: "non octal umask mode", command: `umask 0099 && go test ./...`},
		{name: "umask without and", command: `umask 0077 ; go test ./...`},
		{name: "umask with unknown assignment", command: `umask 0077 && FOO=bar go test ./...`},
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
		{name: "make race under umask", command: `umask 0077 && $(GO) test -race -short ./...`},
		{name: "CGO assignment race short", command: `CGO_ENABLED=1 go test -race -short ./...`},
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
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
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
