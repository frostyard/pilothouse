# A struct field populated by only one of several independently-failing sources needs its source's Available/Error flag checked, not just its own zero value

**When it applies:** A downstream consumer reads a shared aggregate struct
(e.g. `sysext.Extension`, `HostImageStatus`, `AutoUpdateStatus` — anything
following this repo's per-source `*Available`/`*Error` degrade convention)
and branches on a field that only *one* of the aggregate's several
independently-probed sources ever populates — e.g. `Extension.Enabled` is
set only by the updex-backed half of `sysext.ExtensionsSource.State`, while
`Extension.Merged`/`Installed` come from the systemd-sysext half. It's
tempting to filter on the field directly (`if extension.Merged &&
!extension.Enabled`), because that reads correctly for the common case
where every source answered.

**What to do:** When one source of a multi-source aggregate is absent or
failed, the fields it alone owns are left at their Go zero value — not
because the fact is "known false," but because nothing populated them.
`Enabled: false` from a live updex answer and `Enabled: false` because
updex never ran are byte-identical on the wire; only the aggregate's own
per-source `UpdexAvailable`/`UpdexError` (or the equivalent pair for
whichever source owns the field) tells them apart. Before trusting a field
that belongs to one source in downstream logic, check that source's own
Available/Error pair first, and skip deriving anything from that field when
the source didn't actually answer. Write a test for the partial-degrade
case specifically — one source's `*Available=true` with real data, the
other's `*Available=false`/`*Error` set — not just the all-sources-up and
all-sources-down cases, since the all-up and all-down cases can't
distinguish "false" from "unknown."

**Learned from:** mill run for issue #52 (splitting Extensions from
Maintenance), chunk 1. `maintenance.SystemManager.State` derives a
merged-but-disabled reboot reason from every `sysext.Extension` returned by
`extensionState` using only `extension.Merged && !extension.Enabled`,
without checking `ExtensionsState.UpdexAvailable`/`UpdexError`. When
systemd-sysext succeeds but updex is absent or fails, `Enabled` is left at
its zero value (`false`) for a real merged extension, so the code reports
"disabled but remains active until reboot" for an extension whose enabled
state is actually unknown. The reviewer raised this exact objection three
consecutive rounds (rounds 1, 2, and 3 — the text was materially identical
each time) without the fix ever landing, and the run failed after
exhausting `review_rounds` with this still open.
