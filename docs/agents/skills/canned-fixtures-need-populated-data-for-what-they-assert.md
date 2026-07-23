# Canned fixture data must be populated enough to exercise what the test asserts

**When it applies:** Writing or reviewing a contract/integration test whose
fake backend (a fake broker, fake API client, fake datastore, etc.) returns
a single hardcoded "canned" response for a query, and the test then asserts
that some *conditionally-rendered* UI element (a per-row action, a form tied
to a specific record) is absent under a gated/degraded fixture.

**What to do:** If the canned response is an empty or zero-value collection
(e.g. an empty list of managed mounts, an empty table, a nil slice), any
per-item view element that only renders when the collection is non-empty
can never appear in the rendered output — under *any* fixture, gated or
not. An assertion like "no Delete form is rendered" is vacuously true in
that case: it would pass identically whether the gating logic correctly
hides the form or whether the form's rendering code was deleted entirely,
because the row that would contain the form never exists in the first
place. This makes the test look like it covers the acceptance criterion
while proving nothing about the actual conditional-rendering logic. Before
trusting such an assertion, populate the canned/fixture data with at least
one representative record for every collection whose per-item elements the
test needs to prove present-or-absent, then assert against the rendered
per-item markup, not just container-level or top-of-page controls.

**Learned from:** mill run for issue #54, chunk 10 (capability contract test
harness) — `cannedQueryResponse` returned an empty storage `Snapshot` in
every fixture, so `GET /storage` under the no-systemd fixture never
rendered a managed mount row. The reviewer flagged, identically, across two
separate revision cycles (4 total rounds) that this meant the per-mount
Mount/Unmount/Delete forms were never proven absent — only the top-level
"Add remote mount" link was. The fix (add a mount to the fixture) was never
applied before `review_rounds` was exhausted a second time, and the run
failed with this exact objection as the last recorded event.
