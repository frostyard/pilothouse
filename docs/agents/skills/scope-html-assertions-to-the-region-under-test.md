# Scope HTML region assertions to a specific container, not a whole-page substring check

**When it applies:** Writing or reviewing a rendering test that must prove
two or more structurally distinct regions of one rendered page (e.g. the
sidebar nav vs. the dashboard card grid, both present in the same `GET /`
response) each independently include or exclude the same identifying text
(a module's display name), especially across multiple capability-gated
fixtures.

**What to do:** `strings.Contains(fullPageHTML, manifest.Name)` cannot tell
"present in nav but missing from the dashboard" apart from "present in
both" or "absent from both" whenever the identifying text can legitimately
appear in more than one region of the same page. That makes the assertion
blind to a regression in either region alone — the check still passes if
one of the two registries silently breaks, as long as the other still
renders the name somewhere on the page. Isolate each region before
asserting: extract the nav fragment and the dashboard fragment separately
(scope by container id/class, split on a page landmark, or anchor on
something region-specific like `href="/module-id"` for nav links vs. a
card's own class/data attribute for dashboard cards), then run
Contains/NotContains against each isolated fragment independently. When an
acceptance criterion enumerates multiple web-side registries (e.g.
nav-on-dashboard, dashboard cards, nav-on-other-authenticated-pages,
routes), write one present-when-available / absent-when-gated assertion
pair per registry, each scoped to its own container — don't collapse
several registries into a single page-wide check just because they happen
to render on the same HTTP response.

**Learned from:** mill run for issue #54, chunk 10 (capability contract test
harness) — three consecutive revision rounds rejected
`cmd/pilothouse/capability_contract_test.go`'s dashboard/nav assertions
because they checked `strings.Contains`/`NotContains` against the whole
rendered dashboard page using `manifest.Name`. Because the sidebar nav also
contains the module name, this could not catch a dashboard-card-only
regression (or a nav-only one), and a related objection on the same chunk
noted that other authenticated pages were checked for dead links but never
for retaining available modules' nav entries. The objection was never fixed
with a differently-scoped assertion before `review_rounds` was exhausted and
the run failed.
