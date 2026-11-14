package packaging

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// vmBootLabelWorkflowPath is the caller that starts the packaging gate and the
// image tier when the `vm-boot` label goes on a pull request, relative to this test package's
// directory. It is read from the working tree, like packagingWorkflowPath.
const vmBootLabelWorkflowPath = "../.github/workflows/vm-boot-label.yml"

// vmBootLabelJobName is the caller job that calls packaging.yml (#263).
const vmBootLabelJobName = "packaging"

// vmBootLabelImageTierJobName is the caller job that calls image-tier.yml
// (#266).
const vmBootLabelImageTierJobName = "image-tier"

// vmBootLabelGate is the exact `if:` of every caller job, hand-written.
const vmBootLabelGate = "github.event.label.name == 'vm-boot'"

// The types below model only the parts of the caller this guard asserts on.
// Each field that must be absent is a yaml.Node, so a present but empty key
// (Kind 0 means absent) is told apart from a missing one.

type vmBootLabelWorkflow struct {
	On          map[string]yaml.Node  `yaml:"on"`
	Permissions map[string]string     `yaml:"permissions"`
	Jobs        map[string]vmBootCall `yaml:"jobs"`
}

type vmBootCall struct {
	Name        string            `yaml:"name"`
	If          yaml.Node         `yaml:"if"`
	Uses        string            `yaml:"uses"`
	Secrets     yaml.Node         `yaml:"secrets"`
	Permissions map[string]string `yaml:"permissions"`
	RunsOn      yaml.Node         `yaml:"runs-on"`
	Steps       yaml.Node         `yaml:"steps"`
}

// wantVMBootLabelEvent is the caller's only event, hand-written. `labeled` on
// its own is the point of the file: the label event starts a run here, and
// nowhere else.
var wantVMBootLabelEvent = map[string][]string{
	"types":    {"labeled"},
	"branches": {"main"},
}

// wantVMBootLabelSecrets is the complete `secrets:` mapping of the caller:
// GORELEASER_KEY passed by name, never `inherit`.
var wantVMBootLabelSecrets = map[string]string{
	"GORELEASER_KEY": "${{ secrets.GORELEASER_KEY }}",
}

func loadVMBootLabelWorkflow(t *testing.T) vmBootLabelWorkflow {
	t.Helper()

	raw, err := os.ReadFile(vmBootLabelWorkflowPath)
	require.NoErrorf(t, err, "read %s", vmBootLabelWorkflowPath)

	var workflow vmBootLabelWorkflow
	require.NoErrorf(t, yaml.Unmarshal(raw, &workflow), "decode %s as YAML", vmBootLabelWorkflowPath)

	return workflow
}

func TestVMBootLabelWorkflowTriggersOnlyOnLabeledMain(t *testing.T) {
	t.Parallel()

	workflow := loadVMBootLabelWorkflow(t)

	require.ElementsMatchf(t, []string{"pull_request"}, mapKeys(workflow.On),
		"%s's `on:` must have exactly one event, pull_request (#263)", vmBootLabelWorkflowPath)

	var event map[string]yaml.Node
	decodeNodeAt(t, workflow.On, "pull_request", &event)
	require.ElementsMatchf(t, []string{"types", "branches"}, mapKeys(event),
		"pull_request must set exactly types and branches, nothing else")

	var types []string
	decodeNodeAt(t, event, "types", &types)
	require.Equalf(t, wantVMBootLabelEvent["types"], types,
		"pull_request.types must be exactly [labeled]: this caller exists to start the gate when a label goes on")

	var branches []string
	decodeNodeAt(t, event, "branches", &branches)
	require.Equalf(t, wantVMBootLabelEvent["branches"], branches,
		"pull_request.branches must be exactly [main]")
}

func TestVMBootLabelWorkflowPermissionsAreReadOnly(t *testing.T) {
	t.Parallel()

	workflow := loadVMBootLabelWorkflow(t)

	require.Equalf(t, map[string]string{"contents": "read"}, workflow.Permissions,
		"the workflow-level permissions must be exactly contents: read")
}

func TestVMBootLabelWorkflowCallsPackagingOnlyForTheVMBootLabel(t *testing.T) {
	t.Parallel()

	workflow := loadVMBootLabelWorkflow(t)

	require.ElementsMatchf(t, []string{vmBootLabelJobName, vmBootLabelImageTierJobName}, mapKeys(workflow.Jobs),
		"%s must have exactly the jobs %q and %q: any other job would run on every label (#263, #266)",
		vmBootLabelWorkflowPath, vmBootLabelJobName, vmBootLabelImageTierJobName)

	job := workflow.Jobs[vmBootLabelJobName]

	require.Equalf(t, "vm-boot packaging", job.Name,
		"the caller job's check name must be %q: a skipped run of that name is what a label other than vm-boot leaves behind",
		"vm-boot packaging")

	// A label other than vm-boot must start no packaging job. The job's `if:`
	// is the only gate, so its exact text is pinned, not matched by substring.
	var condition string
	require.NotZerof(t, job.If.Kind, "the caller job must gate on the label in its `if:`")
	require.NoError(t, job.If.Decode(&condition))
	require.Equalf(t, vmBootLabelGate, condition,
		"the caller's `if:` must be exactly the vm-boot label check: a label other than vm-boot must skip the job (#263)")

	require.Equalf(t, "./.github/workflows/packaging.yml", job.Uses,
		"the caller must call exactly .github/workflows/packaging.yml, a reusable workflow in this repository")

	// `inherit` would pass every secret of the repository to the callee. An
	// explicit mapping passes only what the callee declares.
	require.NotEqualf(t, yaml.ScalarNode, job.Secrets.Kind,
		"the caller must pass secrets by name; `secrets: inherit` would hand the callee every repository secret")
	require.Equalf(t, yaml.MappingNode, job.Secrets.Kind,
		"the caller's `secrets:` must be a mapping with exactly GORELEASER_KEY")

	var secrets map[string]string
	require.NoError(t, job.Secrets.Decode(&secrets))
	require.Equalf(t, wantVMBootLabelSecrets, secrets,
		"the caller's `secrets:` must map exactly one entry, GORELEASER_KEY: ${{ secrets.GORELEASER_KEY }}")

	require.Equalf(t, map[string]string{"contents": "read"}, job.Permissions,
		"the caller job's permissions must be exactly contents: read")

	require.Zerof(t, job.Steps.Kind,
		"the caller job must have no steps: it only calls packaging.yml, so it runs nothing itself")
	require.Zerof(t, job.RunsOn.Kind,
		"the caller job must have no runs-on: a called workflow runs its own jobs on their own runners")
}

func TestVMBootLabelWorkflowCallsImageTierOnlyForTheVMBootLabel(t *testing.T) {
	t.Parallel()

	workflow := loadVMBootLabelWorkflow(t)

	job, ok := workflow.Jobs[vmBootLabelImageTierJobName]
	require.Truef(t, ok, "%s must have the job %q, which starts the image tier for the vm-boot label (#266)",
		vmBootLabelWorkflowPath, vmBootLabelImageTierJobName)

	require.Equalf(t, "vm-boot image tier", job.Name,
		"the caller job's check name must be %q: a skipped run of that name is what a label other than vm-boot leaves behind",
		"vm-boot image tier")

	// A label other than vm-boot must start no ucore-vm job. The job's `if:`
	// is the only gate, so its exact text is pinned, not matched by substring.
	var condition string
	require.NotZerof(t, job.If.Kind, "the image-tier caller job must gate on the label in its `if:`")
	require.NoError(t, job.If.Decode(&condition))
	require.Equalf(t, vmBootLabelGate, condition,
		"the image-tier caller's `if:` must be exactly the vm-boot label check: a label other than vm-boot must skip the job (#266)")

	require.Equalf(t, "./.github/workflows/image-tier.yml", job.Uses,
		"the caller must call exactly .github/workflows/image-tier.yml, a reusable workflow in this repository")

	// The image tier uses only github.token, so the call passes no secret,
	// neither by name nor by `inherit`.
	require.Zerof(t, job.Secrets.Kind,
		"the image-tier caller job must have no `secrets:` key: the image tier needs no repository secret")

	require.Equalf(t, map[string]string{"contents": "read"}, job.Permissions,
		"the image-tier caller job's permissions must be exactly contents: read")

	require.Zerof(t, job.Steps.Kind,
		"the image-tier caller job must have no steps: it only calls image-tier.yml, so it runs nothing itself")
	require.Zerof(t, job.RunsOn.Kind,
		"the image-tier caller job must have no runs-on: a called workflow runs its own jobs on their own runners")
}
