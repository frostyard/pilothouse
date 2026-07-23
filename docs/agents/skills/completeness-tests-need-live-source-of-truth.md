# Completeness/contract tests must check against the live source, not a second hardcoded copy

**When it applies:** Writing or reviewing a test whose acceptance criterion
is a completeness claim — "every registered broker ID is covered," "all
constants in X are represented," "nothing is missing from the table" — where
the natural implementation is a hand-written fixture (e.g. a
`capabilityTable` slice) that is supposed to mirror another hand-written list
(e.g. the `Action*`/`Query*` constants in `internal/broker/api.go`, or the
handlers actually registered at runtime).

**What to do:** A test that only asserts properties of the fixture itself
(its length, or that iterating it produces the expected count) proves
nothing about completeness — it would pass identically if the fixture
silently dropped a real ID and substituted an arbitrary placeholder, since
both hardcoded lists drift together undetected. Derive the "actual" side of
the comparison from the live source instead of a second hand-maintained
list: parse/reflect the real constants (e.g. via `go/ast` over
`internal/broker/api.go`, or a generated list), or enumerate the actual
handler registry the daemon builds at startup, and diff that against the
fixture. If deriving the true set mechanically is impractical, say so
explicitly in the plan/acceptance criteria rather than letting a
fixture-vs-fixture test masquerade as a completeness guarantee — reviewers
will keep rejecting the shallow version.

**Learned from:** mill run for issue #50, chunk 12 (final contract test) —
`capability_contract_test.go`'s "table-completeness" and "no others"
assertions only validated the hardcoded `capabilityTable`'s own length and
per-kind counts, never comparing it against the real `Action*`/`Query*`
constants in `internal/broker/api.go` or the daemon's actual registered
handler set, so a table row could go stale without the test catching it.
