# A Go meta-guard test that polices call sites in source text must parse the AST, not regex/substring-match the text

**When it applies:** Writing or reviewing a Go test that asserts a
structural invariant about *another piece of Go source in the same
repository* — "this file contains exactly one `exec.CommandContext` call
site," "every `imageGit(...)` invocation uses a literal subcommand from an
allowlist," "no bare `exec.Command` appears outside the one sanctioned
wrapper" — by grepping/regexing the source text of the file(s) under test.

**What to do:** A regex or substring pattern over Go source is written
against one syntactic spelling of the call, but Go admits many
equivalent spellings that reviewers will keep finding on successive
rounds: extra whitespace before `(`, a dot-import or aliased import of
`os/exec`, a call reached through a local variable or method value instead
of the literal function name, a non-literal/expression argument where the
pattern expects a bare word or string literal (`imageGit(t, fixture.sandbox,
subcommand)` vs. the anchored `imageGit(t, <word>, "<literal>")`), or a
second, unmatched call elsewhere in the file that the pattern's "count >=
1" check never rules out. Each fix that tightens the regex for one
bypass tends to leave (or reopen) another, burning `chunk_revise` rounds
without ever closing the class.

Parse the file(s) with `go/parser` and walk the AST with `go/ast.Inspect`
(or `x/tools/go/analysis`-style visitors) instead:
- Match `*ast.CallExpr` nodes and resolve the callee identifier/selector
  structurally (`ast.SelectorExpr.Sel.Name == "CommandContext"` with
  package resolved via imports, not string-matching `"exec.CommandContext"`
  in the source).
- To assert "every call site matches an allowlist," collect *all* matching
  `*ast.CallExpr` nodes in the file and assert the collected set's size
  equals the count that satisfy the allowlist predicate — not just that at
  least one matching, well-formed call exists ("count >= 1" and "count ==
  allowlisted count" are different claims; the guard must make the latter).
- To assert an argument is a literal (not a variable/expression), check
  the argument node's type is `*ast.BasicLit` — string/regex matching on
  the argument's source text cannot distinguish a literal from an
  identically-spelled variable name.

**Learned from:** mill run for issue #80 (image-workspace test harness),
chunk 0 — a `packaging/image_process_test.go` meta-guard for "exactly one
bounded `exec.CommandContext` call site" and "every `imageGit` call site
resolves through a literal-subcommand allowlist," both implemented as
regex/substring checks over the test file's own source, were rejected as
bypassable in five consecutive `chunk_revise` rounds (whitespace/import
variants, expression arguments, unasserted call-set completeness) without
the defect class ever closing; the run exhausted `review_rounds` and
terminated as failed with the guard still regex-based.
