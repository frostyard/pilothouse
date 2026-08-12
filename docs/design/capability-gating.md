# Capability probing and gating

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md` — its phase narrative is historical record, see
[../branding.md](../branding.md)). It covers the whole capability system:
daemon-side probing and capability-guarded registration in `pilothoused`
(#50), the web-side capability fetch/cache and gating mechanism plus each
module's adoption (#54), and the explicit opt-in rule for optional tooling
(#64). The binding table mapping every broker ID to its required capability
is [../capabilities.md](../capabilities.md); the module-facing conventions
are in [../modules.md](../modules.md).

## Daemon side: probing and capability-guarded registration

The bullets below continue the "broker is the only privilege boundary"
pattern list in [overview.md](overview.md):

- **Capability probing at startup.** `pilothoused` probes optional host
  capabilities once, early in `cmd/pilothoused/main.go`'s `run()`, before any
  module manager is constructed (`internal/capability.Probe`): systemd,
  journald, `updex`, `systemd-sysext`, bootc, rpm-ostree, the
  `rpm-ostreed-automatic`/`bootc-fetch-apply-updates` automatic-update
  unit-file pairs, the Podman/Docker/Incus engine sockets, and k3s. Every
  individual probe narrows to "absent" on any error rather than failing —
  probing itself is never fatal. Exec-backed probes keep stdout and stderr
  separate: JSON validation consumes stdout only, successful stderr warnings
  are ignored, and a failed command retains trimmed stderr in its returned
  diagnostic. `updex`, Podman, Docker, Incus, and k3s are
  additionally gated on explicit configuration: `--updex`,
  `--podman-socket`, `--docker`, and `--k3s` all default to empty and `--incus`
  defaults to `false`, and an unset value makes
  `ProbeUpdex`/`ProbePodman`/`ProbeDocker`/`ProbeIncus`/`ProbeK3s` report the
  capability absent without running any command, performing any I/O, or
  dialling anything. The "no client is built" half of that holds literally
  for Docker — `probeDocker`'s empty-endpoint guard sits ahead of its
  constructor — but not for Incus: `ProbeIncus` evaluates
  `newIncusProbeClient()` in its call to `probeIncus`, before the `enabled`
  guard, so a disabled probe does allocate that struct. It is a pure
  allocation with no dial and no I/O, and `probeIncus` returns early
  without ever calling its `Server` method. So a host that merely happens
  to have `updex` or `k3s` on
  `PATH`, a socket at the conventional path, `DOCKER_HOST` exported, or a
  live `/var/lib/incus/unix.socket` never enables the tool/engine. Docker's
  non-empty endpoint is also the *only* input its client is built from —
  `ProbeDocker` and `cmd/pilothoused`'s live client both use
  `dockerclient.WithHost(endpoint)`, never `dockerclient.FromEnv`, so the
  SDK's implicit `DOCKER_HOST`/default-socket resolution is never
  consulted. Incus is the opposite shape: its socket path is *not*
  configurable — it stays fixed at `/var/lib/incus/unix.socket` — so
  `capability.Config.IncusEnabled` is a plain bool carrying `--incus`, and
  `ProbeIncus(ctx, false)` returns an empty set before its client's
  `Server` call is ever reached. The resulting `capability.Set` is not
  cached or re-probed later; a daemon restart re-probes from scratch. It is
  advertised over the fixed, authenticated, non-admin
  `org.frostyard.pilothouse.capabilities.list` query
  (`broker.QueryCapabilities`), returning `{"capabilities": [...]}` —
  present capabilities only, sorted, canonical IDs — and restart re-probes
  from scratch (nothing is cached). The same `capability.Set` gates
  privileged registration: see `docs/capabilities.md` for the binding
  table mapping every broker ID to its required capability, and
  `docs/modules.md`'s "Capability-guarded registration" section for the
  convention new modules follow. `registerPodman`/`registerDocker`/
  `registerIncus`/`registerK3s` are full conversions — each takes `caps
  capability.Set` and registers nothing for its engine when the
  corresponding capability is absent. No engine state is ever a fatal
  `run()` error, but only one of them is warned about. A *configured but
  unreachable* engine degrades silently: `ProbeDocker` narrows to absent on
  a `Ping` failure without logging anything, and `registerDocker` then
  registers nothing. The single warnable case is a Docker client that fails
  to construct from a configured `--docker` endpoint (e.g. a malformed
  endpoint), which `connectDocker` logs as a warning before returning nil.
  An *unconfigured* engine — `--docker` left empty — is not a warnable
  condition either, so `connectDocker` returns nil silently and no client
  is built. Podman's and Incus's client constructors stay
  unconditional in `run()` because neither performs I/O at construction;
  for Incus, `--incus` acts entirely through the probe, so
  `registerIncus`'s `caps.Has(capability.Incus)` guard is what leaves the
  engine with no registered query or action when the flag is false.
  `registerServices` and
  `registerLogs` are the next conversions: `registerServices` guards
  `QueryServicesState` and every services lifecycle action on
  `caps.Has(capability.Systemd)`, and `QueryServicesJournal` separately on
  `caps.HasAll(capability.Systemd, capability.Journald)` — guarded
  individually per `docs/capabilities.md`'s corrected mapping, so a host
  with systemd but no journald still gets full service management with
  only the journal query withheld; `registerLogs` guards its single
  `QueryLogs` registration on that same `caps.HasAll(capability.Systemd,
  capability.Journald)`. `registerStorageActions` and `registerBackups` are
  the next conversions: both guard their whole function on
  `caps.Has(capability.Systemd)` alone (every remote-mount action generates
  or controls systemd units, and backups monitors systemd timers, so
  neither has a services-style mixed per-call requirement); their
  nil-manager check is retained alongside the capability check as a
  defensive backstop for directly-injected test fakes. `QueryStorageState`
  itself, registered separately against the plain, non-systemd
  `storageManager`, remains unconditional per `docs/capabilities.md`'s
  documented exception. The one live systemd D-Bus connection is opened
  through `connectSystemd` with a five-second *wait* bound, but its godbus
  lifetime uses a separately cancellable context: godbus closes a connection
  when its construction context ends, so attaching the five-second context
  would make every systemd-backed manager fail shortly after startup. A
  timeout cancels the connection attempt, a connector that still returns late
  has its connection closed, and a successful connection retains the context
  until daemon shutdown.
- **Maintenance: guarded registration plus a real handler-level degrade.**
  `registerMaintenance` (`cmd/pilothoused/main.go`) is the next conversion:
  it takes the probed `capability.Set` and no-ops both
  `QueryMaintenanceState` and `ActionMaintenanceReboot` when `systemd` is
  absent, exactly like `registerBackups`/`registerStorageActions`.
  `maintenance.NewSystemManager` has no D-Bus dependency of its own (it
  depends only on the sysext extensions source, job store, and command
  runner), so unlike backups/services/logs there is no construction-level
  non-fatal-startup fix to make here; the manager is always constructed
  regardless of systemd, and the registration guard above is the only thing
  withholding it. Separately — and this is the real behavioral change —
  `maintenance.SystemManager.State`'s extension-read subpath never fails the
  query. As of #52 `extensionState` makes one call,
  `sysext.ExtensionsSource.State(ctx, updexAvailable, sysextAvailable)`,
  passing through the two capability facts `cmd/pilothoused/main.go` probes
  as `caps.Has(capability.Updex)`/`caps.Has(capability.Sysext)`. That
  interface owns both the never-attempt rule (a tool whose flag is false is
  never invoked) and the never-hard-error rule (a source whose command
  *fails* sets only its own `UpdexAvailable`/`UpdexError` or
  `SysextAvailable`/`SysextError` pair in the returned `ExtensionsState`,
  leaves the other source intact, and returns no error), so `extensionState`
  inherits the degrade for free rather than branching per capability itself;
  the contractually-unused error result is handled the same way
  `hostImageState` handles a failed host-image read — drop this call's
  contribution, cache nothing, carry on. This closes spec resolution 3's
  requirement: an extension provider that errors costs the extension-derived
  reboot reasons and nothing else, so `QueryMaintenanceState` stays a 200
  instead of 5xx-ing as it did when the subpath propagated `Check`/`List`
  failures. `Jobs`, `OSVersion`, the OS-marker and completed-job reasons, and
  the staged-host-image reason are computed exactly as before regardless.
  From the returned `Extensions` slice maintenance derives exactly one thing —
  a merged extension *known* to be disabled becomes "<name> is disabled but
  remains active until reboot." — and nothing else. "Known" is the load-bearing
  word: `Enabled` is populated only by updex and `Merged` only by
  systemd-sysext, so `mergedButDisabledReasons` reads each field only when its
  own source's `*Available`/`*Error` pair says that source actually answered,
  and additionally requires `Managed`, since an extension merged through
  systemd-sysext with no updex definition has an `Enabled` nobody set. A plain
  `Merged && !Enabled` filter would report every merged extension as disabled
  on a host without updex and would newly over-report unmanaged ones — the
  `Managed` guard is what holds this reason identical to the pre-#52
  `List()`-based behavior, which only ever iterated updex's own feature list.
  Beyond that reason, `State.Updates`, the "Available updates" page section,
  the Summary card's "Updates" mini-row, and the `maintenance.updates` Health
  finding are all gone, update availability being Extensions' to own. The source is shared with `registerExtensions`
  as one daemon-internal *instance*, not one *result*:
  `extctl.SystemManager.State` has no cache, so the two consumers each run
  their own read; maintenance's own 1-minute `extensionState` cache is the
  only cache involved and is this manager's alone. See
  `docs/capabilities.md`'s extension-read note for the full mechanism and
  `internal/modules/maintenance/manager_test.go` for the per-combination
  cases, including one driving the real `*extctl.SystemManager` with a
  failing command runner. (`NewSystemManager` also carries a
  `hostImage`/`bootcAvailable` pair for the host-image leg described in
  [host-image.md](host-image.md); it follows the same degrade convention and leaves the
  extension behavior here untouched.)
- **Sysext: the one module guarded per-action, not per-function.**
  `registerSysextActions` (`cmd/pilothoused/main.go`) is the final capability
  conversion in this phase, and the only one where the four registrations
  don't share a single requirement: `ActionSysextDisable`/`ActionSysextEnable`
  (registered together via the shared `registerNamedActions` helper) require
  `updex AND sysext` together, so that pair is guarded as one group;
  `ActionSysextRefresh` requires `sysext` alone and `ActionSysextUpdate`
  requires `updex` alone — those two already lived in a separate local loop,
  so each entry there now carries its own required capability, checked
  in-loop, without changing `registerNamedActions`/`registerProjectActions`
  (every other caller has a uniform per-call requirement). `extctl.NewSystemManager`
  has no systemd D-Bus dependency (exec/`CommandRunner`-based only), so — like
  maintenance — there is no construction-level non-fatal-startup fix needed;
  `sysextManager` is constructed unconditionally regardless of capability, and
  the per-action registration guards above are what withhold each action. See
  `docs/capabilities.md`'s sysext rows and module-level-defaults section for
  the full per-action table.
- **Web-side capability fetch/cache.** `internal/web.Server` keeps its own
  opportunistically-refreshed view of the broker's advertised
  `capability.Set`, separate from `pilothoused`'s own probe/advertise cycle
  above. `internal/web/capabilities.go`'s
  `capabilityCache` (a field on `Server`, zero-value valid) holds the last
  fetched `Set` plus a `down` flag; `Server.Capabilities(ctx
  context.Context)` (added to the widened `platform.Host` interface so both
  HTTP handlers and `Module.Dashboard(ctx, host)` can call it) returns
  whatever is cached, or the zero (all-absent) `Set` before any successful
  login or fetch. `Server.refreshCapabilities(ctx, token)` issues a
  `broker.QueryCapabilities` query under its own 2s bounded context derived
  from the caller's `ctx` (never `context.Background()`, per the
  reuse-bounded-context lesson from #50) and is wired at exactly two
  checkpoints: right after a successful `login`, and in the `authenticate`
  middleware after `Session()` succeeds, but only when the cache is
  `staleAfterOutage()` — i.e. only the first authenticated request after a
  prior `broker.ErrUnavailable`-wrapped failure triggers a refetch, not
  every request. `Session()`'s own transport-failure branch, and the
  `Query`/`Execute`/`StreamAction`/`StreamQuery` wrapper methods, all call
  `capabilityCache.noteResult(err)` after their underlying broker call to
  mark the cache down on an `ErrUnavailable`-wrapped error; none of them
  ever clear the flag or trigger a refetch themselves — only the two
  checkpoints above do that, so one request never issues more than one
  capability refetch. Authorization failures, request-validation errors,
  and arbitrary domain errors never mark the cache down or trigger a
  refetch. `capability.Set` gained `UnmarshalJSON` (mirroring the existing
  `MarshalJSON`) to decode this query's `{"capabilities": [...]}` response.
- **Whole-module web-side capability gating (mechanism only).**
  `internal/platform/capability.go` adds the primitives every later
  capability-gated module will use, on top of the web-side capability
  fetch/cache above: `CapabilityGate` is an interface
  (`RequiredCapabilities() []capability.ID`) a `Module` optionally
  implements to declare that its whole surface (nav entry, dashboard cards,
  routes) needs some set of host capabilities present (`Set.HasAll`
  semantics); a `Module` that does not implement it has no capability
  requirement and is available whenever it is registered — the default for
  `system`/`files`/`activity`/`fleet` and storage's own inventory reads.
  (Registration is a separate switch from capability gating: `fleet` carries
  no `CapabilityGate`, but `cmd/pilothouse` only registers it under `--dev`,
  so on a production process it is absent from the registry entirely rather
  than gated within it.) `Gate(host Host, ids []capability.ID,
  next http.HandlerFunc) http.HandlerFunc` wraps a `Mount`-registered
  handler so the route itself stays mounted on the shared mux, but 404s at
  request time when `host.Capabilities(ctx)` doesn't `HasAll(ids...)` —
  this is what "routes stay mounted, capability absence 404s instead of
  changing the mux" means concretely for a module's `Mount`. A second,
  exported function, `Available(module Module, caps capability.Set) bool`,
  applies the same `CapabilityGate`-or-default-available test to a whole
  module rather than a single request — it type-asserts `CapabilityGate`
  and defaults to available when a module doesn't implement it, exactly
  mirroring the check `Gate` makes per-request. `internal/web/server.go`
  wires the interface (not `Gate`, which individual modules call from their
  own `Mount`) into the two web-side registries the spec calls out: an
  unexported `moduleAvailable(module platform.Module, caps capability.Set)
  bool` delegates the gating decision to `internal/platform` rather than
  reimplementing it — in this chunk that was `platform.Available` alone; the
  next bullet's any-of work changed the body to
  `platform.Available(module, caps) && platform.AvailableAny(module, caps)`,
  and it remains the single choke point both web-side registries call, with
  each half implemented once in `internal/platform` and shared with that
  package's own tests — and `Render` now builds the shell's `Modules` nav list from a new
  `s.availableManifests(ctx)` (filters `s.registry.Modules()` through
  `moduleAvailable` before mapping to `Manifest`, replacing the previous
  unfiltered `s.registry.Manifests()` call) and the `dashboard` handler's
  per-module loop skips a capability-gated-absent module entirely — no
  `Dashboard()` call, no card, no error-card placeholder, since an
  unavailable surface is not rendered at all, not shown degraded. `Mount()`
  at server construction (`internal/web/server.go`, around where the
  registry's modules are wired to the mux) stays unfiltered: every module's
  routes remain mounted regardless of capability, per the "routes stay
  mounted" requirement above; only the nav list and the dashboard loop are
  filtered by `moduleAvailable`. No production module implemented
  `CapabilityGate` in this chunk — the mechanism was proven with a
  synthetic fake module in `internal/platform/capability_test.go` (which
  exercises `Available` through a fake `Host`'s real `Capabilities()`
  method, not a capability.Set passed in directly, so the test covers the
  same `Host`-integration boundary the production code depends on) and
  `internal/web/server_test.go`, and every real module's
  nav/dashboard/route behavior was unchanged. `services` is the first real
  module to adopt it — see the next bullet.
- **`HasAny`/`CapabilityGateAny`/`GateAny`/`AvailableAny`: an any-of sibling
  (added as mechanism by #54, adopted by `maintenance` in #51).**
  `internal/capability.Set` gained `HasAny(ids ...ID)
  bool`, reporting true iff at least one given id is present; unlike
  `HasAll`'s zero-ids case (vacuously true), `HasAny()` with zero ids is
  always false ("any of nothing" has no capability to satisfy), and a
  nil/zero-value `Set`'s `HasAny` is nil-safe like `Has`/`HasAll`.
  `internal/platform` mirrors `CapabilityGate`/`Gate`/`Available` with a
  parallel any-of trio — `CapabilityGateAny` (`RequiredAnyCapabilities()
  []capability.ID`), `GateAny(host, ids, next)`, and `AvailableAny(module,
  caps)` — kept as separate types rather than folding an any-of flag into
  `CapabilityGate`, since no module needs both AND and OR semantics on its
  whole-module gate at once. `moduleAvailable` now composes both:
  `platform.Available(module, caps) && platform.AvailableAny(module,
  caps)`. Because `Available` defaults to `true` for a module that doesn't
  implement `CapabilityGate` and `AvailableAny` defaults to `true` for a
  module that doesn't implement `CapabilityGateAny`, this
  AND-of-two-defaults composition is correct for all three shapes a module
  can be in (`CapabilityGate` only, `CapabilityGateAny` only, or neither)
  with no type-switching in `server.go` itself. The one other place that
  gates a module's surface outside `Mount`/nav/dashboard —
  `internal/modules/attention.Module.findings`, which calls
  `HealthProvider.Health` directly — was updated in the same chunk to
  type-assert `CapabilityGateAny` alongside `CapabilityGate` and skip a
  provider when either gate is unsatisfied, so a future `CapabilityGateAny`
  module can't be hidden from nav/dashboard and 404 on its routes while
  `/attention` still calls its `Health`. No production module implemented
  `CapabilityGateAny` in this chunk — the mechanism was proven the same
  way `CapabilityGate` was before its first real adopter: a synthetic fake
  module in `internal/platform/capability_test.go` (exercising `AvailableAny`
  through a fake `Host`'s real `Capabilities()`) and a synthetic fake module
  registered into a real `*web.Server` in `internal/web/server_test.go`
  proving nav/dashboard/route behavior through a real registry and HTTP
  round trip. #51 then made `maintenance` the first production adopter, with
  `RequiredAnyCapabilities()` returning `{Systemd, Bootc, RPMOStree}`; see the
  Maintenance bullet below for the per-surface split that adoption produced.
  #52 added the second, `sysext` → `{Updex, Sysext}`; see the sysext bullet
  below.
- **Services module: the first real `CapabilityGate` adopter.**
  `internal/modules/services.Module` now implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Systemd}` — so the whole module (nav entry,
  dashboard card, and future `Health` inclusion) is available only when the
  web process's cached `capability.Set` has `Systemd`, matching #50's daemon-
  side `registerServices` gating. Each route `services.Module.Mount`
  registers is individually wrapped in `platform.Gate`: `GET /services` and
  `POST /services/{unit}/{action}` require only `{capability.Systemd}`;
  `GET /services/{unit}/logs` requires `{capability.Systemd,
  capability.Journald}` (`Gate`'s `HasAll` semantics cover the AND), so a
  host with `Systemd` but not `Journald` keeps full service state and
  lifecycle control while the journal sub-feature 404s. `views.templ`'s
  `Page(...)` takes a new `journalAvailable bool` parameter and only renders
  the per-unit `Logs` link when it is true; the `GET /services` handler in
  `module.go` derives it from `host.Capabilities(r.Context()).Has(capability.Journald)`
  (Systemd is already guaranteed true inside a `Gate`-wrapped handler, so no
  redundant check is needed there). `module_test.go`'s `testHost` gained a
  configurable `caps capability.Set`/`capsSet bool` pair (defaulting to a
  full-capability set matching the pre-#54 behavior) so tests can exercise
  Systemd-present/-absent and Journald-present/-absent independently via
  real `ServeMux` round trips through `Mount`, rather than calling handler
  logic directly.
- **Backups: whole-module `Systemd` gate.**
  `internal/modules/backups.Module` now also implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Systemd}` — unlike services, it has no
  sub-feature with a broader requirement, so there is exactly one
  `platform.Gate(host, []capability.ID{capability.Systemd}, ...)` wrap, on
  its single `GET /backups` route. With `Systemd` absent, the whole module
  disappears — nav entry, dashboard card, and the route 404s at request
  time; with `Systemd` present, the module behaves exactly as before this
  chunk. Its `views.templ` did not change: an absent module 404s before any
  page renders, so there is no conditional view content to add, unlike
  services' `journalAvailable` parameter. `module_test.go` gained the same
  configurable `caps capability.Set`/`capsSet bool` pair on its fake `Host`
  that services' test uses (defaulting to a full-capability set), so
  gated/ungated route behavior is exercised via real `ServeMux` round trips
  through `Mount`.
  `platform.Gate`/`Available` only guard requests that arrive through a
  module's own `Mount`-registered routes or the web-side nav/dashboard
  loops, though — they do nothing for other in-process code that holds a
  `platform.HealthProvider` reference and calls `Health` directly.
  `internal/modules/attention.Module.findings` is exactly that: it iterates
  every registered provider (including `backupModule`, passed into
  `attention.New(...)` in `cmd/pilothouse/main.go`) and previously called
  `provider.Health(ctx, host)` unconditionally, so a `Systemd`-absent host
  still reached `QueryBackupsState` through `/attention` and
  rendered a degraded "status is unavailable" finding instead of the
  provider being absent entirely. `findings` now type-asserts each
  provider to `platform.CapabilityGate` and, when the host's cached
  `capability.Set` doesn't `HasAll` its `RequiredCapabilities`, skips it
  outright — no `Health` call and no "unavailable" finding, since an absent
  module is not the same as one whose status collection failed. This is
  the same `CapabilityGate` type-assert-and-check `Gate`/`Available`
  already apply, generalized to this aggregator's direct method calls;
  `internal/modules/attention/module_test.go` proves it with a
  Health-call-counting fake provider, at both the absent- and
  present-capability ends. (The any-of bullet above later extended the same
  skip to `platform.CapabilityGateAny`/`HasAny`; see "Attention's
  per-provider capability skip" in the current-state section for the
  composed behavior.)
- **Maintenance: whole-module `HasAny(Systemd, Bootc, RPMOStree)` gate
  (reworked by #51).** `internal/modules/maintenance.Module` adopted #54's
  whole-module `Systemd` `CapabilityGate` alongside backups; #51 replaced
  it. `module.go` now implements `platform.CapabilityGateAny`
  (`RequiredAnyCapabilities()` returning `[]capability.ID{capability.Systemd,
  capability.Bootc, capability.RPMOStree}`), **not**
  `platform.CapabilityGate`, because the module reports on two independent
  sources: systemd-gated reboot posture and jobs
  (`QueryMaintenanceState`), and separately gated host-image status
  (`QueryHostImageStatus`, `Bootc OR RPMOStree`). `GET /maintenance` is
  wrapped in `platform.GateAny(host, {Systemd, Bootc, RPMOStree}, ...)`;
  `POST /maintenance/reboot` remains wrapped in a separate, plain
  `platform.Gate(host, {Systemd}, ...)`, unchanged, since rebooting is a
  systemd operation and `ActionMaintenanceReboot` is registered only under
  `Systemd`. So: with none of the three capabilities present the whole
  module disappears (no nav entry, no dashboard card, `GET /maintenance`
  404s); with any one of them present the nav entry, dashboard card, and
  `GET /maintenance` are available, while `POST /maintenance/reboot` remains
  available only when `Systemd` specifically is present, regardless of
  bootc/rpm-ostree. Each broker call the module makes is independently
  capability-gated so no newly-possible capability combination turns an
  available module into an error: `queryState` calls
  `QueryMaintenanceState` only when the host advertises `Systemd` and
  substitutes the zero `State` otherwise, so `Page`, `Summary`, and `Health`
  degrade to "nothing to report" on a bootc-only host rather than the
  handler 503ing (or the dashboard card/`attention` finding erroring) on a
  query the daemon never registered there; symmetrically, `queryHostImage`
  calls `QueryHostImageStatus` only when the host advertises
  `HasAny(Bootc, RPMOStree)` and returns `nil` otherwise, which is what the
  page renders from. `views.templ`'s `Page` therefore takes a
  `hostImage *HostImageStatus` alongside `state`, and nil-ness *is* the
  availability flag — the page can only render host-image content it actually
  fetched. When it is non-nil, `Page` renders the conditional
  `hostImageSection` (unexported — `Page` is its only caller): the booted,
  staged, and rollback deployments bootc reported (image
  reference and digest, plus rpm-ostree's supplementary version and checksum
  where the broker-side merge attached them), an independent per-source
  unavailable indicator for each of `BootcError` and `RPMOStreeError` so one
  failed source never hides the other's data, and — exactly once on the whole
  page — the soft-reboot-eligibility indicator, read straight from
  `HostImageStatus.SoftRebootCapable` (non-nil true renders "a soft reboot may
  be sufficient…", non-nil false "a full reboot is required…", nil renders
  nothing). That indicator is gated on `HasAny(Bootc, RPMOStree)`, **not** on
  `Systemd`, so it renders identically on a bootc-only host and a
  bootc-plus-systemd one; it is deliberately not duplicated inside the
  `Systemd`-gated reboot-posture area, which still renders only the
  pre-existing reboot-required card and reboot form.
  `State.SoftRebootCapable` remains the same fact's API-surface leg on
  `QueryMaintenanceState`'s full posture response, it is just not the page's
  rendering source. With neither bootc nor rpm-ostree advertised the section
  is omitted outright rather than rendered empty or errored. Nothing in the
  section is a control: no upgrade, switch, rebase, rollback, or
  automatic-update link, button, or form exists anywhere on the page for
  bootc or rpm-ostree, and `views_test.go` asserts that mechanically across
  every host-image fixture. The dead-control audit a
  whole-module-present/route-gated combination normally demands is unchanged
  by all of this: the only view element targeting the systemd-only reboot
  route is still the admin "Reboot host" form nested inside
  `if state.RebootRequired`, and the zero `State` substituted when `Systemd`
  is absent leaves that condition false, so the form cannot render on a host
  where the route 404s (`TestPageRendersNoRebootControlWithoutSystemd` pins
  both ends of that).
  The page's "Automatic updates" section (#60) is the exact same shape one
  level over: `queryAutoUpdate` calls `QueryAutoUpdateStatus` only under
  `HasAny(Bootc, RPMOStree)` — the same gate as `queryHostImage`, never the
  `Autoupdate*` pair, which would 404 the query on precisely the no-updater
  host whose "not configured" report is the point — and returns `nil`
  otherwise, so `Page(state, hostImage, autoUpdate, csrf, admin)` renders the
  unexported `autoUpdateSection` (again, `Page` is its only caller) only when
  `autoUpdate != nil`, and a host advertising neither source omits the section
  outright rather than rendering it empty
  (`TestPageOmitsAutoUpdateSectionWithoutAnySource`). Non-nil is not the same
  as populated: within the section, each updater's subsection is chosen by that
  updater's `BootcConfigured`/`RPMOStreeConfigured` flag paired with its payload
  pointer — which the daemon sets from the probed
  `AutoupdateBootc`/`AutoupdateRPMOStree` capabilities — so a plain bootc or
  rpm-ostree host with no updater units gets a non-nil, zero-value response and
  renders two explicit, updater-naming "not configured" statements, never a
  blank or hidden subsection. A configured updater renders its timer active and
  unit-file states, next trigger, service active state and last result,
  normalized policy, and both drop-in-presence booleans; the two subsections are
  independent, so one updater being unconfigured never hides the other's detail.
  Because the section's gate is `HasAny(Bootc, RPMOStree)` and not `Systemd`, it
  renders identically on a bootc-only host with no systemd
  (`TestPageRendersAutoUpdateSectionWithoutSystemd`). Like the host-image
  section, it contains **no control of any kind** — no link, button, form, or
  HTMX request that enables, disables, triggers, or reconfigures either updater,
  and no broker action exists for one to target
  (`TestPageExposesNoAutoUpdateMutationControl` asserts it across all four
  payload combinations).
  Because the module's whole-module gate is
  now an any-of gate, `attention.Module.findings` reaches it through the
  `platform.CapabilityGateAny`/`AvailableAny` (`HasAny`) type-assert rather
  than the `platform.CapabilityGate` (`HasAll`) one, so maintenance is
  skipped there — no `Health` call, no "unavailable" placeholder — only when
  a host has none of the three. Maintenance's existing extension-read
  degrade (`QueryMaintenanceState`'s updex/sysext handling, from #50) is
  untouched by either chunk: the capability gating sits on top of it, at the
  module/route level, not inside the query handler.
- **Logs: whole-module `Systemd AND Journald` gate.**
  `internal/modules/logs.Module` now implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Systemd, capability.Journald}` — matching
  `docs/capabilities.md`'s `QueryLogs` exception (the manager resolves units
  via the systemd D-Bus client before reading journal entries, so the
  module needs both, not journald alone). Its single route,
  `GET /logs`, is wrapped with `platform.Gate(host,
  []capability.ID{capability.Systemd, capability.Journald}, ...)` in
  `internal/modules/logs/handler.go`; with either capability absent the
  whole module disappears — nav entry and the route 404 at request time —
  and with both present it behaves exactly as before this chunk. Unlike
  services, logs has no sub-feature with a narrower requirement, so there
  is exactly one `Gate` wrap. `logs.Module.Dashboard` already returns
  `(nil, nil)` unconditionally and logs is not a `platform.HealthProvider`
  (see the module table in [overview.md](overview.md)), so no dashboard or `attention` aggregator
  change was needed here, unlike backups/maintenance. `module_test.go`
  gained the same configurable `caps capability.Set`/`capsSet bool` pair
  on its fake `Host` that services/backups/maintenance use (defaulting to
  a full-capability set), so gated/ungated route behavior — including the
  systemd-only and journald-only partial cases — is exercised via real
  `ServeMux` round trips through `Mount`. `platform.Available` is also
  exercised directly against the module's `RequiredCapabilities` as a
  unit-level check, but the nav claim itself is proven end-to-end:
  `TestLogsNavEntryFollowsCapabilityGateEndToEnd` builds a real
  `internal/web.Server` (via `platform.NewRegistry(New())`, the same
  constructor path `cmd/pilothouse` uses) backed by a fake broker, drives an
  actual `POST /login` then `GET /` through `server.Handler()`, and asserts
  the rendered dashboard HTML omits `href="/logs"`/`Logs` when either
  capability is missing and includes them when both are present — so the
  nav-filtering predicate (wired generically in `internal/web/server.go`
  since c2) is confirmed against this real module's adoption, not just a
  synthetic gated module or a direct `platform.Available` call.
- **Storage: route-level `Systemd` gate, not a whole-module gate.**
  `internal/modules/storage.Module` deliberately does *not* implement
  `platform.CapabilityGate` — its nav entry, dashboard card, and
  `GET /storage` inventory page stay available regardless of `Systemd`,
  matching `docs/capabilities.md`'s `QueryStorageState` exception (the
  daemon-side `registerStorageActions`/`registerBackups` split from #50).
  Only the three remote-mount routes in `internal/modules/storage/module.go`
  — `GET /storage/mounts/new`, `POST /storage/mounts`, and
  `POST /storage/mounts/{id}/{action}` (which covers mount, unmount, *and*
  delete) — are individually wrapped in `platform.Gate(host,
  []capability.ID{capability.Systemd}, ...)`. This is the one module in the
  phase where a capability gate is scoped to a subset of routes rather than
  the module's whole surface, so the corresponding view had to be audited
  for every element targeting one of those three routes, not just the ones
  named in the spec by example: `views.templ`'s `ManagedPage`/
  `ManagedSnapshotRegion`/`ManagedMountTable` all gained a sibling
  `remoteMountsAvailable bool` parameter (alongside the existing `admin
  bool`), and `ManagedMountTable` collapses the *entire* per-mount
  `<div class="actions">` block — Mount, Unmount, and Delete together — on
  that one flag, evaluated once before the per-state Mount/Unmount `if`s,
  rather than hiding each form independently; `ManagedPage` also omits the
  "Add remote mount" link on the same flag. `module.go`'s `GET /storage`
  handler derives the flag from
  `host.Capabilities(r.Context()).Has(capability.Systemd)` and passes it to
  `ManagedPage` alongside the existing `admin` argument. With `Systemd`
  absent, storage inventory/capacity/findings keep rendering exactly as
  before, but no link, form, or button anywhere on the page still points at
  one of the now-404ing remote-mount routes; with `Systemd` present,
  rendering is byte-for-byte unchanged from before this chunk.
  `storage/module_test.go`'s fake `Host` gained the same configurable `caps
  capability.Set`/`capsSet bool` pair the other gated modules' tests use
  (defaulting to a full-capability set), and a dedicated test asserts
  `storage.Module` does *not* satisfy `platform.CapabilityGate` while
  `platform.Available` still reports it available under a no-`Systemd`
  fixture — the two assertions together are what "storage stays in c2's
  available-modules filter" means concretely for a partial-gate module.
- **Podman and docker: whole-module engine-capability gates.**
  `internal/modules/podman.Module` and `internal/modules/docker.Module` now
  implement `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Podman}` and `[]capability.ID{capability.Docker}`
  respectively — matching `docs/capabilities.md`'s one-capability-per-engine
  mapping and #50's daemon-side `registerPodman`/`registerDocker` gating.
  Each module has the same four-route shape (state page, container logs,
  container action, image action), and every one is wrapped in
  `platform.Gate(host, []capability.ID{capability.Podman|Docker}, ...)` in
  the module's own `Mount`: `GET /podman`/`GET /docker`,
  `GET /{podman,docker}/containers/{id}/logs`,
  `POST /{podman,docker}/containers/{id}/{action}`, and
  `POST /{podman,docker}/images/{id}/{action}`. Neither module has a
  sub-feature with a broader or narrower requirement (unlike services'
  journal split), so there is exactly one `Gate` wrap per route, all sharing
  the module's single capability. With the engine capability absent, the
  whole module disappears — nav entry, dashboard card, and all four routes
  404 at request time — while the sibling engine and the rest of the app are
  unaffected; with the capability present, both modules behave exactly as
  before this chunk. Neither module's `views.templ` changed: an absent
  module 404s before any page renders, so there is no conditional view
  content to add, the same as backups/logs (maintenance's `views.templ`
  gained conditional host-image content in #51; see the Maintenance bullet
  above for its now-different behavior). Neither module is a
  `platform.HealthProvider` (see the module table in [overview.md](overview.md)), so no `attention`
  aggregator change was needed here either. Both `module_test.go` files
  gained the same configurable `caps capability.Set`/`capsSet bool` pair on
  their fake `Host` that the other gated modules' tests use (defaulting to a
  full-capability set), so gated/ungated route behavior — and that gating
  one engine leaves the other engine's routes and the rest of the mux
  unaffected — is exercised via real `ServeMux` round trips through `Mount`.
- **Incus: whole-module engine-capability gate.**
  `internal/modules/incus.Module` now implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Incus}` — the same one-capability-per-engine
  mapping podman and docker use, matching `docs/capabilities.md` and #50's
  daemon-side `registerIncus` gating. At #54 incus had exactly three routes,
  all wrapped in `platform.Gate(host, []capability.ID{capability.Incus}, ...)`
  in the module's own `Mount`: `GET /incus`,
  `POST /incus/instances/{name}/{action}`, and
  `POST /incus/images/{fingerprint}/{action}`. (The instance-depth phase has
  since added four read-only routes, two snapshot routes and a creation
  route behind the same gate — see [incus.md](incus.md) — so the module
  now has ten.) With incus
  absent, the whole module disappears — nav entry, dashboard card, and every
  one of those routes 404s at request time — while podman, docker, and the
  rest of the app are
  unaffected; with incus present, the module behaves exactly as before this
  chunk. `views.templ` needed no capability-conditional content: an absent
  module 404s before any page renders, so there is nothing to add, the same as
  podman/docker/backups/logs (maintenance's `views.templ` gained conditional
  host-image content in #51; see the Maintenance bullet above for its
  now-different behavior). Incus is not a
  `platform.HealthProvider` either, so no `attention` aggregator change was
  needed. `module_test.go` gained the same configurable
  `caps capability.Set`/`capsSet bool` pair on its fake `Host` that the other
  gated modules' tests use (defaulting to a full-capability set), so
  gated/ungated route behavior — and that gating incus leaves the rest of
  the mux unaffected — is exercised via real `ServeMux` round trips through
  `Mount`.

## Web-side capability gating (end state, #54)

Several bullets above narrate individual pieces of #54 (phase 1b of the #35
decomposition, per `docs/capabilities.md`) as they landed — the web-side
fetch/cache, the gating mechanism, and each adopting module. This subsection
is the consolidated end-state contract for the whole issue. The unprivileged web
process (`cmd/pilothouse`) derives its navigation, dashboard cards, routes,
and actions from the broker's advertised `capability.Set`, so a host missing
optional tooling never shows a dead link or a button that always fails.

- **Capability fetch/cache lifecycle** (`internal/web/capabilities.go`,
  `internal/web/server.go`). `broker.QueryCapabilities` is an *authenticated*
  query, so the set cannot be fetched before login. `Server.refreshCapabilities`
  fetches it (1) on each successful `login`, once a session token exists, and
  (2) in the `authenticate` middleware on the first successful authenticated
  request *after* a broker transport/unavailable failure — the cache's `down`
  flag is set by `capabilityCache.noteResult`, which the `Session` branch of
  `authenticate` and the `Query`/`Execute`/`StreamAction`/`StreamQuery`
  wrappers call after their underlying broker call whenever the error wraps
  `broker.ErrUnavailable` (as does `refreshCapabilities` itself on a failed
  fetch, so an outage that also swallows the refetch stays marked and is
  retried on the following request). Only `staleAfterOutage()` triggers a
  refetch, at most one per request, and it runs inside `authenticate` *after*
  that request's own `Session()` validation has already succeeded — not at the
  literal top of the handler, and never for `publicPath` requests, which skip
  the authenticated branch entirely. It is **never fetched pre-login**
  (the login page needs no capabilities; `Server.Capabilities` returns the
  zero, all-absent `Set` until the first successful fetch) and **never cached
  for the process lifetime**: the filtered nav/dashboard/route view is
  re-derived from the latest fetched set on every request, and any
  `ErrUnavailable` — which is what a broker restart looks like to the
  stateless per-request client — marks the cache stale (the previously fetched
  set is kept and still served meanwhile, only the `down` flag flips) so the
  next successful authenticated request refetches. A restarted broker advertising a different
  set is therefore followed without restarting the web process. Authorization
  failures, request-validation errors, and domain errors never mark the cache
  down or trigger a refetch. `refreshCapabilities` derives a bounded 2s
  timeout from the caller's context and, on failure, leaves the previous set
  in place rather than clearing it.
- **`platform.CapabilityGate` / `platform.Gate` mechanism**
  (`internal/platform/capability.go`). A `Module` optionally implements
  `CapabilityGate` (`RequiredCapabilities() []capability.ID`) to declare that
  its whole surface — nav entry, dashboard cards, routes — needs those
  capabilities present (`Set.HasAll` semantics). `platform.Available(module,
  caps)` applies that test to a whole module (default-available when the
  module doesn't implement the interface); `internal/web/server.go`'s
  `moduleAvailable`/`availableManifests` filter the shell's nav list, and the
  `dashboard` loop skips a gated-absent module entirely (no `Dashboard()`
  call, no card, no error placeholder). `platform.Gate(host, ids, next)`
  wraps an individual `Mount`-registered handler and 404s when
  `host.Capabilities(r.Context())` doesn't `HasAll(ids...)`. `Gate` reads the
  set itself per request; `Available` takes an already-fetched set, which
  `web.Server` obtains from the same source: `Capabilities(context.Context)
  capability.Set`, added to the `platform.Host` interface in #54 and satisfied
  by `web.Server` from the cache above. Because it takes a `context.Context`
  rather than an `*http.Request`, it is callable from both HTTP handlers and
  `Module.Dashboard(ctx, host)`. `internal/platform` also has an any-of
  sibling set — `CapabilityGateAny` (`RequiredAnyCapabilities()
  []capability.ID`), `GateAny`, and `AvailableAny`, using `Set.HasAny`
  semantics instead of `HasAll` — and `moduleAvailable` composes both:
  `platform.Available(module, caps) && platform.AvailableAny(module,
  caps)`. Modules implementing `CapabilityGate` (whole-module gate):
  - `services` → `Systemd` (plus a `Systemd AND Journald` `Gate` on just
    `GET /services/{unit}/logs`)
  - `logs` → `Systemd AND Journald`
  - `backups` → `Systemd`
  - `podman` → `Podman`
  - `docker` → `Docker`
  - `incus` → `Incus`

  Modules implementing `CapabilityGateAny` (whole-module any-of gate, added
  by #51):
  - `maintenance` → `HasAny(Systemd, Bootc, RPMOStree)`
  - `sysext` → `HasAny(Updex, Sysext)` (added by #52)

  `POST /maintenance/reboot` is additionally, separately gated by a plain
  `Systemd`-only `platform.Gate` outside the whole-module mechanism, so it
  404s on a bootc-only host whose maintenance module is otherwise present.
  `sysext` has the same shape with three narrower sub-guards instead of one:
  `POST /sysext/{name}/{action}` (enable, disable) carries a plain
  `platform.Gate` on `HasAll(Updex, Sysext)`, and
  `POST /sysext/actions/{action}` carries the module's own `GateAny` plus a
  per-action check inside the handler (`refresh` needs `Sysext`, `update`
  needs `Updex`) because the two actions share one route pattern but not one
  requirement — see the sysext bullet below.

  Modules with partial or no gating: `storage` deliberately does *not*
  implement `CapabilityGate` — its inventory (nav, dashboard card,
  `GET /storage`) is available on every capability set — `storage` is
  registered unconditionally, so unlike `fleet` there is no registration
  switch either — and only its three remote-mount routes
  (`GET /storage/mounts/new`, `POST /storage/mounts`, and
  `POST /storage/mounts/{id}/{action}`, which covers mount, unmount, and
  delete) are wrapped in `platform.Gate(host, {Systemd}, ...)`, with the "Add
  remote mount" link and the entire per-mount actions block collapsed behind
  the same `Systemd` flag in `views.templ`. `system`, `files`, `activity`, and
  `fleet` declare no capability requirement, so they are available on every
  capability set they are registered under. `fleet` is the one module whose
  exposure is decided at registration rather than by capabilities:
  `cmd/pilothouse`'s `newRegistry(dev bool)` appends `fleet.New()` only under
  the `--dev` flag (default `false`), so a production process has no fleet
  module, no fleet nav entry, no fleet system-picker link, and no mounted
  fleet routes at all.
- **Attention's per-provider capability skip**
  (`internal/modules/attention/module.go`). The attention aggregator holds
  `[]platform.HealthProvider` and calls `Health` directly — outside any
  `Mount` route or the nav/dashboard filter — so its `findings` type-asserts
  each provider to *both* whole-module gate interfaces and skips it outright
  when either is unsatisfied: `platform.CapabilityGate` when
  `host.Capabilities(ctx)` doesn't `HasAll` its `RequiredCapabilities`, and
  `platform.CapabilityGateAny` when it doesn't `HasAny` its
  `RequiredAnyCapabilities`. Skipping means no `Health` call and no
  "unavailable" finding, since an absent module is not a failed one. The two
  checks are an AND of two defaults — a provider implementing neither
  interface is always collected — mirroring `moduleAvailable`'s composition,
  so this call path stays gate-complete for a module of any of the three
  shapes. (`platform.Available`/`AvailableAny` take a `platform.Module`,
  which a `HealthProvider` need not be, so `findings` applies the same two
  tests to the provider value rather than calling them directly.) On a
  no-systemd host, `services`/`backups` contribute nothing to `/attention`
  rather than a degraded placeholder, via the plain
  `CapabilityGate`/`HasAll` check. `maintenance` is skipped on a different
  condition: it implements `CapabilityGateAny`, so it contributes nothing
  only when *none* of `Systemd`/`Bootc`/`RPMOStree` is present (the
  `CapabilityGateAny`/`AvailableAny` `HasAny` check added by #51) — a
  bootc-only host still collects its `Health`, which reports what it can
  without the systemd-gated query.
- **Routes stay mounted; absence 404s at request time.** No route is ever
  conditionally registered at startup based on capability — every module's
  `Mount` runs unconditionally and mounts all its routes on the shared mux.
  Absence is enforced at request time by `platform.Gate` (per route) and
  reflected in nav/dashboard by `moduleAvailable` (per render), so a gated-off
  surface is indistinguishable from a route that does not exist, both in the
  UI and at the URL.
- **sysext, which #54 did not cover, got its web-side gate in #52.**
  `cmd/pilothouse`'s `newRegistry` now calls `sysext.New()` with no arguments
  — the web process constructs no extension manager and has no
  `--definitions-root`/`--updex` flags — and reads the inventory through
  `broker.QueryExtensionsState`. The exec-backed manager moved out of
  `internal/modules/sysext` into `internal/modules/sysext/extctl` in the same
  change, so the boundary is enforced by the package graph rather than by
  convention: `sysext` keeps only the `Manager`/`ExtensionsSource` interfaces
  and their types and imports no `os/exec`, while `extctl` — linked by
  `cmd/pilothoused` alone — owns `NewSystemManager`, `ExecRunner`, and every
  `updex`/`systemd-sysext` invocation. `sysext.Module` implements
  `platform.CapabilityGateAny` returning `{Updex, Sysext}` (and, like
  `maintenance`, deliberately not `CapabilityGate`), which gates its nav
  entry, dashboard card, and `GET /sysext`. Its mutating routes keep the
  daemon's narrower requirements, mirroring `registerSysextActions` exactly:
  `POST /sysext/{name}/{action}` (enable, disable) is wrapped in
  `platform.Gate` on `HasAll(Updex, Sysext)`, while
  `POST /sysext/actions/{action}` is wrapped in `platform.GateAny` at the
  route and re-checks per action in the handler — `refresh` 404s without
  `sysext`, `update` 404s without `updex` — because the two actions share one
  route pattern but not one requirement. The rendered controls follow the same
  three predicates via `Page`'s `enableDisableAvailable`/`refreshAvailable`/
  `updateAvailable` parameters, and an extension with `Managed: false` renders
  no enable/disable control at all. `cmd/pilothouse/capability_contract_test.go`'s
  `webSideUngatedBrokerIDs` exemption (the four `ActionSysext*` IDs) and its
  `Len == 4` assertion were deleted in the same change, so those IDs are now
  subject to the ordinary web-side capability check.

## Optional tooling is explicitly opt-in (end state, #64)

Several bullets above narrate individual pieces of #64 (sub-phase 1 of 4
split from #53, phase 4 of the #35 arc). This is the landed end state in one
place, so a reader who lands here first does not have to reassemble it.

- **The rule.** An optional dependency is enabled only by explicit
  configuration *plus* a reachable endpoint. Presence on the host is never
  enough. Four dependencies are optional in this sense — `updex`, Podman,
  Docker, Incus — and each is now off unless its flag is set: `--updex`
  (path, default empty), `--podman-socket` (path, default empty), `--docker`
  (endpoint, default empty), `--incus` (bool, default `false`). At the zero
  value each probe returns an empty `Set` before doing any I/O: no command
  is run and no socket is dialled, with no `PATH` fallback for `updex` and
  no `dockerclient.FromEnv` for Docker. `ProbePodman`/`ProbeDocker` do not
  reach their client constructors at all; `ProbeIncus` allocates its client
  struct before the guard, but that allocation performs no I/O and the
  client's only dialling method is never called.
- **What did not change.** `systemd`, `journald`, `sysext`, `bootc`,
  `rpm-ostree`, and the two `autoupdate-*` capabilities stay presence-probed
  and carry no flag. Every `registerX` guard in `cmd/pilothoused` is
  untouched — the guards were already correct, and the defect was entirely
  in what fed them. No broker ID was added or removed by #64:
  `internal/broker/api.go` declared 35 `Action*` and 19 `Query*` constants
  (54 total) both before and after it, and `docs/capabilities.md`'s binding
  table and both capability contract tests were unchanged in shape and count.
  (The four Incus phases later took the query total to 23 and the action
  total to 40; k3s visibility then took the query total to 24 and the
  overall total to 64.) Both contract harnesses build fixtures
  from explicit `capability.Set` values rather than from a live `Probe`, so a
  fixture naming `podman` still means "podman was configured and reachable."
- **Systemd units.** Both packaged broker units
  (`packaging/deb/pilothoused.service` and `packaging/rpm/pilothoused.service`)
  declare no `Wants=` on
  any engine socket (only `After=`, ordering without pull-in), and their
  `ExecStart` passes none of the five flags — so a stock install runs with
  every optional dependency off, and starting the broker never activates
  `podman.socket` or `incus.socket`.
- **Mock Fleet.** `fleet` is a static preview with no real transport, so it
  is gated at *registration*, not by capabilities: `newRegistry(dev bool)`
  appends `fleet.New()` only under `--dev`. With the flag off the module is
  never constructed, so there is no nav entry, no sidebar system-picker link
  (that link derives from `data.Modules` rather than a hardcoded href), and
  `/fleet`, `/fleet/enroll`, and `/fleet/systems/{id}` are unregistered — a
  mux 404, not a `platform.Gate` 404.
- **Net effect on a bare host.** `pilothoused` on a host with nothing
  configured probes clean, advertises whatever presence-probed capabilities
  it found, registers no engine or `updex` query/action, and starts
  successfully; the console renders the surfaces that remain and omits the
  rest entirely rather than showing them broken.

