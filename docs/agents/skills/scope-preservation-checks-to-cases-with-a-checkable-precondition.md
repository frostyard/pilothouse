# A "the input must be left unchanged" invariant can't apply uniformly across a negative-case matrix — scope it to cases where there's something to check

**When it applies:** Writing or reviewing acceptance criteria for a
function's refusal/error paths across a matrix of invalid inputs (missing
argument, empty string, a relative path, a nonexistent path, an existing
path/entry) where one blanket criterion says every refusal case must leave
some referenced filesystem entry provably unchanged — e.g. "on any refusal,
the presented root's `Lstat`/`EvalSymlinks`/`Stat`/`ReadDir` results must be
identical before and after."

**What to do:** That check only has a subject when the input actually names
a resolvable, pre-existing entry. Applying it verbatim to the whole
negative-case list is incoherent for the cases that don't have one: no
argument, an empty string, and a nonexistent path have no directory to
`Lstat`/`Stat`/`ReadDir` in the first place, so the criterion as written is
unsatisfiable (or vacuously trivial) for those rows. Split the matrix by
precondition before finalizing the criterion:
- Cases with an existing, resolvable entry (a real directory, a symlink to
  one) get the full identity/type/mode preservation check against that
  entry — and if the entry is itself a symlink, check *both* the presented
  symlink entry (via `Lstat`) and its resolved target separately, since a
  check that only inspects the resolved directory can pass even if the
  symlink itself was unlinked and recreated to point at the same place.
- Cases with no resolvable entry (missing arg, empty string, relative path,
  nonexistent path) fall back to a whole-sandbox/whole-tree no-mutation
  check instead — prove the refusal touched nothing anywhere, since there
  is no single recorded entry to diff.
Don't let a single sentence in the spec ("preserve the root on every
refusal") get copied as one uniform test across a case list that mixes
inputs with and without a preservation subject; the reviewer will keep
rejecting whichever row currently lacks a coherent check, one round at a
time, until the plan-round budget runs out.

**Learned from:** mill run for issue #80 (image-workspace test-fixture
library), plan revision round 6 (final, run failed on the plan-round
limit) — the runner-root preservation criterion for refusal cases had been
patched repeatedly (rounds 2, 5) but still applied one preservation check
uniformly to no-argument/empty-string/relative/nonexistent-root rows that
have no existing, resolvable root to `Lstat`/`Stat`/`ReadDir`, alongside
existing-root and symlinked-root rows that do.
