package workflowcheck

import (
	"os"
	"strings"
	"testing"
)

func TestPublicMetricsIndexIsSubstantive(t *testing.T) {
	info, err := os.Stat("../../docs/metrics")
	if err != nil {
		t.Fatalf("stat public metrics directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("docs/metrics must be a directory")
	}

	data, err := os.ReadFile("../../docs/metrics/README.md")
	if err != nil {
		t.Fatalf("read public metrics index: %v", err)
	}
	contents := string(data)
	for _, required := range []string{
		"# Public metrics",
		"## Signal index",
		"## Pull-request acceptance rate",
		"## Publication and privacy contract",
		"https://github.com/frostyard/pilothouse/actions",
		"https://app.codecov.io/gh/frostyard/pilothouse",
		"PR acceptance rate = merged PRs / closed PRs × 100",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("public metrics index is missing %q", required)
		}
	}
}
