package workflowcheck

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var (
	commitActionPattern = regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	dockerActionPattern = regexp.MustCompile(`^docker://[^@]+@sha256:[0-9a-f]{64}$`)

	// actionPin and versionComment are the repository-wide pin check of
	// core ADR-0090: every OWNER/REPO[/PATH]@SHA carries a # vX.Y.Z or
	// # vX.Y comment, and each action has 1 SHA across all files.
	actionPin      = regexp.MustCompile(`^([^@]+)@([0-9a-f]{40})$`)
	versionComment = regexp.MustCompile(`^# v\d+\.\d+(\.\d+)?$`)
)

// corePinPrefix names a frostyard/core reusable workflow. Core has no release
// tags, so its pin needs only a comment (which names the core commit on main).
const corePinPrefix = "frostyard/core/.github/workflows/"

func isImmutableActionReference(ref string) bool {
	return strings.HasPrefix(ref, "./") ||
		commitActionPattern.MatchString(ref) ||
		dockerActionPattern.MatchString(ref)
}

func TestActionReferenceClassification(t *testing.T) {
	sha := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	for _, tc := range []struct {
		name string
		ref  string
		want bool
	}{
		{name: "commit pinned action", ref: "actions/checkout@" + sha, want: true},
		{name: "commit pinned nested action", ref: "frostyard/repogen/.github/actions/publish-to-r2@" + sha, want: true},
		{name: "digest pinned docker action", ref: "docker://alpine@sha256:" + digest, want: true},
		{name: "local action", ref: "./.github/actions/check", want: true},
		{name: "version tag", ref: "actions/checkout@v7", want: false},
		{name: "branch", ref: "frostyard/repogen/.github/actions/publish-to-r2@main", want: false},
		{name: "short commit", ref: "actions/checkout@deadbeef", want: false},
		{name: "expression", ref: "actions/checkout@${{inputs.ref}}", want: false},
		{name: "docker tag", ref: "docker://alpine:3.22", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isImmutableActionReference(tc.ref); got != tc.want {
				t.Errorf("isImmutableActionReference(%q) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}

// pinSHAs maps each action (OWNER/REPO[/PATH]) to its SHAs, and each SHA to
// the first file:line that pins it.
type pinSHAs map[string]map[string]string

// checkPin checks the value node of 1 uses: key (ADR-0090) and records its SHA.
// A value that is not OWNER/REPO[/PATH]@SHA (./local, docker://, or a mutable
// ref) is left to isImmutableActionReference.
func checkPin(path string, value *yaml.Node, shas pinSHAs) error {
	m := actionPin.FindStringSubmatch(value.Value)
	if m == nil || strings.HasPrefix(value.Value, "docker://") {
		return nil
	}
	action, sha := m[1], m[2]
	where := fmt.Sprintf("%s:%d", path, value.Line)
	if shas[action] == nil {
		shas[action] = map[string]string{}
	}
	if _, seen := shas[action][sha]; !seen {
		shas[action][sha] = where
	}
	switch {
	case strings.HasPrefix(action, corePinPrefix):
		if value.LineComment == "" {
			return fmt.Errorf("%s: %s has no comment; name the core commit on main", where, action)
		}
	case !versionComment.MatchString(value.LineComment):
		return fmt.Errorf("%s: %s has comment %q, want # vX.Y.Z or # vX.Y", where, action, value.LineComment)
	}
	return nil
}

// checkOneSHA runs once, after every file is walked: each action has 1 SHA.
func checkOneSHA(shas pinSHAs) []error {
	var problems []error
	for action, set := range shas {
		if len(set) == 1 {
			continue
		}
		var at []string
		for sha, where := range set {
			at = append(at, sha+" ("+where+")")
		}
		sort.Strings(at)
		problems = append(problems, fmt.Errorf("%s has %d SHAs across the workflows, want 1: %s",
			action, len(set), strings.Join(at, ", ")))
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Error() < problems[j].Error() })
	return problems
}

// pinReport is what 1 walk of workflow and action files found.
type pinReport struct {
	files    int
	uses     int
	shas     pinSHAs
	problems []error
}

// checkWorkflowFile checks every uses: key of 1 parsed file: ADR-0021's
// immutable reference and ADR-0090's version comment.
func checkWorkflowFile(path string, data []byte, report *pinReport) {
	report.files++
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		report.problems = append(report.problems, fmt.Errorf("parse %s: %w", path, err))
		return
	}
	var inspectUses func(*yaml.Node)
	inspectUses = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Kind == yaml.ScalarNode && key.Tag == "!!str" && key.Value == "uses" {
					report.uses++
					switch {
					case value.Kind != yaml.ScalarNode || value.Tag != "!!str":
						report.problems = append(report.problems, fmt.Errorf("%s:%d: uses value must be a string", path, value.Line))
					case !isImmutableActionReference(value.Value):
						report.problems = append(report.problems, fmt.Errorf("%s:%d: external action %q must use a full commit SHA or image digest", path, value.Line, value.Value))
					default:
						if err := checkPin(path, value, report.shas); err != nil {
							report.problems = append(report.problems, err)
						}
					}
				}
				inspectUses(value)
			}
			return
		}
		for _, child := range node.Content {
			inspectUses(child)
		}
	}
	inspectUses(&document)
}

// checkPinTree walks every .yml and .yaml file below dirs. A missing directory
// is skipped: .github/actions exists only in some repositories.
func checkPinTree(dirs ...string) (*pinReport, error) {
	report := &pinReport{shas: pinSHAs{}}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if ext := filepath.Ext(path); ext != ".yml" && ext != ".yaml" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkWorkflowFile(path, data, report)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	report.problems = append(report.problems, checkOneSHA(report.shas)...)
	return report, nil
}

func TestWorkflowActionsUseImmutableReferences(t *testing.T) {
	workflowDir := filepath.Join("..", "..", ".github", "workflows")
	actionDir := filepath.Join("..", "..", ".github", "actions")
	if _, err := os.Stat(workflowDir); err != nil {
		t.Fatalf("workflow directory: %v", err)
	}

	report, err := checkPinTree(workflowDir, actionDir)
	if err != nil {
		t.Fatalf("walk workflow directories: %v", err)
	}
	for _, problem := range report.problems {
		t.Error(problem)
	}
	if report.files == 0 {
		t.Fatal("no workflow files found")
	}
	if report.uses == 0 {
		t.Fatal("no action references found")
	}
	if len(report.shas) == 0 {
		t.Fatal("no OWNER/REPO@SHA action pins found: the ADR-0090 check would pass on nothing")
	}
}

// placeholder SHAs for synthetic fixtures (ADR-0088): not live pins.
const (
	fixtureSHA  = "0123456789abcdef0123456789abcdef01234567"
	fixtureSHA2 = "89abcdef0123456789abcdef0123456789abcdef"
)

func TestPinCheckSelfTest(t *testing.T) {
	step := func(uses string) string {
		return "jobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: " + uses + "\n"
	}
	call := func(uses string) string {
		return "jobs:\n  triage:\n    uses: " + uses + "\n    secrets: inherit\n"
	}
	const corePin = corePinPrefix + "pr-triage.yml@" + fixtureSHA
	for _, tc := range []struct {
		name  string
		files []string
		want  []string // substrings, 1 for each expected problem; none means pass
	}{
		{name: "three-part comment", files: []string{step("actions/checkout@" + fixtureSHA + " # v7.0.1")}},
		{name: "two-part comment", files: []string{step("ryand56/r2-upload-action@" + fixtureSHA + " # v1.5")}},
		{name: "quoted value", files: []string{step(`"actions/checkout@` + fixtureSHA + `" # v7.0.1`)}},
		{name: "nested action path", files: []string{step("frostyard/repogen/.github/actions/publish-to-r2@" + fixtureSHA + " # v1.2.3")}},
		{name: "local action needs no comment", files: []string{step("./.github/actions/check")}},
		{name: "docker digest needs no comment", files: []string{step("docker://alpine@sha256:" + strings.Repeat("b", 64))}},
		{name: "core pin with comment", files: []string{call(corePin + " # core#246 (frostyard/core main, 2026-11-14; core has no release tags)")}},
		{name: "same SHA in 2 files", files: []string{
			step("actions/checkout@" + fixtureSHA + " # v7.0.1"),
			step("actions/checkout@" + fixtureSHA + " # v7.0.1"),
		}},
		{name: "missing comment", files: []string{step("actions/checkout@" + fixtureSHA)},
			want: []string{`actions/checkout has comment "", want # vX.Y.Z or # vX.Y`}},
		{name: "major-only comment", files: []string{step("actions/checkout@" + fixtureSHA + " # v7")},
			want: []string{`actions/checkout has comment "# v7"`}},
		{name: "comment with extra text", files: []string{step("actions/checkout@" + fixtureSHA + " # v7.0.1 pinned")},
			want: []string{`has comment "# v7.0.1 pinned"`}},
		{name: "branch comment on a non-core action", files: []string{step("systemd/mkosi@" + fixtureSHA + " # main 2026-11-14")},
			want: []string{`systemd/mkosi has comment "# main 2026-11-14"`}},
		{name: "core pin with no comment", files: []string{call(corePin)},
			want: []string{"pr-triage.yml has no comment"}},
		{name: "2 SHAs for 1 action in 1 file", files: []string{
			"jobs:\n  a:\n    runs-on: x\n    steps:\n      - uses: actions/checkout@" + fixtureSHA + " # v7.0.1\n" +
				"  b:\n    runs-on: x\n    steps:\n      - uses: actions/checkout@" + fixtureSHA2 + " # v7.0.2\n",
		}, want: []string{"actions/checkout has 2 SHAs across the workflows, want 1"}},
		{name: "2 SHAs for 1 action across files", files: []string{
			step("actions/checkout@" + fixtureSHA + " # v7.0.1"),
			step("actions/checkout@" + fixtureSHA2 + " # v7.0.2"),
		}, want: []string{"actions/checkout has 2 SHAs across the workflows, want 1"}},
		{name: "2 SHAs for 1 core workflow", files: []string{
			call(corePin + " # core#246"),
			call(corePinPrefix + "pr-triage.yml@" + fixtureSHA2 + " # core#250"),
		}, want: []string{"pr-triage.yml has 2 SHAs across the workflows, want 1"}},
		{name: "different actions may differ", files: []string{
			step("actions/checkout@" + fixtureSHA + " # v7.0.1"),
			step("actions/setup-go@" + fixtureSHA2 + " # v7.1.0"),
		}},
		{name: "tag ref", files: []string{step("actions/checkout@v7")},
			want: []string{`external action "actions/checkout@v7" must use a full commit SHA`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := &pinReport{shas: pinSHAs{}}
			for i, file := range tc.files {
				checkWorkflowFile(fmt.Sprintf("w%d.yml", i), []byte(file), report)
			}
			report.problems = append(report.problems, checkOneSHA(report.shas)...)
			if len(report.problems) != len(tc.want) {
				t.Fatalf("got %d problems %v, want %d %q", len(report.problems), report.problems, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(report.problems[i].Error(), want) {
					t.Errorf("problem %d = %q, want it to contain %q", i, report.problems[i], want)
				}
			}
		})
	}
}

func TestPinCheckTreeSkipsMissingActionsDirectory(t *testing.T) {
	dir := t.TempDir()
	workflows := filepath.Join(dir, "workflows")
	if err := os.Mkdir(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "jobs:\n  j:\n    runs-on: x\n    steps:\n      - uses: actions/checkout@" + fixtureSHA + " # v7.0.1\n"
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	actions := filepath.Join(dir, "actions", "setup")
	if err := os.MkdirAll(actions, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := "runs:\n  using: composite\n  steps:\n    - uses: actions/checkout@" + fixtureSHA2 + " # v7.0.2\n"
	if err := os.WriteFile(filepath.Join(actions, "action.yml"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := checkPinTree(workflows, filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatalf("a missing directory must be skipped: %v", err)
	}
	if report.files != 1 || len(report.problems) != 0 {
		t.Fatalf("files = %d, problems = %v; want 1 file and no problem", report.files, report.problems)
	}

	report, err = checkPinTree(workflows, filepath.Join(dir, "actions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.problems) != 1 || !strings.Contains(report.problems[0].Error(), "actions/checkout has 2 SHAs") {
		t.Fatalf("problems = %v, want the 1-SHA check to span workflows and action metadata", report.problems)
	}
}
