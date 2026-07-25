# Don't require go.sum to appear in the staged diff when promoting an indirect dependency to direct

**When it applies:** A spec/plan acceptance criterion says something like
"go.mod and go.sum are staged in this commit's `git diff --cached --name-status`"
for a chunk whose only module change is running `go mod tidy` after a
dependency that was already indirect (already present in go.sum, already
downloaded, already checksummed) becomes a direct import — e.g. removing the
`// indirect` comment on a line like `gopkg.in/yaml.v3 v3.0.1`.

**What to do:** `go.sum` is a content-addressed ledger keyed by
module+version+hash, not by direct/indirect status. If the module and
version were already resolved and checksummed (which they will be if the
module was already an indirect requirement), promoting it to direct only
rewrites `go.mod` (dropping the `// indirect` marker) and produces **zero**
`go.sum` diff — there is nothing new to checksum. `git add go.sum` in that
state stages a file with no content change relative to HEAD, so
`git diff --cached --name-status` will never list it, no matter how many
times the chunk is resubmitted. Before writing or accepting a criterion that
requires go.sum to appear in a diff, actually run `go mod tidy` against the
change and check `git diff go.sum` (unstaged) first: if it's empty, the
criterion needs to be rewritten to check `go.mod`'s direct/indirect state
(e.g. `go list -m -f '{{.Indirect}}' <module>` prints `false`) and/or
`go mod tidy -diff` reporting clean, not "go.sum is part of the staged file
set." Requiring an empty diff to be staged blocks the chunk forever on a
technicality the acceptance criterion got wrong, not something the agent
implementing it can fix by staging harder.

**Learned from:** mill run for issue #66 (pilothouse packaging), chunk c4
("Configuration-assertion test for .goreleaser.yaml"). The chunk promoted
`gopkg.in/yaml.v3` from indirect to direct (it was already an indirect
dependency, already in go.sum) and its acceptance criteria required "go.mod
and go.sum are staged in this commit's `git diff --cached --name-status`."
`go.mod`'s one-line change (`v3.0.1 // indirect` → `v3.0.1`) was staged
correctly across all three revision rounds, but `go.sum`'s content never
changed relative to HEAD because the module's checksums already existed —
so the criterion could never be satisfied by any staging action, and the
same objection ("go.sum is not staged") recurred byte-for-byte across
rounds 1, 2, and 3 until the run exhausted its gate-attempt limit and
failed.
