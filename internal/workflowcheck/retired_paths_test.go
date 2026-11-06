package workflowcheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// retiredPathNotes are the only files that may name the retired lesson
// directory or the old CI workflow path: they record the retirement itself
// (ADR 0014). Every other mention is a stale reference.
var retiredPathNotes = map[string]bool{
	"AGENTS.md": true,
	"docs/adr/0014-adopt-the-repository-baseline.md": true,
	"docs/design/agent-workflows.md":                 true,
	"docs/design/engineering-lessons.md":             true,
}

// retiredPaths are the paths ADR 0014 removed or renamed.
var retiredPaths = []string{"docs/agents/skills", "workflows/test.yml"}

// deletedLessonNames are the retired lesson files whose rules were not kept
// as an anchor of engineering-lessons.md. Together with the anchors, they
// are every name a stale `<lesson>.md` citation can carry.
var deletedLessonNames = []string{
	"claude-md-symlink-never-appears-staged",
	"dont-silently-stage-files-outside-a-chunk-explicit-list",
	"go-sum-diff-criteria-can-be-unsatisfiable",
	"keep-a-modules-source-and-doc-restatement-in-the-same-sweep-chunk",
	"mechanism-only-chunks-still-need-a-doc-touch",
	"negative-claim-objection-means-delete-not-reword",
	"verify-required-files-are-staged-before-submitting",
	"scope-html-assertions-to-the-region-under-test",
}

var (
	lessonAnchorRef = regexp.MustCompile(`engineering-lessons\.md#([a-z0-9-]+)`)
	lessonAnchorDef = regexp.MustCompile(`<a id="([a-z0-9-]+)"></a>`)
	// A Markdown file name; a lesson name among them is a stale citation.
	docName = regexp.MustCompile(`\b([a-z0-9]+(?:-[a-z0-9]+)+)\.md\b`)
	// A file name split by a space after a hyphen ("per-capability- set.md").
	splitDocName = regexp.MustCompile(`[a-z0-9]-[ \t]+[a-z0-9-]+\.md\b`)
	// A comment continuation after a hyphen or a slash, joined before every
	// check, so that a citation wrapped over 2 comment lines is 1 token.
	wrappedComment = regexp.MustCompile(`([-/])\n[ \t]*(?://|#|\*)?[ \t]*`)
	// URLs name files in other repositories; the file-name checks skip them.
	urlText = regexp.MustCompile(`https?://[^\s)>\]"'` + "`" + `]+`)
)

var citationCheckedExt = map[string]bool{
	".go": true, ".md": true, ".yml": true, ".yaml": true, ".json": true,
	".toml": true, ".sh": true, ".templ": true, ".mjs": true,
}

// citationProblems returns the stale citations in 1 file's text. anchors is
// the set of anchors that engineering-lessons.md defines.
func citationProblems(rel, text string, anchors map[string]bool) (problems []string, citations int) {
	joined := wrappedComment.ReplaceAllString(text, "$1")
	if !retiredPathNotes[rel] {
		for _, retired := range retiredPaths {
			if strings.Contains(joined, retired) {
				problems = append(problems, rel+": cites the retired path "+retired)
			}
		}
	}
	for _, m := range lessonAnchorRef.FindAllStringSubmatch(joined, -1) {
		citations++
		if !anchors[m[1]] {
			problems = append(problems, rel+": engineering-lessons.md#"+m[1]+" names no anchor")
		}
	}
	local := urlText.ReplaceAllString(joined, "")
	deleted := map[string]bool{}
	for _, name := range deletedLessonNames {
		deleted[name] = true
	}
	for _, m := range docName.FindAllStringSubmatch(local, -1) {
		if anchors[m[1]] || deleted[m[1]] {
			problems = append(problems, rel+": cites the retired lesson file "+m[1]+".md")
		}
	}
	for _, m := range splitDocName.FindAllString(local, -1) {
		problems = append(problems, rel+": cites a split file name "+m)
	}
	return problems, citations
}

func TestCitationProblemsFixtures(t *testing.T) {
	anchors := map[string]bool{"calibrate-canned-fixture-data": true}
	cases := []struct {
		name, rel, text string
		want            int
	}{
		{"anchor citation", "a.go", "// docs/design/engineering-lessons.md#calibrate-canned-fixture-data\n", 0},
		{"wrapped anchor citation", "a.go", "// docs/design/engineering-lessons.md#calibrate-canned-\n// fixture-data\n", 0},
		{"unknown anchor", "a.go", "// engineering-lessons.md#no-such-anchor\n", 1},
		{"retired directory", "a.go", "// see docs/agents/skills/x.md\n", 1},
		{"wrapped retired workflow", "a.go", "// .github/workflows/\n// test.yml\n", 1},
		{"retired workflow in a note", "AGENTS.md", "`.github/workflows/test.yml` became ci.yml\n", 0},
		{"abbreviated lesson name", "a.go", "// and calibrate-canned-fixture-data.md\n", 1},
		{"wrapped abbreviated lesson name", "a.go", "// and calibrate-canned-\n// fixture-data.md\n", 1},
		{"deleted lesson name", "a.md", "see go-sum-diff-criteria-can-be-unsatisfiable.md\n", 1},
		{"split name", "a.go", "// per-capability- set.md\n", 1},
		{"illustrative name", "a.md", "create `replace-the-remote-mount-ui.md` in docs/plans\n", 0},
		{"external reference", "a.md", "[x](https://github.com/frostyard/core/blob/main/docs/adr/0071-hold-every-code-repository-to-one-baseline.md)\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := citationProblems(tc.rel, tc.text, anchors)
			if len(got) != tc.want {
				t.Errorf("citationProblems(%q) = %q, want %d problem(s)", tc.text, got, tc.want)
			}
		})
	}
}

// TestNoCitationOfRetiredPaths guards the baseline cleanup (ADR 0014): no
// tracked text file cites the deleted docs/agents/skills/ lessons or the
// renamed .github/workflows/test.yml, every engineering-lessons.md#<anchor>
// citation names an anchor that exists, and no file cites a lesson by its
// old file name. Wrapped comment lines are joined first.
func TestNoCitationOfRetiredPaths(t *testing.T) {
	root := filepath.Join("..", "..")

	lessons, err := os.ReadFile(filepath.Join(root, "docs", "design", "engineering-lessons.md"))
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]bool{}
	for _, m := range lessonAnchorDef.FindAllStringSubmatch(string(lessons), -1) {
		anchors[m[1]] = true
	}
	if len(anchors) == 0 {
		t.Fatal("engineering-lessons.md defines no anchors")
	}

	files := map[string]string{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".git", ".agents", "bin", "dist", "site/node_modules", "site/dist", "site/.astro":
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasSuffix(rel, "_templ.go") ||
			rel == "internal/workflowcheck/retired_paths_test.go" {
			return nil
		}
		if citationCheckedExt[filepath.Ext(rel)] || rel == "Makefile" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var problems []string
	var citations int
	for rel, text := range files {
		found, n := citationProblems(rel, text, anchors)
		problems = append(problems, found...)
		citations += n
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	t.Logf("checked %d files, %d lesson citations, %d anchors", len(files), citations, len(anchors))
	if citations == 0 {
		t.Error("found no engineering-lessons.md citation: the scan read nothing")
	}
}
