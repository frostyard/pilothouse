# Keep a module's source change and its own doc restatement in the same sweep chunk

**When it applies:** Planning a multi-chunk sweep that touches the same
fact in many places (a branding/wording neutralization, a rename, a
config-key change) where one chunk changes a module's Go/templ source text
and a *different, later* chunk is scheduled to update that same module's
README/`yeti/` wording for the same fact — even when a dedicated
"finish the sweep" chunk is planned at the end to catch stragglers.

**What to do:** When a chunk changes a module's source-level text (a
string literal, a systemd unit description, a template caption), stage
that module's own README/`yeti/` line restating the same fact in the
*same commit*, not a later chunk. AGENTS.md requires documentation to
accompany source changes; a sweep plan that groups "change all the
source" into early chunks and "fix all the docs" into a final chunk makes
every early chunk individually non-mergeable, because each one leaves its
own module's already-changed wording undocumented for however many
chunks intervene before the closer lands. A repo-wide closing sweep chunk
is still the right place for genuinely cross-cutting output (an allowlist
doc, a summary of what changed and why) — but any doc line that is a 1:1
restatement of a fact *this* chunk just changed belongs in this chunk,
not the closer. When a doc file (e.g. README.md) restates the swept fact
in more than one place, track each restatement to the specific chunk that
changes the underlying source, not "the README" as a single later
deliverable.

**Learned from:** mill run for issue #65 (neutral branding sweep), plan
review rounds 1 and 2. Both rounds rejected the plan for the same
underlying reason: chunk c2 changed the sysext module's Go/templ source
wording but deferred the sysext module's own README/yeti restatement to
chunk c3 or c4. The round-1 fix updated the yeti capability-table row, but
round 2 found README lines 3 and 15 still holding the stale sysext
wording — the plan kept treating "the README" as one document to patch in
a single later pass instead of mapping each specific README line to the
specific chunk that changes the fact it restates.
