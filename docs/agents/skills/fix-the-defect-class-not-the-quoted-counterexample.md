# When a reviewer objection includes a concrete counter-example, fix the general defect it exposes, not that literal input

**When it applies:** A chunk implements a validator/parser with an
acceptance criterion like "malformed or non-conforming input must return a
non-nil error and a zero-value result" (a discriminated document format,
a required-field check, a strict-mode decode), and a review round rejects
it with a specific JSON/input example that slips through.

**What to do:** Reviewer objections that include a concrete counter-example
are illustrating a class of bug, not asking you to special-case that exact
input. Before resubmitting, ask "what is the general rule this example
violates?" and fix that rule everywhere it applies in the same function,
not just the literal case quoted. Two failure modes recur here:

1. **Optional-looking required-field checks.** A guard written as
   `if field != "" && field != want { reject }` treats the field as
   optional — omitting it entirely bypasses validation that was meant to
   be mandatory. The fix is to require presence (`if field != want { reject
   }`, or an explicit presence check first), not to special-case the
   empty string the reviewer's example happened to use.
2. **Discriminator-only validation.** Matching `apiVersion`/`kind` proves
   the document claims to be the right shape, not that its required
   substance is actually present. If the acceptance criterion implies a
   populated result (e.g. deployments, a status object), validate that the
   required nested fields exist before returning success — otherwise a
   document that passes the discriminator check but omits everything else
   still returns a confident, empty "success."

If the same objection (or a trivially related one) comes back on the next
round with a different counter-example, that is a signal the previous fix
patched the quoted input rather than the rule — re-derive the full set of
required checks for the format from the acceptance criteria in one pass
instead of iterating example-by-example. This burns `review_rounds`
identically to patching one cell of a test matrix at a time (see
`enumerate-the-full-test-matrix-for-multi-axis-criteria.md`); the same
discipline applies to validation logic, not just test coverage.

**Learned from:** mill run for issue #51, chunk 3
(`maintenance.ParseBootcStatus`). Round 1 objected that
`{"kind":"BootcHost","status":{}}` was accepted because the apiVersion
prefix check only runs when `host.APIVersion != ""`. Round 2 restated the
same defect with a different payload
(`{"kind":"BootcHost","status":{"booted":{...}}}`). Round 3 restated it a
third time (`{"kind":"BootcHost","status":{"booted":{}}}`) and added a
second, related objection that no chunk had validated presence of the
required `status`/`booted` structure, so
`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost"}` returned
`BootcAvailable: true` with every deployment nil. The line
`host.APIVersion != "" && !strings.HasPrefix(...)` was never changed across
any of the three rounds — each resubmission left the general rule (an
empty apiVersion bypasses the check) intact and the run failed once
`review_rounds` was exhausted.
