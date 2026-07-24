# Contract-test fixtures need their own canned backend response per capability set, not one shared response

**When it applies:** Writing or reviewing a contract/integration test
harness that runs the same assertions across several named fixtures
(different `capability.Set` combinations — e.g. `bootc-only`,
`rpm-ostree-only`, `bootc-plus-rpm-ostree`) to prove per-fixture rendering
differences, where a fake broker/backend returns one hardcoded "canned"
response for a query regardless of which fixture is currently running.

**What to do:** If every fixture's fake broker returns the identical
canned response object, the response necessarily contains fields that are
impossible under some of the fixtures it's reused for (e.g. populated
rpm-ostree version/checksum detail served to a `bootc-only` fixture that
never advertises rpm-ostree). The test then can't prove the acceptance
criterion it's aimed at — "detail from an unavailable source is absent" —
because the canned data always has that detail present; the assertion
either has to skip checking it or ends up asserting that impossible data
renders. Give each fixture (or at least each fixture whose point is to
prove a source is absent/unavailable) its own canned response calibrated
to what that fixture's capability set can actually produce, and add
explicit failure/unavailable-variant fixtures for *every* source the
criterion calls out (not just the one source that already has a failure
fixture) — a matrix of N sources needs N failure fixtures, not one.

**Learned from:** mill run for issue #51, chunk 9
(`cmd/pilothouse/capability_contract_test.go`). Round 2 objected that
`runCapabilityContractFixture` called `cannedHostImageStatus()` for every
capability set including `bootc-only`, so the canned response's
`RPMOStreeAvailable: true` plus populated rpm-ostree `Version`/`Checksum`
rendered even though that fixture never advertised rpm-ostree — the test
asserted impossible data instead of proving absence. The same round noted
no fixture ever set `BootcError`/`BootcAvailable: false`, so only
rpm-ostree's failure path was exercised even though the acceptance
criterion required success/failure coverage for both host-image sources.
