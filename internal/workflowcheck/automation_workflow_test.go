package workflowcheck

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

type automationStep struct {
	ID    string            `yaml:"id"`
	If    string            `yaml:"if"`
	Shell string            `yaml:"shell"`
	Env   map[string]string `yaml:"env"`
	Run   string            `yaml:"run"`
}

type automationJob struct {
	If             string            `yaml:"if"`
	Needs          string            `yaml:"needs"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Env            map[string]string `yaml:"env"`
	Outputs        map[string]string `yaml:"outputs"`
	Steps          []automationStep  `yaml:"steps"`
}

type automationWorkflow struct {
	Jobs map[string]automationJob `yaml:"jobs"`
}

func loadAutomationWorkflow(t *testing.T, name string) automationWorkflow {
	t.Helper()

	path := filepath.Join("..", "..", ".github", "workflows", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var workflow automationWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("decode %s as YAML: %v", path, err)
	}
	return workflow
}

func findAutomationStep(t *testing.T, job automationJob, id string) automationStep {
	t.Helper()

	for _, step := range job.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("job does not contain step %q", id)
	return automationStep{}
}
