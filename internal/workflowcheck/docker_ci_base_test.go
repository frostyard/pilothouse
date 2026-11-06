package workflowcheck

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDockerCIPassesBase guards the container gate: `make docker-ci BASE=<ref>`
// must hand BASE to the `make ci` it runs inside the development image, or the
// corrections append-only comparison silently skips there (ADR 0014).
func TestDockerCIPassesBase(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	const sentinel = "sentinel-base-ref"
	cmd := exec.Command("make", "-n", "--no-print-directory", "docker-ci", "DOCKER=docker", "BASE="+sentinel)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n docker-ci: %v\n%s", err, out)
	}
	var runLine string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "docker run ") {
			runLine = line
		}
	}
	if runLine == "" {
		t.Fatalf("make -n docker-ci printed no `docker run` line:\n%s", out)
	}
	if !strings.HasSuffix(runLine, ` make ci BASE="`+sentinel+`"`) {
		t.Errorf("docker-ci does not pass BASE into the container; its run line ends: %q", runLine[max(0, len(runLine)-60):])
	}
}
