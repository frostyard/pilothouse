# Partial-gate modules: audit every view element tied to a gated route, not just the ones spec prose names

**When it applies:** A module that gates *some* routes/actions behind a
capability (`platform.Gate` wrapping specific handlers) while leaving the
module itself, its nav entry, and its main page ungated — as opposed to a
"whole-module" gate where the entire module (and everything it renders)
disappears together. This repo calls this a *partial-gate module*; `storage`
is the current example (`QueryStorageState`/the inventory page is always
present, but the three remote-mount routes need `systemd`).

**What to do:** Whole-module gates can't have this bug — when the module is
absent, its whole view is absent with it. Partial gates can drift: it's easy
to hide the view element the spec calls out by name (e.g. "hide the Add
remote mount link") while missing a sibling element that targets the same
gated route family but wasn't mentioned in prose (e.g. a per-row Delete form
posting to the same gated action). The result is a live-looking
link/button/form pointing at a route that 404s for hosts missing the
capability — exactly the "no broken routes or dead links" failure the
gating work exists to prevent.

Before marking a partial-gate chunk's view changes complete:

1. Identify every route the gate wraps (check the route group, not just the
   handler named in the spec — e.g. `mounts/{id}/{action}` covers mount,
   unmount, *and* delete, even if only "mount" is mentioned by name).
2. `grep` the module's `.templ` files for every link, form, or button whose
   `href`/`action`/`hx-post` targets one of those routes.
3. Collapse *all* of them behind the same gating condition as one unit (a
   single `remoteMountsAvailable`-style boolean), not each hidden
   independently by matching against spec prose — prose is illustrative, the
   route membership is the actual unit of gating.
4. Write the acceptance criteria (or, when reviewing, check the criteria)
   as an explicit enumeration of every view element in scope, not "hide the
   X link" alone.

**Learned from:** mill run for issue #54, plan-review round 1 on chunk c7
(storage remote-mount actions). The first draft hid the Mount/Unmount forms
but left the per-mount Delete form (`broker.ActionStorageDelete`) rendered
next to a route the same chunk gates behind `systemd` — rejected as a
`reject`-severity objection. The plan was revised to collapse the entire
per-mount actions block as one unit, but the run failed (in a later, later
chunk) before c7 was actually implemented, so this generalization was never
exercised in code and would have been lost if not captured here.
