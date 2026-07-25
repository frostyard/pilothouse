# Before writing "X absent → module gone" prose, check whether that module's gate is HasAll or HasAny

**When it applies:** Writing or editing narrative prose (README.md,
`site/content/**`, `yeti/OVERVIEW.md`, or a paragraph in `docs/modules.md`
*other than* its own per-module completeness-table row) that states the
consequence of one particular capability being absent or unconfigured for a
module's availability, nav entry, dashboard card, or broker registrations.
It's tempting to write the consequence as if every module's gate is a
`HasAll` (every listed capability required, so any single absence removes
the whole module) — that's true for most modules, but not all of them.

**What to do:** Before asserting "without `<capability>`, `<module>`'s nav
entry / dashboard card / routes / broker registration disappear," find that
module's actual `RequiredCapabilities` (`CapabilityGate`, `HasAll`
semantics) or `RequiredAnyCapabilities` (`CapabilityGateAny`, `HasAny`
semantics) implementation, or its row in `docs/modules.md`'s per-module
completeness table — both already document the mechanism in detail. A
`CapabilityGateAny` module survives on *any one* of its listed sources; an
absent single source only removes what that source *alone* backs, not the
module. `internal/modules/sysext` (Extensions) is the concrete example:
gated `HasAny(Updex, Sysext)`. An absent/unconfigured Updex does **not**
remove the Extensions module, its nav entry, its dashboard card, or its
`broker.QueryExtensionsState`/`broker.ActionSysextRefresh` registrations
when systemd-sysext is still reachable — only Updex-specific operations
(enabling/disabling an extension) disappear. Writing the unqualified
AND-shaped sentence for an OR-gated module is factually wrong even though
it reads correctly for every `HasAll`-gated module in the same doc. Qualify
per-source ("only Updex-backed actions disappear; the module itself needs
either Updex or Sysext, not both") or spell out both sources explicitly.
This generalizes past Extensions to any future `CapabilityGateAny` adopter
— check the gate type before generalizing from whichever single capability
the current chunk happens to be about.

**Learned from:** mill run for issue #64 (default-off container engines +
dev-gated mock Fleet). The spec-review gate itself flagged this ambiguity
before chunking began (the spec's "optional modules become explicitly
opt-in" language didn't identify that Extensions/Updex/Sysext is an
any-of relationship, not a uniform per-module flag). It then recurred twice
more in chunk 5's first revision round, in two different files making the
identical category of mistake: `site/content/reference/cli.md:56` claimed
that without Updex, Extensions' `QueryExtensionsState`/nav/dashboard/routes
are all omitted — false whenever Sysext is present — and
`docs/modules.md:130` (a prose paragraph, not the file's own already-correct
`HasAny(Updex, Sysext)` completeness-table row) restated the same false
blanket consequence. Three independent occurrences of the same
HasAny-vs-HasAll confusion across the spec, a site doc, and the very file
that already documents the correct mechanism elsewhere in itself.
