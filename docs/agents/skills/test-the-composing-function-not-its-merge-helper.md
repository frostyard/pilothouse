# Test the top-level composing function, not the merge helper it calls

**When it applies:** Any acceptance criterion asking for a unit test of a
function whose job is to call several independent real-world
probes/collectors and combine their results (e.g. `capability.Probe(ctx,
config)`, which calls `probeSystemd`, `probeJournald`, `probeUpdex`, etc.,
then merges the returned `Set`s).

**What to do:** Test doubles must be injected at the boundaries of the
top-level function under test, and the test must call that top-level
function itself, asserting on its actual return value under partial-success
and all-fail fixtures. Testing only an internal merge/union helper (one that
takes already-computed `Set`s as plain arguments, e.g. `unionSets`) does not
prove the top-level function correctly wires up, calls, and error-tolerates
each real probe — it would still pass if the function omitted a probe,
called the wrong one, or mis-ordered composition. If the top-level function
hardcodes calls to real host-dependent probes with no seam to inject fakes,
that is a design gap in the implementation, not a testing shortcut to route
around: either refactor so the probe list/table is injectable, or negotiate
the acceptance criterion down before writing the test. Reviewers will reject
a helper-only test against a composition-function acceptance criterion every
time, no matter how many revision rounds it's resubmitted across.

**Learned from:** mill run for issue #50, chunk 4 (`capability.Probe`) — the
same objection ("this test only calls `unionSets`, not `Probe`") recurred
across 3 revision rounds without the underlying testability gap being
addressed, exhausting `review_rounds` and failing the run.
