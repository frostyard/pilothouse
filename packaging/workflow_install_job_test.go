package packaging

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// packagingWorkflowPath is the live CI packaging gate, relative to this test
// package's directory. It is read from the working tree rather than embedded:
// a guard compared against a snapshot could not detect drift at all.
const packagingWorkflowPath = "../.github/workflows/packaging.yml"

// The types below model only the slice of the workflow this guard asserts on.
// Unknown keys are ignored (no yaml.Decoder.KnownFields), so triggers,
// permissions and every other job's contents parse and are discarded.

type workflowStep struct {
	Name string `yaml:"name"`
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

type workflowMatrixEntry struct {
	Family   string `yaml:"family"`
	Image    string `yaml:"image"`
	Artifact string `yaml:"artifact"`
}

type workflowStrategy struct {
	FailFast *bool `yaml:"fail-fast"`
	Matrix   struct {
		Include []workflowMatrixEntry `yaml:"include"`
	} `yaml:"matrix"`
}

type workflowJob struct {
	Name     string           `yaml:"name"`
	RunsOn   string           `yaml:"runs-on"`
	Needs    yaml.Node        `yaml:"needs"`
	If       yaml.Node        `yaml:"if"`
	Strategy workflowStrategy `yaml:"strategy"`
	Steps    []workflowStep   `yaml:"steps"`
}

type packagingWorkflow struct {
	Name string                 `yaml:"name"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

// loadPackagingWorkflow decodes the live workflow. Decoding is itself part of
// the guarantee: a workflow that no longer parses as YAML fails here.
func loadPackagingWorkflow(t *testing.T) (packagingWorkflow, string) {
	t.Helper()

	raw, err := os.ReadFile(packagingWorkflowPath)
	require.NoErrorf(t, err, "read %s", packagingWorkflowPath)

	var workflow packagingWorkflow
	require.NoErrorf(t, yaml.Unmarshal(raw, &workflow), "decode %s as YAML", packagingWorkflowPath)

	return workflow, string(raw)
}

// installJobName is the key the install-validation job is registered under.
const installJobName = "install"

// wantInstallMatrix is written out by hand from the specification, not derived
// from the workflow: an expectation read out of the file under test would pass
// for whatever that file happened to say. Both references are pinned by digest
// because a floating tag makes the gate's result depend on when it ran.
var wantInstallMatrix = []workflowMatrixEntry{
	{
		Family:   "debian",
		Image:    "debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc",
		Artifact: "packages-deb",
	},
	{
		Family:   "fedora",
		Image:    "fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814",
		Artifact: "packages-rpm",
	},
}

func TestPackagingWorkflowInstallJob(t *testing.T) {
	t.Parallel()

	workflow, _ := loadPackagingWorkflow(t)

	job, ok := workflow.Jobs[installJobName]
	require.Truef(t, ok, "%s must declare a %q job", packagingWorkflowPath, installJobName)

	t.Run("needs exactly the packages job", func(t *testing.T) {
		var needs []string
		switch job.Needs.Kind {
		case yaml.ScalarNode:
			var single string
			require.NoError(t, job.Needs.Decode(&single))
			needs = []string{single}
		case yaml.SequenceNode:
			require.NoError(t, job.Needs.Decode(&needs))
		default:
			t.Fatalf("job %q must declare `needs`; it declares none", installJobName)
		}

		require.Equalf(t, []string{"packages"}, needs,
			"job %q must depend on exactly the packages job so it installs the artifacts that job built and verified",
			installJobName)
	})

	t.Run("does not restate the fork skip", func(t *testing.T) {
		require.Zerof(t, job.If.Kind,
			"job %q must declare no `if:` of its own: a skipped `packages` job skips its dependents, so the fork skip is inherited rather than copied",
			installJobName)
	})

	t.Run("matrix pins both families by digest", func(t *testing.T) {
		require.NotNilf(t, job.Strategy.FailFast, "job %q must set strategy.fail-fast", installJobName)
		require.Falsef(t, *job.Strategy.FailFast,
			"job %q must set fail-fast: false so one family's failure does not mask the other's result",
			installJobName)
		require.Equal(t, wantInstallMatrix, job.Strategy.Matrix.Include,
			"the install matrix must be exactly the two digest-pinned images, each paired with its own format's artifact")
	})

	t.Run("checkout and download use the versions already in the file", func(t *testing.T) {
		var uses []string
		for _, step := range job.Steps {
			if step.Uses != "" {
				uses = append(uses, step.Uses)
			}
		}

		require.Contains(t, uses, "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
			"the install job checks the tree out: verify-install.sh and the Makefile come from the repository, not the artifact")
		require.Contains(t, uses, "actions/download-artifact@634f93cb2916e3fdff6788551b99b062d0335ce0",
			"the install job downloads the already-uploaded artifact instead of building a second time")

		for _, ref := range uses {
			require.NotContainsf(t, ref, "goreleaser",
				"the install job must not build packages; it installs what `packages` uploaded (step uses %s)", ref)
		}
	})

	t.Run("runs the same make target a developer runs", func(t *testing.T) {
		var found bool
		for _, step := range job.Steps {
			if !strings.Contains(step.Run, "make verify-package-install") {
				continue
			}
			found = true
			require.Contains(t, step.Run, "INSTALL_IMAGE=",
				"the install step must pass INSTALL_IMAGE; the target has no default image")
			require.Contains(t, step.Run, "matrix.image",
				"INSTALL_IMAGE must come from the matrix so each family installs on its own pinned image")
			require.Contains(t, step.Run, "ARTIFACT_DIR=",
				"the install step must pass ARTIFACT_DIR so it reads the downloaded artifact")
		}
		require.Truef(t, found, "job %q must run `make verify-package-install`", installJobName)
	})
}

func TestPackagingWorkflowHasOneGoReleaserBuild(t *testing.T) {
	t.Parallel()

	workflow, _ := loadPackagingWorkflow(t)

	var builds int
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "goreleaser/goreleaser-action") {
				builds++
			}
		}
	}

	require.Equalf(t, 1, builds,
		"%s must run goreleaser/goreleaser-action exactly once: the install job reuses the uploaded artifacts rather than rebuilding them",
		packagingWorkflowPath)
}

func TestPackagingWorkflowInstallsNoCpio(t *testing.T) {
	t.Parallel()

	_, raw := loadPackagingWorkflow(t)

	require.NotContainsf(t, raw, "cpio",
		"%s must not mention cpio: packaging/extract's rpm backend runs rpm2archive piped into tar, and nothing in this workflow needs cpio",
		packagingWorkflowPath)
}
