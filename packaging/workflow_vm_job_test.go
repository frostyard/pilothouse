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

// wantPullRequestTypes is the trigger list the label gate needs: the three
// defaults plus `labeled`, so applying the label can start a run.
var wantPullRequestTypes = []string{"opened", "synchronize", "reopened", "labeled"}

// workflowTriggers models only the `on:` block. gopkg.in/yaml.v3 keeps `on` as
// a string key (it does not apply YAML 1.1's boolean aliases), so this decodes
// as written.
type workflowTriggers struct {
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

func TestPackagingWorkflowTriggersAllowTheLabel(t *testing.T) {
	t.Parallel()

	_, raw := loadPackagingWorkflow(t)

	var triggers workflowTriggers
	require.NoErrorf(t, yaml.Unmarshal([]byte(raw), &triggers), "decode %s triggers", packagingWorkflowPath)

	require.Equal(t, wantPullRequestTypes, triggers.On.PullRequest.Types,
		"pull_request.types must be the three defaults plus `labeled`, so applying the vm-boot label can start a run")
	require.Equal(t, []string{"main"}, triggers.On.PullRequest.Branches,
		"the pull_request trigger stays scoped to main")
	require.Equal(t, []string{"main"}, triggers.On.Push.Branches,
		"the push trigger stays scoped to main")
	require.Zerof(t, triggers.On.Push.Tags.Kind,
		"%s must gain no tag trigger: a tag points at a commit main already covered, and satisfying `needs: packages` on tags would add a third GoReleaser build alongside release.yml",
		packagingWorkflowPath)
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
	require.Contains(t, uses, "actions/download-artifact@634f93cb2916e3fdff6788551b99b062d0335ce0",
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

	// Every form a secret can be reached by — `secrets.KEY`, `secrets['KEY']`
	// and a YAML `secrets:` declaration — counts, so the criterion cannot be
	// sidestepped by spelling the reference differently.
	secretReference := regexp.MustCompile(`\bsecrets\s*[.\[:]`)

	require.Lenf(t, secretReference.FindAllString(raw, -1), 1,
		"%s must use exactly one secret — GORELEASER_KEY, in the packages job — and this tier must add none",
		packagingWorkflowPath)
	require.Containsf(t, raw, "${{ secrets.GORELEASER_KEY }}",
		"%s's one secret use is GORELEASER_KEY", packagingWorkflowPath)

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
