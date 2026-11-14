package packaging

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// workflowsDir is the repository's workflow directory, relative to this test
// package's directory.
const workflowsDir = "../.github/workflows"

// labelTriggerAllowList names the only workflows that may list `labeled` in
// `pull_request` or `pull_request_target` types, each with its reason. It is
// hand-written: GitHub cannot filter a label trigger by label name, so every
// label starts a run of the whole workflow (#263, #266). A workflow that must
// react to one label gets a caller job in vm-boot-label.yml instead.
var labelTriggerAllowList = map[string]string{
	"vm-boot-label.yml": "it exists to start label-only work; each job gates on the label name, " +
		"so another label leaves only skipped runs under the caller's own check names",
	"risk-paths.yml": "PR triage itself must react to labels (ready-for-human, the level labels)",
}

// labelTriggerEvents are the events whose `types` may list `labeled`.
var labelTriggerEvents = []string{"pull_request", "pull_request_target"}

// workflowListsLabeled reports the events of an `on:` node whose types list
// `labeled`. It handles `on` given as a string, a list or a map. The string and
// list forms use GitHub's default types (opened, synchronize, reopened), which
// never include `labeled`.
func workflowListsLabeled(t *testing.T, file string, on yaml.Node) []string {
	t.Helper()

	switch on.Kind {
	case 0, yaml.ScalarNode, yaml.SequenceNode:
		return nil
	case yaml.MappingNode:
	default:
		require.Failf(t, "unexpected `on:` shape", "%s: `on:` has YAML kind %d", file, on.Kind)
	}

	var events map[string]yaml.Node
	require.NoErrorf(t, on.Decode(&events), "%s: decode `on:` as a map", file)

	var found []string
	for _, name := range labelTriggerEvents {
		event, ok := events[name]
		if !ok || event.Kind != yaml.MappingNode {
			continue
		}
		var filter map[string]yaml.Node
		require.NoErrorf(t, event.Decode(&filter), "%s: decode on.%s", file, name)
		typesNode, ok := filter["types"]
		if !ok {
			continue
		}
		var types []string
		switch typesNode.Kind {
		case yaml.ScalarNode:
			var single string
			require.NoErrorf(t, typesNode.Decode(&single), "%s: decode on.%s.types", file, name)
			types = []string{single}
		case yaml.SequenceNode:
			require.NoErrorf(t, typesNode.Decode(&types), "%s: decode on.%s.types", file, name)
		default:
			require.Failf(t, "unexpected types shape", "%s: on.%s.types has YAML kind %d", file, name, typesNode.Kind)
		}
		if slices.Contains(types, "labeled") {
			found = append(found, name)
		}
	}
	return found
}

func TestWorkflowsListLabeledOnlyWhenAllowed(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(workflowsDir)
	require.NoErrorf(t, err, "read %s", workflowsDir)

	var checked []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		checked = append(checked, name)

		raw, err := os.ReadFile(filepath.Join(workflowsDir, name))
		require.NoErrorf(t, err, "read %s", name)

		var workflow struct {
			On yaml.Node `yaml:"on"`
		}
		require.NoErrorf(t, yaml.Unmarshal(raw, &workflow), "decode %s as YAML", name)

		events := workflowListsLabeled(t, name, workflow.On)
		if _, allowed := labelTriggerAllowList[name]; allowed {
			continue
		}
		require.Emptyf(t, events,
			"%s lists `labeled` in %v types: every label would start a run of the whole workflow (#263, #266); "+
				"start label-only work from a caller job in .github/workflows/vm-boot-label.yml instead",
			name, events)
	}

	// The guard must see the workflows it exists for, and every allow-list
	// entry must name a workflow that still exists.
	for _, want := range []string{"image-tier.yml", "packaging.yml"} {
		require.Containsf(t, checked, want, "the label-trigger guard must check %s", want)
	}
	for name := range labelTriggerAllowList {
		require.Containsf(t, checked, name, "allow-list entry %s names no workflow", name)
	}
}

func TestWorkflowListsLabeledHandlesEveryOnShape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		on   string
		want []string
	}{
		{name: "string", on: "on: pull_request\n"},
		{name: "list", on: "on: [push, pull_request]\n"},
		{name: "null event", on: "on:\n  pull_request:\n"},
		{name: "map without labeled", on: "on:\n  pull_request:\n    types: [opened, synchronize]\n"},
		{name: "map with labeled", on: "on:\n  pull_request:\n    types: [opened, labeled]\n", want: []string{"pull_request"}},
		{name: "scalar types", on: "on:\n  pull_request_target:\n    types: labeled\n", want: []string{"pull_request_target"}},
		{name: "push only", on: "on:\n  push:\n    branches: [main]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var workflow struct {
				On yaml.Node `yaml:"on"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(tc.on), &workflow))
			require.Equal(t, tc.want, workflowListsLabeled(t, tc.name, workflow.On))
		})
	}
}
