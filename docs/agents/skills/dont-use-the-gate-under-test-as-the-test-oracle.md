# Don't derive a test's expected results from the same predicate the test is verifying

**When it applies:** Writing or reviewing a contract test that iterates
over many entities (modules, routes, features) under several fixtures and
needs, for each entity x fixture combination, an "expected available /
expected gated" value to assert against — where the production code being
tested is itself a gating predicate (e.g. `platform.Available(module,
caps)`).

**What to do:** Computing the expected value by calling that same
predicate (`want := platform.Available(m, fixtureCaps)`) makes the test
tautological: it can only ever confirm the predicate agrees with itself. A
real regression — e.g. a module accidentally picking up an unintended
`CapabilityGate`, or losing one it should have — shifts both the "expected"
and "actual" sides together, so the test keeps passing while the acceptance
criterion (e.g. "every other module is unaffected by the no-systemd
fixture") is silently violated. Build the expected-availability matrix by
hand from the spec/docs (e.g. a literal table: "backups, maintenance,
services, logs, and storage's remote-mount routes are gated on systemd; all
other modules are always available"), independent of any call into the
production gating code, then assert the real predicate's/route's/nav's
actual behavior against that independent matrix.

**Learned from:** mill run for issue #54, chunk 10 (capability contract test
harness) — the degraded-fixture test used `platform.Available(module,
caps)` as the oracle for which modules should be available in each fixture.
The reviewer pointed out this would pass even if an unrelated module (e.g.
`system`, `files`, `activity`) accidentally gained a `Systemd`
`CapabilityGate`, because the test's expectation would shift to match the
bug instead of catching it. This objection, together with a related one
about empty canned fixture data, was never resolved before the chunk
exhausted its second `review_rounds` budget and the run failed.
