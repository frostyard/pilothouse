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

This applies to *every* capability-gated boundary in this repo, not just the
one named in the origin case below — the registry's module-availability
filter, `internal/web`'s nav/dashboard rendering, and any future gated
surface all share the same shape. Concretely, for a "nav entry is absent
when capability X is missing" criterion, the test must construct a real
`web.NewServer(registry, brokerClient, logger, ...)` wired to a fake broker
that reports the capability set, call the module's actual `New()` and the
server's real `Render`, and assert on the emitted HTML — not call
`platform.Available`/a gating helper directly with a hand-built
`capability.Set`. That direct-call version proves the pure filtering logic
is correct given an already-computed capability set, but never proves
`internal/web`'s nav wiring actually asks the module's gate before deciding
to render it.

**Learned from:** mill run for issue #54. First seen in chunk 1 (platform
registry capability-based module availability) — the objection recurred
across 2 revision rounds because the test kept using a test-only
`availableModuleIDs(capability.Set)` helper that filtered
`registry.Modules()` directly, instead of exercising a fake `Host`'s
`Capabilities()` through the actual registry availability API. Despite that
skill already being on file, the *same* antipattern recurred a second time
in chunk 5 (logs module nav gating): the test called `platform.Available`
directly instead of driving a real `internal/web` server/render round trip,
and the objection was rejected 3 rounds straight until `review_rounds` was
exhausted and the run failed. The lesson as originally written was too
tightly scoped to its origin example (registry/`Host.Capabilities()`) to be
recognized as applying to the nav-rendering boundary — this revision
broadens it explicitly so the next gated surface doesn't repeat it a third
time.
