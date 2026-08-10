package workflowcheck

import (
	"os"
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
		"        run:",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("Claude review workflow contains forbidden contract %q", forbidden)
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
		"comments are advisory and require human verification",
	} {
		if !strings.Contains(documentation, required) {
			t.Errorf("Claude review documentation does not contain %q", required)
		}
	}
}
