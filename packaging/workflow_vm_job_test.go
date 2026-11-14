package packaging

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This guard covers Layer B's CI job: the booted-VM tier of
// .github/workflows/packaging.yml. It reuses loadPackagingWorkflow from
// workflow_install_job_test.go, so both tiers are asserted against the same
// live decode of the same file, and it never executes the harness — that needs
// KVM and a network.

// vmJobName is the key the booted-VM job is registered under.
const vmJobName = "vm-boot"

// wantVMMatrix is written out by hand from the specification, not read back out
// of the workflow: an expectation derived from the file under test would pass
// for whatever that file happened to say. No image is pinned here — unlike the
// container tier, this job boots a cloud image whose pin lives in
// test/vm/images.env, so exactly one pinning site exists.
var wantVMMatrix = []workflowMatrixEntry{
	{Family: "debian", Artifact: "packages-deb"},
	{Family: "fedora", Artifact: "packages-rpm"},
}

// wantVMLabelGate is the job's complete `if:` expression, hand-written. It is
// label PRESENCE, so a push to an already-labelled pull request runs the job
// again, and it is the whole condition: the fork skip is inherited through
// `needs: packages` rather than restated here.
const wantVMLabelGate = "github.event_name != 'pull_request' || " +
	"contains(github.event.pull_request.labels.*.name, 'vm-boot')"

// wantPullRequestTypes is the trigger list of packaging.yml: the three
// defaults, and deliberately NOT `labeled`. A label event must not start the
// packaging jobs (#263): `PR triage` then found `Build and verify packages` in
// progress and removed `ready-for-human`. `vm-boot-label.yml` starts the run
// for the `vm-boot` label instead.
var wantPullRequestTypes = []string{"opened", "synchronize", "reopened"}

// wantTriggerEvents is the exact set of keys under `on:`, hand-written. A new
// event, such as a label trigger or a second pull_request block, fails here.
var wantTriggerEvents = []string{"push", "pull_request", "workflow_call"}

// workflowEvents decodes only the `on:` block, keeping each event as a raw node
// so the test can check the exact event set before it decodes any event.
// gopkg.in/yaml.v3 keeps `on` as a string key (it does not apply YAML 1.1's
// boolean aliases), so this decodes as written.
type workflowEvents struct {
	On map[string]yaml.Node `yaml:"on"`
}

type pushTrigger struct {
	Branches []string  `yaml:"branches"`
	Tags     yaml.Node `yaml:"tags"`
}

type pullRequestTrigger struct {
	Branches []string `yaml:"branches"`
	Types    []string `yaml:"types"`
}

// workflowCallSecret is one entry under `on.workflow_call.secrets`.
type workflowCallSecret struct {
	Required bool `yaml:"required"`
}

// decodeNodeAt decodes the raw node under key into out. A map index is not
// addressable, so Decode cannot be called on it directly.
func decodeNodeAt(t *testing.T, nodes map[string]yaml.Node, key string, out any) {
	t.Helper()

	node, ok := nodes[key]
	require.Truef(t, ok, "the mapping must have the key %q", key)
	require.NoErrorf(t, node.Decode(out), "decode %q", key)
}

// mapKeys returns the keys of a decoded mapping, in no particular order.
func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

// secretUseExpression matches a `${{ ... }}` expression that reads the
// `secrets` context, however it is spelled (`secrets.KEY`, `secrets['KEY']`).
var secretUseExpression = regexp.MustCompile(`\$\{\{[^}]*\bsecrets\b`)

// yamlSecretUse is one scalar that reads the `secrets` context, with the path
// of the key that holds it. The path is structural (for example
// `jobs.packages.steps[].env.GORELEASER_KEY`), so a comment that mentions a
// secret cannot be counted.
type yamlSecretUse struct {
	path  string
	value string
}

// walkYAML calls visit for every node with its dotted key path. Sequence items
// use `[]` in the path. Mapping keys are not visited as nodes.
func walkYAML(n *yaml.Node, path string, visit func(n *yaml.Node, path string)) {
	visit(n, path)

	switch n.Kind {
	case yaml.DocumentNode:
		for _, child := range n.Content {
			walkYAML(child, path, visit)
		}
	case yaml.SequenceNode:
		for _, child := range n.Content {
			walkYAML(child, path+"[]", visit)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			walkYAML(n.Content[i+1], joinYAMLPath(path, n.Content[i].Value), visit)
		}
	}
}

func joinYAMLPath(path, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}

func loadVMJob(t *testing.T) (workflowJob, string) {
	t.Helper()

	workflow, raw := loadPackagingWorkflow(t)

	job, ok := workflow.Jobs[vmJobName]
	require.Truef(t, ok, "%s must declare a %q job", packagingWorkflowPath, vmJobName)

	return job, raw
}

func TestPackagingWorkflowVMJobShape(t *testing.T) {
	t.Parallel()

	job, _ := loadVMJob(t)

	t.Run("runs on the standard runner and needs the packages job", func(t *testing.T) {
		require.Equalf(t, "ubuntu-latest", job.RunsOn,
			"job %q runs on a standard GitHub-hosted runner: no self-hosted and no larger runner", vmJobName)

		var needs []string
		switch job.Needs.Kind {
		case yaml.ScalarNode:
			var single string
			require.NoError(t, job.Needs.Decode(&single))
			needs = []string{single}
		case yaml.SequenceNode:
			require.NoError(t, job.Needs.Decode(&needs))
		default:
			t.Fatalf("job %q must declare `needs`; it declares none", vmJobName)
		}

		require.Equalf(t, []string{"packages"}, needs,
			"job %q must depend on exactly the packages job so it boots the artifacts that job built and verified",
			vmJobName)
	})

	t.Run("matrix is exactly the two families, fail-fast off", func(t *testing.T) {
		require.NotNilf(t, job.Strategy.FailFast, "job %q must set strategy.fail-fast", vmJobName)
		require.Falsef(t, *job.Strategy.FailFast,
			"job %q must set fail-fast: false so one family's failure does not mask the other's result",
			vmJobName)
		require.Equal(t, wantVMMatrix, job.Strategy.Matrix.Include,
			"the VM matrix must be exactly debian/packages-deb and fedora/packages-rpm")
	})
}

func TestPackagingWorkflowVMJobLabelGate(t *testing.T) {
	t.Parallel()

	job, _ := loadVMJob(t)

	var condition string
	require.NotZerof(t, job.If.Kind, "job %q must carry the label gate as its `if:`", vmJobName)
	require.NoError(t, job.If.Decode(&condition))

	require.Equalf(t, wantVMLabelGate, strings.TrimSpace(condition),
		"job %q's `if:` must be precisely the label-presence gate and nothing else", vmJobName)

	require.NotContainsf(t, condition, "head.repo.full_name",
		"job %q must not restate the fork skip: a skipped `packages` job skips its dependents, so the condition is inherited",
		vmJobName)
}

func TestPackagingWorkflowTriggersIgnoreLabelEvents(t *testing.T) {
	t.Parallel()

	_, raw := loadPackagingWorkflow(t)

	var events workflowEvents
	require.NoErrorf(t, yaml.Unmarshal([]byte(raw), &events), "decode %s triggers", packagingWorkflowPath)

	require.ElementsMatchf(t, wantTriggerEvents, mapKeys(events.On),
		"%s's `on:` must have exactly push, pull_request and workflow_call: no label trigger may start the packaging jobs (#263)",
		packagingWorkflowPath)

	var pull pullRequestTrigger
	decodeNodeAt(t, events.On, "pull_request", &pull)
	require.Equalf(t, wantPullRequestTypes, pull.Types,
		"pull_request.types must be exactly opened, synchronize, reopened: a label event must not start the packaging jobs (#263); .github/workflows/vm-boot-label.yml starts the run for the vm-boot label")
	require.NotContainsf(t, pull.Types, "labeled",
		"pull_request.types must not contain `labeled`: each label would start a full packaging run, and PR triage would then remove ready-for-human (#263)")
	require.Equal(t, []string{"main"}, pull.Branches,
		"the pull_request trigger stays scoped to main")

	var push pushTrigger
	decodeNodeAt(t, events.On, "push", &push)
	require.Equal(t, []string{"main"}, push.Branches,
		"the push trigger stays scoped to main")
	require.Zerof(t, push.Tags.Kind,
		"%s must gain no tag trigger: a tag points at a commit main already covered, and satisfying `needs: packages` on tags would add a third GoReleaser build alongside release.yml",
		packagingWorkflowPath)

	var call map[string]yaml.Node
	decodeNodeAt(t, events.On, "workflow_call", &call)
	require.ElementsMatchf(t, []string{"secrets"}, mapKeys(call),
		"workflow_call must declare only `secrets`, and no `inputs`: the caller in vm-boot-label.yml passes none")

	var declared map[string]workflowCallSecret
	decodeNodeAt(t, call, "secrets", &declared)
	require.ElementsMatchf(t, []string{"GORELEASER_KEY"}, mapKeys(declared),
		"workflow_call must declare exactly one secret, GORELEASER_KEY, and no other")
}

func TestPackagingWorkflowVMJobRunsTheHarnessThroughAnInterpreter(t *testing.T) {
	t.Parallel()

	job, _ := loadVMJob(t)

	var harnessSteps int
	for _, step := range job.Steps {
		if !strings.Contains(step.Run, "vm-boot-test.sh") {
			continue
		}
		harnessSteps++

		command := strings.TrimSpace(step.Run)
		require.Truef(t, strings.HasPrefix(command, "bash test/vm/vm-boot-test.sh"),
			"the harness step must invoke the orchestrator through an explicit interpreter (`bash test/vm/vm-boot-test.sh ...`), never as a bare path; got %q",
			command)
		require.Contains(t, command, "--family ${{ matrix.family }}",
			"the harness step must pass --family from the matrix so each row boots its own distro")
		require.Contains(t, command, "--artifact-dir",
			"the harness step must pass --artifact-dir so it installs the downloaded artifact")
	}

	require.Equalf(t, 1, harnessSteps,
		"job %q must run the orchestrator in exactly one step", vmJobName)
}

func TestPackagingWorkflowVMJobBuildsAndPublishesNothing(t *testing.T) {
	t.Parallel()

	job, _ := loadVMJob(t)

	var uses []string
	for _, step := range job.Steps {
		if step.Uses != "" {
			uses = append(uses, step.Uses)
		}
	}

	require.Contains(t, uses, "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"the VM job checks the tree out: the harness comes from the repository, not the artifact")
	require.Contains(t, uses, "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333",
		"the VM job downloads the already-uploaded artifact instead of building a second time")

	for _, ref := range uses {
		require.NotContainsf(t, ref, "goreleaser",
			"the VM job must not build packages; it boots what `packages` uploaded (step uses %s)", ref)
		require.NotContainsf(t, ref, "upload-artifact",
			"the VM job must upload no artifact: disks, seeds and logs never leave the job (step uses %s)", ref)
	}

	for _, step := range job.Steps {
		for _, forbidden := range []string{"docker push", "podman push", "skopeo copy", "ghcr.io"} {
			require.NotContainsf(t, step.Run, forbidden,
				"the VM job must push no image anywhere: it builds, derives and publishes no OS image (step %q)", step.Name)
		}
	}
}

// wantGoReleaserActions is the number of GoReleaser invocations the whole
// workflow may contain: the one in the `packages` job. Hand-written, so adding
// a second build anywhere in the file fails here rather than being absorbed.
const wantGoReleaserActions = 1

func TestPackagingWorkflowHasExactlyOneGoReleaserInvocation(t *testing.T) {
	t.Parallel()

	workflow, raw := loadPackagingWorkflow(t)

	var invocations int
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Uses, "goreleaser") || strings.Contains(step.Run, "goreleaser") {
				invocations++
				require.Equalf(t, "packages", name,
					"only the packages job may build with GoReleaser; job %q step %q does too", name, step.Name)
			}
		}
	}

	require.Equalf(t, wantGoReleaserActions, invocations,
		"%s must invoke GoReleaser exactly once: the booted-VM tier boots the artifact the packages job already built",
		packagingWorkflowPath)

	require.Equalf(t, wantGoReleaserActions, strings.Count(raw, "goreleaser/goreleaser-action@"),
		"%s must reference the GoReleaser action exactly once", packagingWorkflowPath)
}

func TestPackagingWorkflowVMJobDownloadsOnlyTheMatchingFormat(t *testing.T) {
	t.Parallel()

	job, _ := loadVMJob(t)

	_, raw := loadPackagingWorkflow(t)
	require.Containsf(t, raw, "name: ${{ matrix.artifact }}",
		"the VM job must download the matrix row's artifact by name, so a format mis-detection cannot install the wrong file")

	// Neither format may be named outside the matrix: the row itself is what
	// decides which artifact this run installs.
	for _, step := range job.Steps {
		require.NotContains(t, step.Run, "packages-deb")
		require.NotContains(t, step.Run, "packages-rpm")
	}
}

func TestPackagingWorkflowVMJobEnablesKVM(t *testing.T) {
	t.Parallel()

	job, _ := loadVMJob(t)

	var rules bool
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "99-kvm4all.rules") {
			rules = true
			require.Contains(t, step.Run, "udevadm",
				"installing the rule file is not enough: the step must reload and trigger udev so /dev/kvm is usable in this job")
		}

		for _, emulation := range []string{"-accel tcg", "accel=tcg", "qemu-system-i386"} {
			require.NotContainsf(t, step.Run, emulation,
				"the VM job must not fall back to software emulation (step %q)", step.Name)
		}
	}

	require.Truef(t, rules,
		"job %q must install the 99-kvm4all.rules udev rule: hardware acceleration is required, not preferred", vmJobName)
}

func TestPackagingWorkflowVMJobAddsNoSecret(t *testing.T) {
	t.Parallel()

	workflow, raw := loadPackagingWorkflow(t)

	// The file has 2 secret forms, and each is counted on its own, so a new
	// reference in any spelling fails here:
	//   - a use: an expression `${{ secrets.KEY }}` in a step's env, found by
	//     walking the decoded YAML tree (comments are not nodes, so a comment
	//     that mentions a secret is not counted);
	//   - a declaration: the `secrets:` key under `on.workflow_call`, which
	//     exposes GORELEASER_KEY to the caller (vm-boot-label.yml).
	var doc yaml.Node
	require.NoErrorf(t, yaml.Unmarshal([]byte(raw), &doc), "decode %s as a YAML node tree", packagingWorkflowPath)

	var uses []yamlSecretUse
	var declarations []string
	walkYAML(&doc, "", func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "secrets" {
					declarations = append(declarations, joinYAMLPath(path, "secrets"))
				}
			}
		case yaml.ScalarNode:
			if secretUseExpression.MatchString(n.Value) {
				uses = append(uses, yamlSecretUse{path: path, value: n.Value})
			}
		}
	})

	require.Lenf(t, uses, 1,
		"%s must use exactly one secret expression — GORELEASER_KEY, in the packages job — and this tier must add none",
		packagingWorkflowPath)
	require.Equalf(t, "${{ secrets.GORELEASER_KEY }}", uses[0].value,
		"%s's one secret use must be exactly ${{ secrets.GORELEASER_KEY }}", packagingWorkflowPath)
	require.Truef(t, strings.HasPrefix(uses[0].path, "jobs.packages."),
		"the one secret use must sit in the packages job, not at %q", uses[0].path)

	require.Equalf(t, []string{"on.workflow_call.secrets"}, declarations,
		"%s's only `secrets:` declaration must be the workflow_call one that exposes GORELEASER_KEY to the caller",
		packagingWorkflowPath)

	secretReference := regexp.MustCompile(`\bsecrets\s*[.\[:]`)
	for name, job := range workflow.Jobs {
		if name == "packages" {
			continue
		}
		for _, step := range job.Steps {
			require.NotRegexpf(t, secretReference, step.Run,
				"job %q must reference no secret (step %q)", name, step.Name)
		}
	}
}
