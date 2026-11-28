package workflowcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// releaseGoTargets names the release workflow and the job in it that builds
// and publishes with GoReleaser.
var releaseGoTargets = []struct {
	workflow string
	job      string
}{
	{workflow: "release.yml", job: "goreleaser"},
	{workflow: "snapshot.yml", job: "snapshot"},
}

// releaseWorkflow decodes only what the Go toolchain gate inspects: env at
// every level and every job and step. Env decodes to map[string]string so an
// empty value stays distinguishable from an absent key, which catches
// `GOTOOLCHAIN: ""`, `go-version: ""` and `go-version-file: ""`.
type releaseWorkflow struct {
	Env  map[string]string             `yaml:"env"`
	Jobs map[string]releaseWorkflowJob `yaml:"jobs"`
}

type releaseWorkflowJob struct {
	Env   map[string]string     `yaml:"env"`
	Steps []releaseWorkflowStep `yaml:"steps"`
}

type releaseWorkflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

// TestReleaseWorkflowsUseGoMod guards the Go toolchain of the release and the
// snapshot workflow. The goreleaser and snapshot jobs must exist and each keep
// exactly one actions/setup-go step. Every setup-go step in both workflows must
// read the version from go.mod through go-version-file and must carry no
// go-version key at all. The `stable` alias moves with the newest Go release,
// so it can build a published binary with a toolchain other than the one the
// repository pins.
//
// Every env block in both workflows — workflow, job and step, including steps
// that do not use setup-go — must leave GOTOOLCHAIN unset or set it to the
// literal local. An empty value, or an expression that resolves later, could
// otherwise select a different Go release than go.mod pins.
func TestReleaseWorkflowsUseGoMod(t *testing.T) {
	for _, target := range releaseGoTargets {
		path := filepath.Join("..", "..", ".github", "workflows", target.workflow)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", target.workflow, err)
		}

		var workflow releaseWorkflow
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatalf("decode %s as YAML: %v", target.workflow, err)
		}

		required, ok := workflow.Jobs[target.job]
		if !ok {
			t.Fatalf("%s: job %q not found", target.workflow, target.job)
		}

		var setup int
		for _, step := range required.Steps {
			if isSetupGoStep(step.Uses) {
				setup++
			}
		}
		if setup != 1 {
			t.Fatalf("%s job %s: %d actions/setup-go steps, want exactly 1", target.workflow, target.job, setup)
		}

		checkReleaseGOTOOLCHAIN(t, target.workflow, "workflow env", workflow.Env)

		jobs := make([]string, 0, len(workflow.Jobs))
		for name := range workflow.Jobs {
			jobs = append(jobs, name)
		}
		sort.Strings(jobs)
		for _, name := range jobs {
			job := workflow.Jobs[name]
			jobWhere := fmt.Sprintf("job %q", name)
			checkReleaseGOTOOLCHAIN(t, target.workflow, jobWhere+" env", job.Env)

			for i, step := range job.Steps {
				where := fmt.Sprintf("%s step %d", jobWhere, i+1)
				switch {
				case step.Name != "":
					where += fmt.Sprintf(" %q", step.Name)
				case step.Uses != "":
					where += fmt.Sprintf(" %q", step.Uses)
				}

				checkReleaseGOTOOLCHAIN(t, target.workflow, where+" env", step.Env)
				if isSetupGoStep(step.Uses) {
					checkReleaseGoSetup(t, target.workflow, where, step.With)
				}
			}
		}
	}
}

// checkReleaseGOTOOLCHAIN rejects any GOTOOLCHAIN value other than the literal
// local. An absent key does not override the runner’s toolchain policy.
func checkReleaseGOTOOLCHAIN(t *testing.T, workflow, where string, env map[string]string) {
	t.Helper()

	value, present := env["GOTOOLCHAIN"]
	if !present {
		return
	}
	if value != "local" {
		t.Errorf("%s %s: GOTOOLCHAIN = %q, want the literal local", workflow, where, value)
	}
}

// checkReleaseGoSetup requires a setup-go step to read the version from go.mod
// and to omit the competing go-version input entirely.
func checkReleaseGoSetup(t *testing.T, workflow, where string, with map[string]string) {
	t.Helper()

	if got := with["go-version-file"]; got != "go.mod" {
		t.Errorf("%s %s: go-version-file = %q, want go.mod", workflow, where, got)
	}
	if got, present := with["go-version"]; present {
		t.Errorf("%s %s: go-version = %q, want no go-version key", workflow, where, got)
	}
}

// isSetupGoStep reports whether uses names the actions/setup-go action at any
// ref, pinned or not.
func isSetupGoStep(uses string) bool {
	action, _, _ := strings.Cut(uses, "@")
	return action == "actions/setup-go"
}
