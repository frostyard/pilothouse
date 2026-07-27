# A "harness-wide" guard test must scan every file in the harness, not the one entrypoint script the chunk happens to touch

**When it applies:** Writing or reviewing a Go guard/contract test
(`packaging/*_test.go`, or any `*_test.go` that greps/parses shell scripts
for a policy invariant) whose acceptance criterion is phrased as a
repo-wide or harness-wide guarantee — "every `scp`/`guest_copy` destination
must stay inside `~/vm-boot`," "every guest script must be invoked as
`sudo sh ...`," "every discovered guest script must pass mode/shebang/
`require_root`/shellcheck checks" — for a harness made of multiple files
across `test/**/lib/` and `test/**/guest/` (or any similar directory tree
of scripts/entrypoints).

**What to do:** Two failure modes recur together and both slip past
`gates_chunk`/`make test` because the guard test itself passes — it's just
checking the wrong scope:

1. **Single-file scope instead of whole-harness scope.** A guard written to
   grep only the one entrypoint script named in the chunk description (e.g.
   `vm-boot-test.sh`) for `scp`/`guest_copy` call sites, or to check
   invocation form only in that file, misses every other file in the
   harness (`test/vm/lib/*.sh`) that can call the same risky primitive
   directly. Before writing the guard, list every file under the harness's
   root that could plausibly contain the primitive being policed, and scan
   all of them — not just the one the chunk's own diff touched.
2. **Non-recursive directory listing instead of a full walk.** Using
   `os.ReadDir` (single level, skips subdirectories) to enumerate "every
   guest script" or "every assertion script" lets a file added later under
   a subdirectory (`test/vm/guest/subdir/new.sh`) escape every check the
   guard was supposed to run against it. Use `filepath.WalkDir` (or
   equivalent recursive discovery) so the guard's file set is derived from
   the actual directory tree, not a fixed depth, and re-derives itself
   automatically as the harness grows.

Also watch for the **stringly-typed invocation check** variant of the same
bug: a guard that greps for one literal path spelling (e.g.
`'~/vm-boot/guest/<name>.sh'`) instead of parsing/normalizing the actual
call form will silently pass an invocation through a variable or a
differently-spelled path. Prefer deriving the expected file set
programmatically and asserting a property against each discovered call
site, not string-matching one hardcoded literal.

Before submitting a guard test that claims a harness-wide invariant, mentally
add one new file to every directory the invariant is supposed to cover
(a new `test/vm/lib/*.sh` helper, a new `test/vm/guest/subdir/*.sh` script,
a call site using a variable instead of a literal path) and confirm the
guard would still catch a violation in it.

**Learned from:** mill run for issue #67, chunk 2 (`packaging/vm_harness_test.go`
guard tests for the VM boot-validation harness). The same defect — a
copy-containment/staging-path guard that scanned only `vm-boot-test.sh`
instead of the whole harness — was raised as a `high`-severity objection in
two consecutive `chunk_revise` rounds without being fixed, and a related
non-recursive `os.ReadDir`-based guest-script enumeration was raised in a
third round; the run exhausted its `review_rounds` limit on chunk 2 and
terminated as failed with the defect still present.
