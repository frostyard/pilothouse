# A "nil input behaves like X" doc claim must be checked separately for every backend it covers, not just the one it was written from

**When it applies:** Documenting (in a doc comment or a `docs/*.md` file) the
behavior of a manager/aggregator type that reports on two or more
independent backends behind one method (e.g. `AutoUpdateManager.Status`
covering both a bootc updater and an rpm-ostree updater, or any type that
fans out to several subsystems and merges their results). It's tempting to
write one blanket sentence like "a nil client behaves precisely as though
every read failed, Policy falls back to custom/unknown" once, from
whichever backend you were staring at when you wrote it.

**What to do:** Before asserting a blanket claim about what a shared input
(a nil client, a missing file, an empty config) does to the aggregate
result, trace each backend's code path independently. In this run,
`AutoUpdateManager.Status`'s bootc half genuinely zeroes out when the
systemd client is nil, but its rpm-ostree half derives `Policy` from reading
`rpmostreed.conf` on disk — a source that doesn't touch the client at all —
so a nil client leaves rpm-ostree's `Policy` however the config file says,
not `custom/unknown`. The doc comment's claim was true for one backend and
false for the other, and it was asserted identically in both the Go doc
comment (`autoupdate_manager.go`) and the parallel narrative doc
(`docs/autoupdate.md`). The same false claim survived three review rounds
unchanged because each fix reworded nearby text instead of tracing the
actual branch for the backend the claim was wrong about, and a fix to the
code comment didn't get mirrored into the markdown doc making the same
claim. When a reviewer flags a behavioral claim as false, grep for every
place the same claim is restated — including a parallel markdown doc
describing the same type — and verify the corrected claim against the real
per-backend code path, not just against the backend that inspired the
original sentence.

**Learned from:** mill run for issue #58, chunk 2
(`internal/modules/maintenance/autoupdate_manager.go` /
`docs/autoupdate.md`) — the `Status` doc comment's "nil client -> Policy
custom/unknown" claim was rejected as false for the rpm-ostree backend in
round 1, reappeared byte-for-byte identical in round 3, and by round 3 the
same false claim was also found duplicated in `docs/autoupdate.md`. The run
exhausted `review_rounds` (limit 2, actually used 3) with this objection
still open and failed.
