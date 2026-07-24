# A new broker capability ID and its completeness-table row must land in the same chunk, not be split across chunks

**When it applies:** Planning a multi-chunk feature that adds a new
`Action*`/`Query*` constant to `internal/broker/api.go` (a new broker
capability), where the daemon-side and web-side contract tests
(`cmd/pilothoused/capability_contract_test.go`,
`cmd/pilothouse/capability_contract_test.go`) assert completeness against a
hand-maintained `capabilityTable`/`capabilityRequirements` list and a
hardcoded total row count. It's tempting to split the work as "add the
broker ID and wire a caller in chunk N, add/update the fixture matrix and
contract-test rows in a later chunk N+k" so the contract-test-authoring work
can be its own reviewable unit.

**What to do:** The contract tests parse `internal/broker/api.go` (or the
web fake broker's capability map) as live source, so the moment a new
`Action*`/`Query*` constant exists — or the moment a web module calls a
broker ID absent from `capabilityRequirements`/`capabilityAnyRequirements`
— `go test ./...` fails until the table row and the hardcoded total are
updated to match. That break is real for every commit between the two
chunks, not just a documentation nicety: this repo's chunks must each pass
gates independently (`make generate`, `gofmt`, `go vet`, `go test`), so a
plan that defers the table/total update to a later chunk cannot produce a
green chunk N. When planning, put "add the new ID" and "update the
completeness table row + total count for that ID" in the same chunk; a
separate later chunk may still add the richer per-fixture test matrix
(canned responses, additional fixtures) without breaking anything, as long
as the base completeness assertion already passes.

**Learned from:** mill run for issue #58, plan review round 1 — the
original plan's c4 (broker registration of `QueryAutoUpdateStatus`) and c5
(web module wiring that call) both deferred the corresponding
`capability_contract_test.go` updates to c6/c7, which the plan reviewer
rejected because the existing live-source contract tests would fail on
every commit between the ID's introduction and the deferred table update.
Caught during plan review before any chunk was implemented, so it never
became a chunk-level gate failure, but the same split is easy to propose
again for any future capability addition.
