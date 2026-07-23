# Generated `*_templ.go` files are untracked and never appear in diffs

**When it applies:** Any change touching `*.templ` files, and any review of a
diff that modifies templ views.

**What to do:** Doc comments and all other content for templ components live
in the `.templ` source file. `make generate` regenerates the `*_templ.go`
output, which is gitignored (`.gitignore`: `/**/*_templ.go`). As of
2026-07-23 **every** `*_templ.go` in this repo is untracked — the few
stragglers that had been committed before the ignore rule existed
(`internal/modules/{services,files,logs}/views_templ.go`) were removed from
the index (`git rm --cached`), so the tree is now consistent: no generated
templ output is tracked.

Consequences:
- A generated `*_templ.go` must **never** appear in a staged diff. Reject a
  diff that hand-adds one, and never demand one be staged.
- Verify generated content by reading it on disk (`grep`/`Read` the
  regenerated file in the working tree), not from the diff.
- Implementers: edit the `.templ` file and run `make generate`; never
  hand-edit generated output.

**Gotcha worth remembering** (why the stragglers existed): a `.gitignore`
rule only stops *new*, untracked paths from being added — it does nothing for
a path Git already tracks. If a generated file ever shows up as tracked again
(`git ls-files <path>` prints it), that is a repo-hygiene bug, not something
to route around mid-change: fix it with `git rm --cached <path>` + commit so
the tree stays consistent with `make generate` output.

**Learned from:** mill smoke run 2026-07-22 (reviewer demanded a gitignored
`views_templ.go` in a staged diff) and mill run for issue #54 chunk 2, which
deadlocked because `internal/modules/services/views_templ.go` was still
tracked at the time. That inconsistency was fixed on 2026-07-23 by untracking
all straggler generated files; this skill now reflects the clean state.
