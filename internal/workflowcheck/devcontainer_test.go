package workflowcheck

import (
	"os"
	"strings"
	"testing"
)

func TestDevcontainerToolInstallsArePinned(t *testing.T) {
	data, err := os.ReadFile("../../.devcontainer/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(data)

	for _, want := range []string{
		"ARG GOLANGCI_LINT_VERSION=v2.11.4",
		"ARG GOVULNCHECK_VERSION=v1.6.0",
		"github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}",
		"golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf(".devcontainer/Dockerfile must contain %q", want)
		}
	}

	for _, forbidden := range []string{"raw.githubusercontent.com", "/HEAD/", "@latest"} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf(".devcontainer/Dockerfile must not contain floating or unverified tool source %q", forbidden)
		}
	}
}
