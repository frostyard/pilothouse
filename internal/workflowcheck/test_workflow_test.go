package workflowcheck

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type testWorkflowStep struct {
	Run string `yaml:"run"`
}

type testWorkflowJob struct {
	Permissions map[string]string  `yaml:"permissions"`
	Steps       []testWorkflowStep `yaml:"steps"`
}

type testWorkflow struct {
	Permissions yaml.Node                  `yaml:"permissions"`
	Jobs        map[string]testWorkflowJob `yaml:"jobs"`
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
}
