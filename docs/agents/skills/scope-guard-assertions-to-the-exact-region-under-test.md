# Scope guard/assertion checks to the exact region under test, not a whole-file substring search

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

**The same anti-pattern recurs outside HTML, in shell-script and YAML guard
tests — treat it as a repo-wide habit, not an HTML-only rule.** Mill run for
issue #67 hit four separate variants of "the guard's text search isn't
actually anchored to the thing it claims to check", across four different
chunks, none of which involved HTML:
- `packaging/vm_harness_test.go` (chunk 3): a guard for "the daemon-emitted
  line must reach the `startup_hits` jq command" instead checked
  `strings.Contains` for the redirect operator `<"$journal_response"`
  *anywhere in the script*. Because an earlier, unrelated jq command also
  read the broker response, moving the real assertion to the wrong command
  (or removing it) would not fail the guard.
- `packaging/vm_harness_test.go` (chunk 6): `TestVMRebootPostureDriftGuard`
  used `require.Contains` against the raw script text for the
  sentinel-absence/recreation-metadata/audit-inode assertions. Commenting
  out any one of those assertion lines (turning it into a dead comment)
  left the substring present in the file, so the guard kept passing with
  the actual check deleted. A guard over source text must confirm the
  matched text is on an *executable* line, not merely present anywhere in
  the file (comments included).
- `packaging/vm_harness_test.go` (chunk 1, round 3): a private-key-leak
  guard used a regex anchored with `$` expecting end-of-line behavior, but
  without the multiline flag `$` matches only end-of-file. Combined with
  matching only the literal `id_ed25519` (not variables like
  `$VM_SSH_KEY`), the guard could not catch a script that echoed the key
  through a variable. Whenever a regex guard is meant to check every line,
  explicitly enable multiline mode and confirm the pattern matches
  variable/aliased spellings of the sensitive value, not just one literal
  form.
- `packaging/workflow_vm_job_test.go` (chunk 7): a "the new CI job downloads
  the correct artifact" guard searched the raw workflow YAML text for
  `name: ${{ matrix.artifact }}`. Because an unrelated, pre-existing job in
  the same workflow file already contains that exact string, the guard
  stayed green through **four consecutive review rounds** even though the
  objection ("decode the YAML and assert the new job's own `with.name`
  field directly, don't grep the file") was repeated nearly verbatim each
  time; the chunk exhausted `review_rounds` and soft-passed with the defect
  still open. For structured formats (YAML, JSON, TOML) being asserted on
  in a guard test, parse the structure and index into the specific
  job/step/key under test — never `strings.Contains`/grep the serialized
  text — because any other part of the same document can contain an
  identical-looking substring and mask a real defect indefinitely.

The general rule: before trusting a guard/contract test that scans raw
source text (HTML, shell, YAML, or otherwise) for a substring or regex,
mentally break the specific behavior it's supposed to police (delete the
assertion, comment it out, move it to the wrong command/job/step, swap in a
variable) and confirm the guard would actually catch that specific break —
not just that some text resembling the expected pattern exists somewhere in
the file.
