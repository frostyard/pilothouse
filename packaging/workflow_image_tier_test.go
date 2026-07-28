package packaging

import (
	"bytes"
	"io"
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
	With map[string]string `yaml:"with"`
}

type imageTierJob struct {
	Name           string            `yaml:"name"`
	RunsOn         string            `yaml:"runs-on"`
	If             yaml.Node         `yaml:"if"`
	Needs          yaml.Node         `yaml:"needs"`
	Permissions    map[string]string `yaml:"permissions"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Steps          []imageTierStep   `yaml:"steps"`
}

type imageTierWorkflow struct {
	Name        string            `yaml:"name"`
	Permissions map[string]string `yaml:"permissions"`
	On          struct {
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
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&workflow),
		"the image workflow may contain only the explicitly reviewed keys")
	var trailingDocument yaml.Node
	require.ErrorIs(t, decoder.Decode(&trailingDocument), io.EOF,
		"the image workflow must contain exactly one YAML document")
	return workflow, string(raw)
}

func TestImageTierWorkflowTriggerAndJobBoundary(t *testing.T) {
	t.Parallel()

	workflow, _ := loadImageTierWorkflow(t)
	require.Equal(t, "Image tier", workflow.Name)
	require.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions,
		"the workflow's complete permission map must remain read-only and minimal")
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
	require.Empty(t, job.Permissions,
		"the job must not override the workflow's exact read-only permission map")

	var condition string
	require.NoError(t, job.If.Decode(&condition))
	require.Equal(t, imageTierLabelGate, strings.TrimSpace(condition))
}

func TestImageTierWorkflowRunsOneRootOwnedOrchestrator(t *testing.T) {
	t.Parallel()

	workflow, _ := loadImageTierWorkflow(t)
	job := workflow.Jobs[imageTierJobName]

	require.Equal(t, []imageTierStep{
		{
			Name: "Checkout code",
			Uses: "actions/checkout@v7",
		},
		{
			Name: "Set up Go",
			Uses: "actions/setup-go@v6",
			With: map[string]string{"go-version": "stable"},
		},
		{
			Name: "Install cosign",
			Uses: "sigstore/cosign-installer@v4.1.2",
		},
		{
			Name: "Enable KVM acceleration",
			Run: "echo 'KERNEL==\"kvm\", GROUP=\"kvm\", MODE=\"0666\", OPTIONS+=\"static_node=kvm\"' \\\n" +
				"  | sudo tee /etc/udev/rules.d/99-kvm4all.rules\n" +
				"sudo udevadm control --reload-rules\n" +
				"sudo udevadm trigger --name-match=kvm\n",
		},
		{
			Name: "Install QEMU and OVMF",
			Run: "sudo apt-get update\n" +
				"sudo apt-get install -y ovmf qemu-system-x86\n",
		},
		{
			Name: "Verify the private-store tool contract",
			Run: "podman --version\n" +
				"podman --help | grep -F -- '--imagestore'\n" +
				"skopeo --version\n" +
				"cosign version\n" +
				"test -r /dev/kvm\n",
		},
		{
			Name: "Compose, boot, validate and remove the uCore fixtures",
			Env:  map[string]string{"GITHUB_TOKEN": "${{ github.token }}"},
			Run: "sudo -n env \"PATH=$PATH\" \"GITHUB_TOKEN=$GITHUB_TOKEN\" \"RUNNER_TEMP=$RUNNER_TEMP\" \\\n" +
				"  bash test/image/ucore-image-test.sh \\\n" +
				"  --run-id \"${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}\"\n",
		},
	}, job.Steps,
		"the complete ordered workflow step set and every executable body are closed")
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
