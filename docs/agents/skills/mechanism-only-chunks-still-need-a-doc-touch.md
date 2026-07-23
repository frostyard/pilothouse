# Mechanism-only chunks still need a doc touch in the same commit

**When it applies:** A chunk introduces a new public type, method, or
primitive (a new `Host` method, a new `CapabilityGate`/`Gate` primitive, a
new exported package API) that isn't yet wired up to any user-visible
behavior — "just plumbing" that a later chunk in the same series will
build on. It's tempting to treat these as exempt from documentation because
nothing observable changed yet.

**What to do:** Stage at least a scoped doc note in the same commit
describing the new primitive's contract (what it does, what calls it, what
future modules must do to use it) in `docs/modules.md` or
`yeti/OVERVIEW.md`, even though the full end-state narrative isn't true
yet. AGENTS.md's "update relevant documentation after any change to source
code" is per source change, not per user-visible feature — a new exported
API is relevant even with zero UI impact. Don't silently defer to "the doc
chunk at the end of the series" by default: if a later dedicated doc chunk
really is the right place for the full narrative, say so explicitly in the
plan for that chunk (and keep this chunk's note narrowly scoped to what it
actually adds, per `dont-doc-ahead-of-the-chunk.md`) rather than shipping
zero doc changes and hoping the reviewer doesn't ask. A one- or
two-sentence addition is cheaper than the revision round that follows an
objection for omitting it entirely.

**Learned from:** mill run for issue #54, chunk 0 (`platform.Host.Capabilities`
web-side fetch/cache plumbing) and chunk 1 (`platform.CapabilityGate`/`Gate`
primitive) — both round-1 reviews rejected the chunk solely for staging zero
documentation changes alongside a new public API, even though neither chunk
was a dedicated "doc chunk" and the new mechanism wasn't yet user-visible.
