# Don't claim work is unified/shared unless the code actually memoizes it

**When it applies:** Writing or revising architecture/overview documentation
(yeti/OVERVIEW.md, docs/*.md) that describes two or more call sites —
typically separate broker queries or HTTP handlers — as deriving from "one
parse," "a single run," or "the same computation" because they call methods
on the same shared manager/struct instance. This is a distinct trap from
temporal overclaiming (see dont-doc-ahead-of-the-chunk.md): here the chunk
is finished and every call site really does exist, but the doc conflates
*sharing an instance* with *sharing a result*.

**What to do:** Before asserting that multiple consumers "share" a
computation, a parse, or an external-command invocation, open the shared
type's method and check for actual memoization (a cached field, a
sync.Once, a TTL, an explicit "compute once at construction" comment). If
the method has no cache and simply re-runs its work (e.g. shelling out to
an external command and re-parsing its output) on every call, then each
caller triggers an independent execution even though they hold the same
`*Manager` pointer — write the doc claim as "each caller invokes the same
manager, which independently re-runs X" rather than "X is parsed once and
the result is shared." This matters most when two different broker
queries/handlers both end up calling the same no-cache method during a
single page render — the two results are not guaranteed to be identical or
atomic with each other, and a doc claiming otherwise will be rejected as an
architecture-grounding defect, not a wording nit.

**Learned from:** mill run for issue #51, chunk 10 (final architecture-doc
chunk, after all mechanism chunks had landed) — round 3 objections on
yeti/OVERVIEW.md:136 and docs/modules.md:333 rejected the claim that
soft-reboot eligibility is "parsed once from `bootc status --json`" and
that one parse feeds `QueryHostImageStatus`, `QueryMaintenanceState`'s
`State`, and the UI. In the actual code, `QueryMaintenanceState` and
`QueryHostImageStatus` are separate broker queries that each independently
call `HostImageManager.Status`, which has no cache and re-runs `bootc
status --json` (and the rpm-ostree equivalent) on every call
(`internal/modules/maintenance/hostimage_manager.go`). The doc had already
survived two earlier revision rounds by narrowing *which surfaces* see
host-image content (a different, already-covered trap) but kept restating
the "single shared parse" claim until round 3, when the run exhausted its
revision budget on this chunk and terminated as failed. Checking for a
cache before writing "computed once" / "shared" / "the same run" would
have caught this on the first pass.
