# When gating a module by capability, grep for every caller of its methods — not just Mount()'s routes

**When it applies:** Any chunk that adds `platform.CapabilityGate`
(`RequiredCapabilities()`) to a module, or otherwise makes a module's
availability conditional on a `platform.Host.Capabilities()` set.

**What to do:** `platform.Gate` wrapping the routes registered in a module's
`Mount()` only protects requests that arrive through that module's own HTTP
routes. It does nothing for other in-process code that holds a reference to
the module (or one of its interfaces) and calls it directly. This repo has
at least one such aggregator: `internal/modules/attention/module.go`'s
`Module.findings()` iterates every registered `platform.HealthProvider` and
calls `provider.Health(ctx, host)` unconditionally — a plain Go method call,
not a mux route, so wrapping `services`/`backups`/`maintenance`'s `Mount()`
handlers with `platform.Gate` leaves this path completely unguarded. On a
host missing the required capability, `/attention` (and its dashboard card)
still calls `Health()` on the gated provider and renders an "unavailable"
finding linking to a route that 404s — the opposite of the module being
absent entirely.

Before marking a capability-gating chunk complete:

1. `grep -rn` for every call site of the gated module's exported methods and
   every interface it implements (`platform.HealthProvider`, or any other
   cross-module interface), not just its own `Mount()`/`Dashboard()`.
2. For each call site outside the module's own package, add the same
   `CapabilityGate` type-assert-and-check the mechanism already provides
   (see `platform.CapabilityGate`/`platform.Gate` in
   `internal/platform/capability.go`) — e.g. `findings()` must type-assert
   each provider to `platform.CapabilityGate` and skip it (no `Health()`
   call, no finding, no "unavailable" placeholder) when
   `!host.Capabilities(ctx).HasAll(gate.RequiredCapabilities()...)`.
3. Write the test at the aggregator, not just the module: prove that with
   the capability absent, the aggregator never invokes the gated
   provider's method at all (e.g. via a spy/counter), not merely that the
   module's own routes 404.

**Learned from:** mill run for issue #54 failed after `chunk_revise`
rejected the same defect three rounds in a row on the "attention includes a
health provider only when its module's capability is advertised" chunk:
`attention.Module.findings` kept calling `backups.Health`/
`maintenance.Health` directly, unfiltered by the new capability gate, even
after two rounds of targeted feedback — because the gating mechanism built
for chunk-local routes/nav/dashboard was never generalized to this
aggregator's direct method calls.
