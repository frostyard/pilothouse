# Exercise the boundary the criterion names, not a shim that takes its output as input

**When it applies:** An acceptance criterion says a test must prove behavior
by driving it through a specific production boundary — e.g. "using a fake
Host's `Capabilities()`" — where the behavior under test (filtering,
gating, availability) is built on top of that boundary's return value.

**What to do:** Don't introduce a test-only helper that accepts the
boundary's *return type* as a plain parameter (e.g. a function taking a
`capability.Set` directly) and assert through that instead of the real call
chain. That kind of test proves the downstream filtering/gating logic is
correct given an already-computed value, but never proves the production
code actually obtains that value by calling the named boundary (a fake
`Host`'s method) — broken or missing wiring between the boundary and the
logic would still pass. Route the test through an actual fake implementing
the interface and call the real integration path that invokes its method,
then assert on the result. If no such path exists yet, that's a design gap
in the implementation to fix (add the seam), not something to route around
with a test-only shim — reviewers reject the shim version on resubmission
just as they did the first time, because the objection is identical: the
fake's method was never called.

**Learned from:** mill run for issue #54, chunk 1 (platform registry
capability-based module availability) — the objection recurred across 2
revision rounds because the test kept using a test-only
`availableModuleIDs(capability.Set)` helper that filtered
`registry.Modules()` directly, instead of exercising a fake `Host`'s
`Capabilities()` through the actual registry availability API. The
underlying gap was never addressed between rounds, `review_rounds` was
exhausted, and the run failed.
