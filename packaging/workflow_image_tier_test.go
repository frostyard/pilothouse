package packaging

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	imageTierWorkflowPath = "../.github/workflows/image-tier.yml"
	imageTierJobName      = "ucore-vm"
	imageTierLabelGate    = "github.event_name != 'pull_request' || " +
		"contains(github.event.pull_request.labels.*.name, 'vm-boot')"
)

type imageTierStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

type imageTierJob struct {
	Name           string          `yaml:"name"`
	RunsOn         string          `yaml:"runs-on"`
	If             yaml.Node       `yaml:"if"`
	Needs          yaml.Node       `yaml:"needs"`
	TimeoutMinutes int             `yaml:"timeout-minutes"`
	Steps          []imageTierStep `yaml:"steps"`
}

type imageTierWorkflow struct {
	Name        string `yaml:"name"`
	Permissions struct {
		Contents string `yaml:"contents"`
	} `yaml:"permissions"`
	On struct {
		Push struct {
			Branches []string  `yaml:"branches"`
			Tags     yaml.Node `yaml:"tags"`
		} `yaml:"push"`
		PullRequest struct {
			Branches []string `yaml:"branches"`
			Types    []string `yaml:"types"`
		} `yaml:"pull_request"`
	} `yaml:"on"`
	Jobs map[string]imageTierJob `yaml:"jobs"`
}

func loadImageTierWorkflow(t *testing.T) (imageTierWorkflow, string) {
	t.Helper()

	raw, err := os.ReadFile(imageTierWorkflowPath)
	require.NoError(t, err)

	var workflow imageTierWorkflow
	require.NoError(t, yaml.Unmarshal(raw, &workflow))
	return workflow, string(raw)
}

func TestImageTierWorkflowTriggerAndJobBoundary(t *testing.T) {
	t.Parallel()

	workflow, _ := loadImageTierWorkflow(t)
	require.Equal(t, "Image tier", workflow.Name)
	require.Equal(t, "read", workflow.Permissions.Contents)
	require.Equal(t, []string{"main"}, workflow.On.Push.Branches)
	require.Zerof(t, workflow.On.Push.Tags.Kind,
		"the image tier must not run again for a tag already tested on main")
	require.Equal(t, []string{"main"}, workflow.On.PullRequest.Branches)
	require.Equal(t,
		[]string{"opened", "synchronize", "reopened", "labeled"},
		workflow.On.PullRequest.Types,
	)
	require.Len(t, workflow.Jobs, 1,
		"the image workflow owns one complete lifecycle, not independently scheduled phases")

	job, ok := workflow.Jobs[imageTierJobName]
	require.True(t, ok)
	require.Equal(t, "Boot uCore and validate the last released RPM", job.Name)
	require.Equal(t, "ubuntu-26.04", job.RunsOn,
		"Podman 5's --imagestore support is required for the private-store contract")
	require.Equal(t, 180, job.TimeoutMinutes)
	require.Zerof(t, job.Needs.Kind,
		"the image tier tests the last release and must not consume this branch's package build")

	var condition string
	require.NoError(t, job.If.Decode(&condition))
	require.Equal(t, imageTierLabelGate, strings.TrimSpace(condition))
}

func TestImageTierWorkflowRunsOneRootOwnedOrchestrator(t *testing.T) {
	t.Parallel()

	workflow, _ := loadImageTierWorkflow(t)
	job := workflow.Jobs[imageTierJobName]

	var orchestratorSteps int
	var kvmSteps int
	var toolContractSteps int
	var uses []string
	for _, step := range job.Steps {
		if step.Uses != "" {
			uses = append(uses, step.Uses)
		}
		if strings.Contains(step.Run, "99-kvm4all.rules") {
			kvmSteps++
			require.Contains(t, step.Run, "udevadm control --reload-rules")
			require.Contains(t, step.Run, "udevadm trigger --name-match=kvm")
		}
		if strings.Contains(step.Run, "podman --help") {
			toolContractSteps++
			require.Contains(t, step.Run, "--imagestore")
			require.Contains(t, step.Run, "test -r /dev/kvm")
		}
		if !strings.Contains(step.Run, "ucore-image-test.sh") {
			continue
		}
		orchestratorSteps++
		require.Contains(t, step.Run,
			`sudo -n env "PATH=$PATH" "GITHUB_TOKEN=$GITHUB_TOKEN" "RUNNER_TEMP=$RUNNER_TEMP"`)
		require.Contains(t, step.Run, "bash test/image/ucore-image-test.sh")
		require.Contains(t, step.Run, `--run-id "${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"`)
		require.Equal(t, "${{ github.token }}", step.Env["GITHUB_TOKEN"])
	}

	require.Equal(t, []string{
		"actions/checkout@v7",
		"actions/setup-go@v6",
		"sigstore/cosign-installer@v4.1.2",
	}, uses)
	require.Equal(t, 1, kvmSteps)
	require.Equal(t, 1, toolContractSteps)
	require.Equal(t, 1, orchestratorSteps)
}

func TestImageTierWorkflowPublishesAndRetainsNothing(t *testing.T) {
	t.Parallel()

	workflow, raw := loadImageTierWorkflow(t)
	job := workflow.Jobs[imageTierJobName]

	secretReference := regexp.MustCompile(`\bsecrets\s*[.\[:]`)
	require.NotRegexp(t, secretReference, raw)
	require.NotContains(t, raw, "upload-artifact")
	require.NotContains(t, raw, "download-artifact")
	require.NotContains(t, raw, "goreleaser")

	for _, step := range job.Steps {
		for _, forbidden := range []string{
			"docker push", "podman push", "skopeo copy", "gh release upload",
		} {
			require.NotContainsf(t, step.Run, forbidden,
				"the image tier must not publish or upload fixtures (step %q)", step.Name)
		}
	}
}
