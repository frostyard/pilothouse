package workflowcheck

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type automationStep struct {
	ID  string            `yaml:"id"`
	Env map[string]string `yaml:"env"`
	Run string            `yaml:"run"`
}

type automationJob struct {
	If      string            `yaml:"if"`
	Needs   string            `yaml:"needs"`
	Outputs map[string]string `yaml:"outputs"`
	Steps   []automationStep  `yaml:"steps"`
}

type automationWorkflow struct {
	Jobs map[string]automationJob `yaml:"jobs"`
}

func loadAutomationWorkflow(t *testing.T, path string) automationWorkflow {
	t.Helper()

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

func TestAIFixWorkflowSkipsAssignmentWithoutToken(t *testing.T) {
	workflow := loadAutomationWorkflow(t, "../../.github/workflows/ai-fix-requested.yml")

	gate, ok := workflow.Jobs["gate"]
	if !ok {
		t.Fatal("AI fix workflow must declare a gate job")
	}
	if !strings.Contains(gate.If, "github.event.label.name == 'ai-fix-requested'") {
		t.Errorf("gate job must retain the label-event admission check, got %q", gate.If)
	}
	if got := gate.Outputs["enabled"]; got != "${{ steps.check.outputs.enabled }}" {
		t.Errorf("gate enabled output = %q, want check-step output", got)
	}

	var check automationStep
	for _, step := range gate.Steps {
		if step.ID == "check" {
			check = step
			break
		}
	}
	if got := check.Env["TOKEN"]; got != "${{ secrets.COPILOT_ASSIGNMENT_TOKEN }}" {
		t.Errorf("gate token source = %q, want COPILOT_ASSIGNMENT_TOKEN secret", got)
	}
	for _, want := range []string{
		`if [[ -n "${TOKEN:-}" ]]`,
		`echo "enabled=true" >>"$GITHUB_OUTPUT"`,
		`echo "enabled=false" >>"$GITHUB_OUTPUT"`,
		"::notice::COPILOT_ASSIGNMENT_TOKEN is not configured; skipping Copilot assignment",
	} {
		if !strings.Contains(check.Run, want) {
			t.Errorf("gate check must contain %q", want)
		}
	}

	assign, ok := workflow.Jobs["assign-copilot"]
	if !ok {
		t.Fatal("AI fix workflow must declare the assignment job")
	}
	if assign.Needs != "gate" {
		t.Errorf("assignment needs = %q, want gate", assign.Needs)
	}
	if assign.If != "needs.gate.outputs.enabled == 'true'" {
		t.Errorf("assignment condition = %q, want enabled gate output", assign.If)
	}
}

func TestNightlyComplianceReportsMissingAssignmentToken(t *testing.T) {
	workflow := loadAutomationWorkflow(t, "../../.github/workflows/nightly-compliance.yml")

	job, ok := workflow.Jobs["automation-secret-drift"]
	if !ok {
		t.Fatal("nightly compliance must declare an automation secret drift job")
	}
	if len(job.Steps) != 1 {
		t.Fatalf("automation secret drift steps = %d, want 1", len(job.Steps))
	}

	step := job.Steps[0]
	if got := step.Env["TOKEN"]; got != "${{ secrets.COPILOT_ASSIGNMENT_TOKEN }}" {
		t.Errorf("nightly token source = %q, want COPILOT_ASSIGNMENT_TOKEN secret", got)
	}
	for _, want := range []string{
		`if [[ -z "${TOKEN:-}" ]]`,
		"::error::COPILOT_ASSIGNMENT_TOKEN is not configured",
		"exit 1",
	} {
		if !strings.Contains(step.Run, want) {
			t.Errorf("nightly drift check must contain %q", want)
		}
	}
}
