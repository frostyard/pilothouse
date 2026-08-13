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

	var workflow struct {
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress bool   `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse snapshot workflow: %v", err)
	}
	if workflow.Concurrency.Group != "goreleaser-nightly" {
		t.Errorf("concurrency group = %q, want goreleaser-nightly", workflow.Concurrency.Group)
	}
	if !workflow.Concurrency.CancelInProgress {
		t.Error("cancel-in-progress = false, want true")
	}
}
