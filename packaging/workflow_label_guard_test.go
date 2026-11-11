package packaging

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This guard covers when the two heavy workflows run on a pull-request label
// event. `labeled` is in both trigger lists so that applying `vm-boot` can
// start a run; without the guard asserted below, EVERY other label starts one
// too. `PR triage` removes `ready-for-human` when a check at the head is not
// green when the label comes, so a `Build and verify packages` run that the
// `ready-for-human` event itself started is still `in_progress` when triage
// reads the checks, and the label is always removed. On a pull request that
// already carries `vm-boot`, an unrelated label would restart image-tier's
// 180-minute job as well.
//
// Both assertions decode the parsed job's `if:` through the same types and
// loaders the neighbouring workflow tests use, so they read that job's own
// condition and never the file's raw text.

// labelEventGuard is the guard both jobs must conjoin: a `labeled` event runs
// the job only when the label it added is `vm-boot`. Written out by hand, not
// read back out of the workflow under test.
const labelEventGuard = "(github.event.action != 'labeled' || github.event.label.name == 'vm-boot')"

// TestHeavyWorkflowJobsRunOnALabelOnlyForVMBOOT pins the label-event guard on
// the `packages` job of packaging.yml and the `ucore-vm` job of image-tier.yml.
// Each keeps its own earlier condition unchanged and adds the guard beside it;
// `github.event.action` is empty on a push event, so push runs are unaffected.
func TestHeavyWorkflowJobsRunOnALabelOnlyForVMBOOT(t *testing.T) {
	t.Parallel()

	packaging, _ := loadPackagingWorkflow(t)
	imageTier, _ := loadImageTierWorkflow(t)

	for _, tc := range []struct {
		name     string
		workflow string
		job      string
		node     yaml.Node
		earlier  string
	}{
		{
			name:     "packages job of packaging.yml",
			workflow: packagingWorkflowPath,
			job:      "packages",
			node:     packaging.Jobs["packages"].If,
			// The fork skip, exactly as it was: fork pull requests never
			// receive GORELEASER_KEY. `install` and `vm-boot` need this job,
			// so they inherit its skip rather than restating it.
			earlier: "github.event_name != 'pull_request' || " +
				"github.event.pull_request.head.repo.full_name == github.repository",
		},
		{
			name:     "ucore-vm job of image-tier.yml",
			workflow: imageTierWorkflowPath,
			job:      imageTierJobName,
			node:     imageTier.Jobs[imageTierJobName].If,
			// The `vm-boot` label PRESENCE gate, so a later push to a
			// labelled pull request still runs the tier.
			earlier: "github.event_name != 'pull_request' || " +
				"contains(github.event.pull_request.labels.*.name, 'vm-boot')",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.NotZerof(t, tc.node.Kind, "%s must carry the label-event guard as its job `if:`", tc.workflow)

			var condition string
			require.NoErrorf(t, tc.node.Decode(&condition), "decode the `if:` of job %q", tc.job)
			condition = strings.TrimSpace(condition)

			// The exact form, so the guard is conjoined (&&) with the earlier
			// condition and not, for example, an alternative to it (||).
			want := "(" + tc.earlier + ") && " + labelEventGuard
			require.Equalf(t, want, condition,
				"job %q of %s must be its earlier condition AND the label-event guard, so any other label skips it",
				tc.job, tc.workflow)
		})
	}
}
