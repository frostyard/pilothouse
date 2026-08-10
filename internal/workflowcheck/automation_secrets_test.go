package workflowcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type automationStep struct {
	ID    string            `yaml:"id"`
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

func runAutomationScript(t *testing.T, script, token, outputPath string) (string, error) {
	t.Helper()

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GITHUB_OUTPUT=" + outputPath,
	}
	if token != "" {
		cmd.Env = append(cmd.Env, "TOKEN="+token)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestAIFixWorkflowSkipsAssignmentWithoutToken(t *testing.T) {
	workflow := loadAutomationWorkflow(t, "ai-fix-requested.yml")

	gate, ok := workflow.Jobs["gate"]
	if !ok {
		t.Fatal("AI fix workflow must declare a gate job")
	}
	wantAdmission := strings.Join(strings.Fields(`
		github.event_name == 'workflow_dispatch' ||
		(github.event.issue.state == 'open' &&
		github.event.label.name == 'ai-fix-requested')
	`), " ")
	if got := strings.Join(strings.Fields(gate.If), " "); got != wantAdmission {
		t.Errorf("gate admission condition = %q, want %q", got, wantAdmission)
	}
	if got := gate.Outputs["enabled"]; got != "${{ steps.check.outputs.enabled }}" {
		t.Errorf("gate enabled output = %q, want check-step output", got)
	}

	check := findAutomationStep(t, gate, "check")
	if check.Shell != "bash" {
		t.Errorf("gate shell = %q, want bash", check.Shell)
	}
	if got := check.Env["TOKEN"]; got != "${{ secrets.COPILOT_ASSIGNMENT_TOKEN }}" {
		t.Errorf("gate token source = %q, want COPILOT_ASSIGNMENT_TOKEN secret", got)
	}

	t.Run("missing token", func(t *testing.T) {
		outputPath := filepath.Join(t.TempDir(), "github-output")
		log, err := runAutomationScript(t, check.Run, "", outputPath)
		if err != nil {
			t.Fatalf("gate script failed: %v\n%s", err, log)
		}
		output, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("read gate output: %v", err)
		}
		if got := strings.TrimSpace(string(output)); got != "enabled=false" {
			t.Errorf("gate output = %q, want enabled=false", got)
		}
		if !strings.Contains(log, "::notice::COPILOT_ASSIGNMENT_TOKEN is not configured; skipping Copilot assignment") {
			t.Errorf("gate log does not explain skip: %q", log)
		}
	})

	t.Run("configured token", func(t *testing.T) {
		outputPath := filepath.Join(t.TempDir(), "github-output")
		log, err := runAutomationScript(t, check.Run, "configured", outputPath)
		if err != nil {
			t.Fatalf("gate script failed: %v\n%s", err, log)
		}
		output, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("read gate output: %v", err)
		}
		if got := strings.TrimSpace(string(output)); got != "enabled=true" {
			t.Errorf("gate output = %q, want enabled=true", got)
		}
	})

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
	if got := assign.Env["GH_TOKEN"]; got != "${{ secrets.COPILOT_ASSIGNMENT_TOKEN }}" {
		t.Errorf("assignment token source = %q, want COPILOT_ASSIGNMENT_TOKEN secret", got)
	}
	if len(assign.Steps) != 1 {
		t.Fatalf("assignment steps = %d, want 1", len(assign.Steps))
	}
	if !strings.Contains(assign.Steps[0].Run, `if [[ -z "${GH_TOKEN:-}" ]]`) {
		t.Error("assignment step must retain its defensive token check")
	}
}

func TestNightlyComplianceReportsMissingAssignmentToken(t *testing.T) {
	workflow := loadAutomationWorkflow(t, "nightly-compliance.yml")

	job, ok := workflow.Jobs["automation-secret-drift"]
	if !ok {
		t.Fatal("nightly compliance must declare an automation secret drift job")
	}
	if job.TimeoutMinutes != 5 {
		t.Errorf("automation secret drift timeout = %d, want 5", job.TimeoutMinutes)
	}
	if len(job.Steps) != 1 {
		t.Fatalf("automation secret drift steps = %d, want 1", len(job.Steps))
	}

	step := job.Steps[0]
	if step.Shell != "bash" {
		t.Errorf("nightly drift shell = %q, want bash", step.Shell)
	}
	if got := step.Env["TOKEN"]; got != "${{ secrets.COPILOT_ASSIGNMENT_TOKEN }}" {
		t.Errorf("nightly token source = %q, want COPILOT_ASSIGNMENT_TOKEN secret", got)
	}

	t.Run("missing token", func(t *testing.T) {
		log, err := runAutomationScript(t, step.Run, "", filepath.Join(t.TempDir(), "unused-output"))
		if err == nil {
			t.Fatal("nightly drift script succeeded without a token")
		}
		if !strings.Contains(log, "::error::COPILOT_ASSIGNMENT_TOKEN is not configured") {
			t.Errorf("nightly drift log does not explain failure: %q", log)
		}
	})

	t.Run("configured token", func(t *testing.T) {
		log, err := runAutomationScript(t, step.Run, "configured", filepath.Join(t.TempDir(), "unused-output"))
		if err != nil {
			t.Fatalf("nightly drift script failed with token: %v\n%s", err, log)
		}
	})
}
