# Generated *_templ.go files are gitignored and never appear in diffs

**When it applies:** Any change touching `*.templ` files, and any review of a
diff that modifies templ views.

**What to do:** Doc comments and all other content for templ components live
in the `.templ` source file. `make generate` regenerates `*_templ.go`, which
is **gitignored** (`.gitignore: /**/*_templ.go`) — it is never committed and
will never appear in `git diff` or a staged change. Implementers: edit the
`.templ` file and run `make generate`; never hand-edit the generated file.
Reviewers: verify generated-file claims by reading the file on disk (e.g.
`grep` the regenerated `*_templ.go` in the working tree), and never reject a
change for not including a gitignored generated file in its diff — that
requirement is structurally impossible to satisfy.

**Learned from:** mill smoke run 2026-07-22 — a chunk deadlocked because the
reviewer demanded `views_templ.go` in the staged diff three times; the
implementer had correctly regenerated it on disk but could not stage an
ignored file.
