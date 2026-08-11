package workflowcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	claudeActionSHA = "6b082c41935b4c8a3b8b0ef85ba4ba4d9eeb8975"
	claudeTools     = "mcp__github_inline_comment__create_inline_comment,Bash(gh pr comment:*),Bash(gh pr diff:*),Bash(gh pr view:*)"
)

func readWorkflowContractFile(t *testing.T, path ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(path...), err)
	}
	return string(data)
}

func TestClaudeReviewWorkflowIsPinnedAndLeastPrivilege(t *testing.T) {
	workflow := readWorkflowContractFile(t, "..", "..", ".github", "workflows", "claude-code-review.yml")

	for _, required := range []string{
		"  pull_request:\n",
		"permissions: {}",
		"github.event.pull_request.draft == false",
		"github.event.pull_request.head.repo.full_name == github.repository",
		"timeout-minutes: 10",
		"      contents: read",
		"      pull-requests: write",
		"CONFIGURED: ${{ secrets.ANTHROPIC_API_KEY != '' }}",
		"::warning::ANTHROPIC_API_KEY is not configured; skipping Claude code review",
		"persist-credentials: false",
		"anthropics/claude-code-action@" + claudeActionSHA,
		"anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}",
		`--allowedTools "` + claudeTools + `"`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("Claude review workflow does not contain %q", required)
		}
	}

	for _, forbidden := range []string{
		"pull_request_target",
		"contents: write",
		"issues: write",
		"actions: write",
		"id-token: write",
		"anthropics/claude-code-action@v",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("Claude review workflow contains forbidden contract %q", forbidden)
		}
	}
}

func TestClaudeReviewWorkflowSkipsWithoutAPIKey(t *testing.T) {
	workflow := loadAutomationWorkflow(t, "claude-code-review.yml")
	review, ok := workflow.Jobs["review"]
	if !ok {
		t.Fatal("Claude review workflow must declare a review job")
	}

	configuration := findAutomationStep(t, review, "configuration")
	if configuration.Shell != "bash" {
		t.Errorf("configuration shell = %q, want bash", configuration.Shell)
	}
	if got := configuration.Env["CONFIGURED"]; got != "${{ secrets.ANTHROPIC_API_KEY != '' }}" {
		t.Errorf("configuration source = %q, want ANTHROPIC_API_KEY availability", got)
	}

	for _, tc := range []struct {
		name       string
		configured string
		wantOutput string
		wantWarn   bool
	}{
		{name: "missing key", configured: "false", wantOutput: "enabled=false", wantWarn: true},
		{name: "configured key", configured: "true", wantOutput: "enabled=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "github-output")
			cmd := exec.Command("bash", "-c", configuration.Run)
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"CONFIGURED=" + tc.configured,
				"GITHUB_OUTPUT=" + outputPath,
			}
			log, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("configuration script failed: %v\n%s", err, log)
			}
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read configuration output: %v", err)
			}
			if got := strings.TrimSpace(string(output)); got != tc.wantOutput {
				t.Errorf("configuration output = %q, want %q", got, tc.wantOutput)
			}
			hasWarning := strings.Contains(string(log), "::warning::ANTHROPIC_API_KEY is not configured; skipping Claude code review")
			if hasWarning != tc.wantWarn {
				t.Errorf("configuration warning present = %v, want %v; log: %q", hasWarning, tc.wantWarn, log)
			}
		})
	}

	const enabled = "steps.configuration.outputs.enabled == 'true'"
	if len(review.Steps) != 3 {
		t.Fatalf("review steps = %d, want configuration, checkout, and review", len(review.Steps))
	}
	for _, step := range review.Steps[1:] {
		if step.If != enabled {
			t.Errorf("step condition = %q, want %q", step.If, enabled)
		}
	}
}

func TestClaudeReviewDocumentationNamesTrustBoundary(t *testing.T) {
	documentation := readWorkflowContractFile(t, "..", "..", "docs", "claude-code-review.md")
	documentation = strings.Join(strings.Fields(documentation), " ")

	for _, required := range []string{
		"ANTHROPIC_API_KEY",
		"Fork pull requests are deliberately skipped",
		"pull_request_target",
		"contents: read",
		"pull-requests: write",
		"warning",
		"skips checkout and review",
		"comments are advisory and require human verification",
	} {
		if !strings.Contains(documentation, required) {
			t.Errorf("Claude review documentation does not contain %q", required)
		}
	}
}
