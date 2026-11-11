package workflowcheck

import (
	"strings"
	"testing"
)

// arm64PortsWorkflows lists every workflow that adds the ports.ubuntu.com arm64
// source to a runner.
var arm64PortsWorkflows = []string{"ci.yml", "packaging.yml", "release.yml", "snapshot.yml"}

// TestArm64PortsSuiteFollowsRunnerRelease guards the arm64 ports source: its
// Suites must come from the runner's own Ubuntu release. A hard-coded suite
// makes apt resolve the arm64 packages against a different release than the
// runner's amd64 ones, and the versioned dependencies then collide
// (`libpam0g:amd64 Breaks libpam0g:arm64`), which fails the arm64 build.
func TestArm64PortsSuiteFollowsRunnerRelease(t *testing.T) {
	const wantedSuites = "Suites: ${codename} ${codename}-updates ${codename}-security"

	for _, name := range arm64PortsWorkflows {
		workflow := readWorkflowContractFile(t, "..", "..", ".github", "workflows", name)
		t.Logf("checked %s (%d bytes)", name, len(workflow))

		for _, required := range []string{
			"arm64-ports.sources",
			"VERSION_CODENAME",
			wantedSuites,
		} {
			if !strings.Contains(workflow, required) {
				t.Errorf("%s: does not contain %q", name, required)
			}
		}

		for number, line := range strings.Split(workflow, "\n") {
			trimmed := strings.TrimSpace(line)

			// The heredoc feeds the ports source file, so its delimiter must be
			// unquoted or ${codename} reaches the file unexpanded.
			if strings.Contains(trimmed, "arm64-ports.sources") && !strings.HasSuffix(trimmed, "<<EOF") {
				t.Errorf("%s:%d: arm64-ports.sources heredoc must end with an unquoted <<EOF so ${codename} expands, got %q",
					name, number+1, trimmed)
			}

			if !strings.HasPrefix(trimmed, "Suites:") {
				continue
			}
			for _, suite := range strings.Fields(strings.TrimPrefix(trimmed, "Suites:")) {
				if suite == "${codename}" || strings.HasPrefix(suite, "${codename}-") {
					continue
				}
				t.Errorf("%s:%d: Suites has the literal suite %q; the ports suite must follow the runner's Ubuntu release",
					name, number+1, suite)
			}
		}
	}

	t.Logf("checked %d workflow files for the arm64 ports source", len(arm64PortsWorkflows))
}
