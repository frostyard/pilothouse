package workflowcheck

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSnapshotWorkflowUsesRollingDevConcurrency(t *testing.T) {
	const path = "../../.github/workflows/snapshot.yml"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot workflow: %v", err)
	}

	type concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress bool   `yaml:"cancel-in-progress"`
	}
	var workflow struct {
		Concurrency *concurrency `yaml:"concurrency"`
		Jobs        struct {
			Snapshot struct {
				If          string      `yaml:"if"`
				Concurrency concurrency `yaml:"concurrency"`
			} `yaml:"snapshot"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse snapshot workflow: %v", err)
	}
	if workflow.Concurrency != nil {
		t.Error("workflow-level concurrency lets unsuccessful upstream runs cancel a release")
	}
	if workflow.Jobs.Snapshot.If != "${{ github.event.workflow_run.conclusion == 'success' }}" {
		t.Errorf("snapshot job condition = %q, want successful upstream run guard", workflow.Jobs.Snapshot.If)
	}
	if workflow.Jobs.Snapshot.Concurrency.Group != "goreleaser-nightly" {
		t.Errorf("snapshot concurrency group = %q, want goreleaser-nightly", workflow.Jobs.Snapshot.Concurrency.Group)
	}
	if !workflow.Jobs.Snapshot.Concurrency.CancelInProgress {
		t.Error("cancel-in-progress = false, want true")
	}
}
