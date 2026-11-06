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
	"internal/workflowcheck/retired_paths_test.go":   true,
}

var (
	lessonAnchorRef = regexp.MustCompile(`engineering-lessons\.md#([a-z0-9-]+)`)
	lessonAnchorDef = regexp.MustCompile(`<a id="([a-z0-9-]+)"></a>`)
	// A long hyphenated Markdown file name, the shape of the retired lesson
	// files. Each one that is cited must exist somewhere in the tree.
	hyphenatedDoc = regexp.MustCompile(`\b([a-z0-9]+(?:-[a-z0-9]+){3,})\.md\b`)
	// A file name split by a space after a hyphen ("per-capability- set.md").
	splitDocName = regexp.MustCompile(`[a-z0-9]-[ \t]+[a-z0-9-]+\.md\b`)
	// A comment continuation after a hyphen or a slash, joined before the
	// checks so that a citation wrapped over 2 comment lines is 1 token.
	wrappedComment = regexp.MustCompile(`([-/])\n[ \t]*(?://|#|\*)?[ \t]*`)
	// URLs name files in other repositories; they are not checked here.
	urlText = regexp.MustCompile(`https?://[^\s)>\]"'` + "`" + `]+`)
)

var citationCheckedExt = map[string]bool{
	".go": true, ".md": true, ".yml": true, ".yaml": true, ".json": true,
	".toml": true, ".sh": true, ".templ": true, ".mjs": true,
}

// TestNoCitationOfRetiredPaths guards the baseline cleanup (ADR 0014): no file
// cites the deleted docs/agents/skills/ lessons or the renamed
// .github/workflows/test.yml, every engineering-lessons.md#<anchor> citation
// names an anchor that exists, and no other long hyphenated .md file name is
// cited unless that file exists. Wrapped comment lines are joined first.
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
	basenames := map[string]bool{}
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
		if !d.Type().IsRegular() {
			return nil
		}
		basenames[d.Name()] = true
		if strings.HasSuffix(rel, "_templ.go") {
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
		if rel == "internal/workflowcheck/retired_paths_test.go" {
			continue
		}
		if !retiredPathNotes[rel] {
			for _, retired := range []string{"docs/agents/skills", "workflows/test.yml"} {
				if strings.Contains(text, retired) {
					problems = append(problems, rel+": cites the retired path "+retired)
				}
			}
		}
		joined := urlText.ReplaceAllString(wrappedComment.ReplaceAllString(text, "$1"), "")
		for _, m := range lessonAnchorRef.FindAllStringSubmatch(joined, -1) {
			citations++
			if !anchors[m[1]] {
				problems = append(problems, rel+": engineering-lessons.md#"+m[1]+" names no anchor")
			}
		}
		for _, m := range hyphenatedDoc.FindAllStringSubmatch(joined, -1) {
			if !basenames[m[1]+".md"] {
				problems = append(problems, rel+": cites "+m[1]+".md, which does not exist")
			}
		}
		for _, m := range splitDocName.FindAllString(joined, -1) {
			problems = append(problems, rel+": cites a split file name "+m)
		}
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
