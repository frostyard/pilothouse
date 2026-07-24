# Parsers for external CLI status JSON must reject malformed input, not default to success

**When it applies:** Writing or reviewing a parser that decodes JSON emitted
by an external host tool (`bootc status --json`, `rpm-ostree status --json`,
or any future host-tool status document) into a typed Go struct, where the
chunk contract says malformed/non-conforming input must return a non-nil
error and a zero-value result.

**What to do:** A parser that only checks a discriminator field (`kind`,
`apiVersion`) when it is *present and non-empty* silently accepts documents
that omit the field entirely — `encoding/json` leaves an absent field at its
Go zero value, and a zero-value check like
`strings.HasPrefix(host.APIVersion, prefix)` on an empty string is false but
an equality/prefix check gated behind `if host.APIVersion != ""` skips
validation altogether for the omitted case. Always validate a discriminator
field unconditionally (reject empty exactly like wrong-value), and validate
the presence of every substructure the success return type depends on
(e.g. `status.booted`) before building the result — a document that passes
the discriminator check but has a nil/absent required substructure must
still return an error, not a success value with zero-valued fields inside
it. Write malformed-payload test cases that isolate each failure mode
separately: missing discriminator, wrong discriminator, right discriminator
but missing required substructure — a single "obviously broken" fixture
(e.g. empty JSON `{}`) will pass an incomplete validator without exercising
the field-by-field gaps that a real (if unusual) host tool output could hit.

**Learned from:** mill run for issue #51, chunk 3 (`ParseBootcStatus` in
`internal/modules/maintenance/hostimage.go`). Three consecutive revision
rounds hit variations of the same defect class: round 1 and 2 flagged that
an empty `apiVersion` bypassed the prefix check entirely (`{"kind":
"BootcHost","status":{...}}` parsed as `BootcAvailable: true`); round 3,
after the discriminator check was tightened, flagged that a
discriminator-valid document with no `status.booted` substructure
(`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost"}`) still
returned a successful-but-empty `HostImageStatus{BootcAvailable: true}`
instead of an error. Each round fixed the specific counterexample quoted
without checking for the other gap in the same validation function.
