# Check `git ls-files` before applying the "*_templ.go never appears in diffs" rule

**When it applies:** Any change touching `*.templ` files, and any review of a
diff that modifies templ views.

**What to do:** Doc comments and all other content for templ components live
in the `.templ` source file. `make generate` regenerates `*_templ.go`, and
the repo's `.gitignore` has an untracked-files rule for it
(`/**/*_templ.go`). But gitignore rules only stop *new* files from being
added to the index — they do nothing for a path Git already tracks. Several
generated files in this repo (e.g. `internal/modules/services/views_templ.go`,
`internal/modules/files/views_templ.go`, `internal/modules/logs/views_templ.go`)
were committed before the ignore rule existed (or via a forced add) and
remain tracked, so `git status`/`git diff` correctly show them as modified
whenever the corresponding `.templ` source changes and `make generate` reruns.

Before invoking this rule in either direction, run `git ls-files <path to
the _templ.go file>`:

- **If it prints nothing** (untracked): the file is properly ignored. It
  must never appear in a staged diff; reject a diff that hand-adds it, and
  never demand it be staged — verify its content by reading it on disk
  (`grep`/`Read` the regenerated file in the working tree) instead.
- **If it prints the path** (tracked): the file is expected to show up as a
  normal modified-file diff hunk whenever its `.templ` source changes.
  Require that the diff is *only* the output of `make generate` — reject
  hand-edits to the generated content — but do not reject the chunk merely
  for including the file.

Implementers: still always edit the `.templ` file and run `make generate`;
never hand-edit generated output either way. If a tracked generated file
should really be ignored going forward, that's a separate cleanup
(`git rm --cached` + commit) — don't try to route around it mid-chunk by
omitting it from `git add`, since that leaves the working tree and index
inconsistent with what `make generate` produced.

**Learned from:** mill smoke run 2026-07-22 — a chunk deadlocked because the
reviewer demanded `views_templ.go` in a staged diff three times while
believing all `*_templ.go` were untracked. Mill run for issue #54, chunk 2
hit the *identical* deadlock again for the same file even with this skill in
place, because `internal/modules/services/views_templ.go` is actually
tracked in this repo (`git ls-files` proves it) — the reviewer kept
rejecting it as an improperly-staged ignored file for 3 rounds, exhausting
`review_rounds` and failing the run, when the real requirement was just "diff
must equal `make generate` output," which it already did.
