# Reuse a bounded context for the real connection after a capability probe

**When it applies:** Any change where an optional dependency (systemd D-Bus,
a container engine socket, etc.) is first probed with a bounded/timeout
context to decide whether a capability is present, and then — only if
present — a *second*, separate connection is opened later to actually build
the manager(s) that use it.

**What to do:** The probe step exists specifically so an absent/hung
dependency can't block daemon startup; carry that same bounded-context
discipline into the second connection. Reusing `context.Background()` for
the later "real" dial (e.g. `dbus.NewSystemConnectionContext(ctx)` called
again to build long-lived managers) reintroduces exactly the unbounded-hang
risk the probe was added to eliminate: if the capability was reported
present but the bus becomes unresponsive between the probe and the second
dial, startup can block forever instead of degrading to nil-plus-warning.
Give the second connection attempt the same kind of timeout/cancellation the
probe used (or thread the probe's context through), not `context.Background()`.

**Learned from:** mill run for issue #50, chunk 6 — `capability.Probe` dialed
systemd with a bounded context, but `run()` in `cmd/pilothoused/main.go`
called `connectSystemd(context.Background(), caps, ...)` for the follow-up
connection used to build the services/logs/backups managers. Reviewers
flagged this as a high-severity gap (unbounded startup hang on a
present-but-hanging D-Bus), and the chunk exhausted its review rounds before
the fix landed, contributing to the run's overall failure.
