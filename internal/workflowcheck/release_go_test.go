package workflowcheck

import (
	"os"
	"path/filepath"
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

// TestReleaseWorkflowsUseGoMod guards the Go toolchain of the release and the
// snapshot workflow: their actions/setup-go step must read the version from
// go.mod through go-version-file. The `stable` alias moves with the newest Go
// release, so it can build a published binary with a toolchain other than the
// one the repository pins.
func TestReleaseWorkflowsUseGoMod(t *testing.T) {
	for _, target := range releaseGoTargets {
		path := filepath.Join("..", "..", ".github", "workflows", target.workflow)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", target.workflow, err)
		}

		var workflow testWorkflow
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatalf("decode %s as YAML: %v", target.workflow, err)
		}

		job, ok := workflow.Jobs[target.job]
		if !ok {
			t.Fatalf("%s: job %q not found", target.workflow, target.job)
		}

		var setup []testWorkflowStep
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/setup-go@") {
				setup = append(setup, step)
			}
		}
		if len(setup) != 1 {
			t.Fatalf("%s job %s: %d actions/setup-go steps, want exactly 1", target.workflow, target.job, len(setup))
		}

		with := setup[0].With
		if got := strings.TrimSpace(with["go-version-file"]); got != "go.mod" {
			t.Errorf("%s job %s: go-version-file = %q, want go.mod", target.workflow, target.job, got)
		}
		if got := strings.TrimSpace(with["go-version"]); got != "" {
			t.Errorf("%s job %s: go-version = %q, want no override", target.workflow, target.job, got)
		}
	}
}
