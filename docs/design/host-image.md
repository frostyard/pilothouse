# Host-image and automatic-update reporting (Maintenance)

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md` — its phase narrative is historical record, see
[../branding.md](../branding.md)). It covers the read-only host-image status
surface (#51) and its automatic-update companion (#58/#60): the bootc and
rpm-ostree parsers, `HostImageManager`, `QueryHostImageStatus` /
`QueryAutoUpdateStatus`, and how `maintenance.SystemManager` consumes them.
Related: [capability-gating.md](capability-gating.md) (the module's web-side
gates), [../capabilities.md](../capabilities.md) (broker-ID binding table),
[../autoupdate.md](../autoupdate.md) (the automatic-update surface end to
end).

**Host-image status (#51) — landed end state.** Maintenance is no longer a
systemd-only surface: it is a read-only host-image lifecycle *and* reboot-posture
module. Every piece has landed — the parsers, the manager, the broker query, the
reboot posture that consumes it, the module's any-of capability gate, and the
Maintenance page section that renders it. Two things it deliberately does **not**
do, and no sentence below should be read as saying otherwise: it exposes no bootc
or rpm-ostree mutation of any kind — no upgrade, switch, rebase, or rollback
action exists, and `cmd/pilothoused/capability_contract_test.go`'s
`TestNoHostImageMutationActionExists` keeps it that way by asserting that none of
`internal/broker/api.go`'s 35 `Action*` constants names bootc, ostree, or
rpm-ostree in either its Go identifier or its wire ID (`Query*` constants are
deliberately exempt — `QueryHostImageStatus` is the read-only surface this phase
adds) — and #51 itself does no automatic-update reporting: normalized updater
policy/timer reporting was split out of #51 into #58, and its web-render
surface into #60. Both have since landed in full, daemon side and web side:
`cmd/pilothoused`'s `run()` now registers
`broker.QueryAutoUpdateStatus`
(`org.frostyard.pilothouse.maintenance.autoupdate_status`) via `registerAutoUpdate`,
guarded by `caps.HasAny(capability.Bootc, capability.RPMOStree)` exactly like
`registerHostImage` and independent of both `registerMaintenance`'s and
`registerHostImage`'s own guards. It constructs one `maintenance.AutoUpdateManager`
whose per-updater configured/not-configured split is driven by the probed
`capability.AutoupdateBootc`/`capability.AutoupdateRPMOStree` flags — so the
`autoupdate-bootc`/`autoupdate-rpm-ostree` capabilities #50 probes and advertises
over `QueryCapabilities` now have a production consumer that gates on and reports
them, not only tests' full-capability fixtures. The manager reuses the same probed
systemd `*dbus.Conn` `cmd/pilothoused` already opens for backups/services/logs (no
second D-Bus dial), passing it through only when non-nil so a typed-nil never
reaches the manager's own nil-client guard. The query's web consumer is
`internal/modules/maintenance`'s `queryAutoUpdate`, which `collectPage` calls
under the same `HasAny(Bootc, RPMOStree)` gate `queryHostImage` uses — never on
a host advertising neither, where the query is unregistered — threading the
response into `Page`'s `autoUpdate *AutoUpdateStatus` parameter and its
`autoUpdateSection` view. That section is read-only in the same strong sense the
host-image one is — it renders no updater control and no automatic-update action
exists in the broker's ID vocabulary for one to target — and its per-updater
configured/not-configured rendering is spelled out in the "Maintenance:
whole-module `HasAny(Systemd, Bootc, RPMOStree)` gate" bullet in the web-side
gating narrative in [capability-gating.md](capability-gating.md). Zincati is neither queried nor special-cased:
`TestMaintenanceNeverReferencesZincati` fails on any non-comment mention of it in
any `.go` or `.templ` file under `internal/modules/maintenance`.

The per-surface capability split is the thing to hold in mind — Maintenance was
the first module where module presence, one route, and each individual broker
call are gated on *different* capability expressions (Extensions/`sysext` is the
other, since #52; see the sysext bullet in the web-side gating narrative in
[capability-gating.md](capability-gating.md)):

| Surface | Gate | Where |
|---|---|---|
| Module presence: nav entry, dashboard card | `HasAny(Systemd, Bootc, RPMOStree)` | `Module.RequiredAnyCapabilities` → `platform.AvailableAny`, via `internal/web/server.go`'s `moduleAvailable` |
| `GET /maintenance` | `HasAny(Systemd, Bootc, RPMOStree)` | `platform.GateAny` in `Mount` (`internal/modules/maintenance/module.go`) |
| `POST /maintenance/reboot` | `Has(Systemd)` | a separate, plain `platform.Gate` in the same `Mount` |
| `/attention` health collection | `HasAny(Systemd, Bootc, RPMOStree)` | `attention.Module.findings`' `CapabilityGateAny` type-assert |
| `QueryMaintenanceState` (reboot posture, reasons, jobs; no extension update availability — that is `QueryExtensionsState`'s) | `Has(Systemd)` | `queryState` web-side; `registerMaintenance` daemon-side |
| `ActionMaintenanceReboot` | `Has(Systemd)` | `registerMaintenance` (`cmd/pilothoused/main.go`); serialized on its own `maintenance/global` lock, no longer `sysext/global` — see the "Per-resource action serialization" bullet in [overview.md](overview.md) |
| `QueryHostImageStatus` (booted/staged/rollback, digests, soft-reboot eligibility) | `HasAny(Bootc, RPMOStree)` | `queryHostImage` web-side; `registerHostImage` daemon-side |
| `QueryAutoUpdateStatus` (per-updater timer/service state, next trigger, policy, drop-in presence) | `HasAny(Bootc, RPMOStree)` | `queryAutoUpdate` web-side; `registerAutoUpdate` daemon-side. The `Autoupdate*` capabilities gate nothing here — they drive the configured/not-configured split *inside* the response, so the "no updater configured" report stays reachable |

What makes the first row real is `internal/web/server.go`'s
`moduleAvailable(module, caps)` — the single choke point `availableManifests`
(nav) and the dashboard loop both call — which composes the two whole-module
tests as `platform.Available(module, caps) && platform.AvailableAny(module,
caps)`. Each half defaults to `true` for a module that doesn't implement its
interface, so this AND-of-two-defaults is exactly right for all three shapes a
module can be in, and maintenance — which implements `CapabilityGateAny` only —
is filtered purely by the `HasAny` half with no type-switching in `server.go`.

Neither `Dashboard` nor `Health` fetches `QueryHostImageStatus`: both call only
`queryState`, so the *host-image section* — deployments, digests, per-source
unavailable indicators, the soft-reboot indicator — is rendered on
`GET /maintenance` and nowhere else. That is not the same as the card and
`/attention` being host-image-free, and the distinction matters:
`QueryMaintenanceState`'s `State` is itself partly host-image-derived on the
daemon side. `SystemManager.State` calls `HostImageSource.Status` (only when the
probed `capability.Bootc` flag passed to `NewSystemManager` is true and the
source is non-nil), appends `stagedHostImageReason` — "A staged host image
deployment requires activation by reboot." — to `State.RebootReasons` when a
staged deployment exists, and copies `SoftRebootCapable` onto `State`. `Summary`
renders `rebootSummary(state)`, which is `state.RebootReasons[0]` whenever
`RebootRequired`, and `Health`'s `maintenance.reboot` finding uses that same
`RebootReasons[0]` as its detail. Reasons are appended in a fixed order —
`/run/reboot-required` marker, staged host image, merged-but-disabled extension,
completed extension update — so on a host whose only reboot reason is a staged
host-image deployment, that host-image-derived sentence *is* what the dashboard
card and the `/attention` finding display. What the card and `/attention` never
show is raw host-image data (image references, digests, rollback slots,
soft-reboot eligibility); the reboot posture they show can be host-image-caused.

Soft-reboot eligibility reaches exactly three places:
`HostImageStatus.SoftRebootCapable` on `QueryHostImageStatus`'s response (set by
`ParseBootcStatus`, preserved by `MergeHostImage`); `State.SoftRebootCapable` on
`QueryMaintenanceState`'s response, copied verbatim by `SystemManager.State` from
the same single `hostImageState` read that supplies that call's
staged-deployment reboot reason; and exactly one UI indicator, rendered by
`hostImageSection`.

Do **not** read that as one parse feeding all three legs. `hostImageState`
reading the source once is a guarantee *within a single `State` call* and
nothing more. `QueryMaintenanceState` and `QueryHostImageStatus` are separate
broker queries, and `HostImageManager.Status` memoizes nothing — it re-runs
`bootc status --json` (and, when rpm-ostree is advertised,
`rpm-ostree status --json`) on every call. One `GET /maintenance` on a
systemd-plus-bootc host therefore runs bootc twice: `collectPage` calls
`queryState`, whose daemon handler reaches `Status` through
`SystemManager.State` → `hostImageState`, and then `queryHostImage`, whose
daemon handler calls `Status` directly. The single `HostImageManager` instance
wired in `cmd/pilothoused/main.go` shares the *code path* to bootc, not the
*result*: `State.SoftRebootCapable` and the `HostImageStatus` the page renders
come from two independent runs and are not guaranteed to agree.

The UI leg reads `HostImageStatus.SoftRebootCapable` — **not** `State.SoftRebootCapable` —
so the indicator's availability follows `HasAny(Bootc, RPMOStree)` and never
`Systemd`: it renders identically on a bootc-only host with no systemd, where
`QueryMaintenanceState` is never called and `State.SoftRebootCapable` is
therefore always nil. `State.SoftRebootCapable` still lands and is still
populated — it is the fact's API-surface leg for any consumer reading the full
systemd-gated posture response in one call — it is simply not what the page
renders from. All three legs keep the value's three states (non-nil true, non-nil
false, nil for "this bootc does not report it"), and nothing in the tree performs
a soft reboot: only the pre-existing full `ActionMaintenanceReboot` exists.

`internal/modules/maintenance/hostimage.go` adds the read-only host-image
domain types — `Deployment` (bootc's image reference + manifest digest, plus
rpm-ostree's supplementary version + ostree checksum) and `HostImageStatus`
(the booted/staged/rollback deployment slots, a three-state
`SoftRebootCapable`, and a symmetric availability/error pair per source:
`BootcAvailable`/`BootcError` and `RPMOStreeAvailable`/`RPMOStreeError`) —
plus `ParseBootcStatus`, a pure decoder for `bootc status --json`;
`ParseRPMOStreeStatus`, a pure decoder for `rpm-ostree status --json` into an
unexported supplement type; and `MergeHostImage`, which combines the two under
a bootc-authoritative precedence rule.

Those parsers have exactly one caller:
`internal/modules/maintenance/hostimage_manager.go`'s `HostImageManager`,
which in turn has exactly two consumers, both wired to the *same* instance in
`cmd/pilothoused/main.go`: the broker query `QueryHostImageStatus`
(`org.frostyard.pilothouse.maintenance.host_image_status`, registered by
`registerHostImage`), and `maintenance.SystemManager`, which takes it as a
`HostImageSource` and reads it while computing `QueryMaintenanceState`'s
posture. Sharing the instance shares the *path*, not the *result*: `Status`
memoizes nothing, so each of the two consumers re-runs the underlying commands.
The query now has a web-side consumer as well: `GET /maintenance`
fetches it when the host advertises `HasAny(Bootc, RPMOStree)` and renders it
as the page's "Host image" section — see the "Maintenance: whole-module
`HasAny(Systemd, Bootc, RPMOStree)` gate" bullet in
[capability-gating.md](capability-gating.md) for exactly what that
section renders. `QueryMaintenanceState`'s response also changed shape (see
the `State` bullet below). The `maintenance` module's own
nav/route/dashboard gating was reworked separately, to
`HasAny(Systemd, Bootc, RPMOStree)` — see the "Maintenance: whole-module
`HasAny(Systemd, Bootc, RPMOStree)` gate" bullet in the web-side gating
narrative in [capability-gating.md](capability-gating.md). What the daemon
side now does:

- `NewHostImageManager(runner, bootcAvailable, rpmOstreeAvailable)` takes the
  probed `capability.Bootc`/`capability.RPMOStree` facts and runs, at most,
  `bootc status --json` and `rpm-ostree status --json` — each at most once per
  `Status` call, only when its flag is true, always through the injected
  `Runner`, never a shell and never a second subcommand. `Status` merges the
  two with `MergeHostImage` and returns raw facts only; it computes no
  reboot-required posture (still `SystemManager.State`'s job) and exposes no
  mutation.
- Per-source failure is symmetric and never fatal: an exec failure *or* a
  parse failure on either source sets that source's `*Available` to false and
  its `*Error` to the message, leaving the other source's data intact.
  `Status` returns no error of its own for a source-level failure, so a host
  where only one tool answers still gets an honest, partial report. A source
  whose capability is absent is never attempted at all, and reports neither
  availability nor an error.
- `registerHostImage` guards the query with
  `caps.HasAny(capability.Bootc, capability.RPMOStree)` — the first any-of
  guard in the daemon's registration code — and is deliberately independent of
  `registerMaintenance`'s `Systemd` guard, so a bootc host without systemd gets
  host-image reporting while the reboot posture query and reboot action stay
  withheld. `docs/capabilities.md`'s binding table carries the row (64 IDs,
  24 queries; the newest ID is k3s's `QueryK3sState`, after the Incus phases' `QueryIncusInstance`,
  `QueryIncusLogs`, `QueryIncusNetwork`, `QueryIncusProfile`, the four
  snapshot/force-stop actions, and `ActionIncusCreate`) and
  `cmd/pilothoused/capability_contract_test.go` exercises it
  across bootc-only, rpm-ostree-only, both, and neither fixtures.
- `maintenance.SystemManager` consumes the staged-deployment fact. `State` is
  where reboot-required posture is assembled and, per the spec's
  "reboot-required posture lives in exactly one place" rule, the only place a
  staged bootc deployment becomes a reason:
  `NewSystemManager(..., hostImage HostImageSource, ..., bootcAvailable bool)`
  reads the source **once** per `State` call, when `bootcAvailable`, and uses
  the single result for two independent purposes. A non-nil `Staged`
  deployment appends "A staged host image deployment requires activation by
  reboot." (`stagedHostImageReason`) alongside the `/run/reboot-required`
  marker, the merged-but-disabled extension reasons, and the completed-job
  reason, and factors into `RebootRequired` the same way. Independently,
  `HostImageStatus.SoftRebootCapable` is copied verbatim onto the new
  `State.SoftRebootCapable *bool` (`soft_reboot_capable,omitempty`) — copied,
  never recomputed, so there is no second source of truth — and is purely
  informational: it is reported whether or not anything is staged and never
  makes `RebootRequired` true on its own. Its three states survive the copy:
  nil means "this bootc does not report eligibility," never a synthesized
  false. The bootc leg follows the same degrade convention as the
  `updexAvailable`/`sysextAvailable` legs: with `bootcAvailable` false the
  source is never called at all (no staged reason, `SoftRebootCapable` nil,
  whatever the source would have said), and when it is called and fails, the
  failure is dropped rather than propagated — per-source availability and
  errors are `QueryHostImageStatus`'s to report (`BootcAvailable`/`BootcError`),
  and the aggregate posture stays answerable. `State` never returns an error
  because of bootc. Only the existing full reboot action is exposed; nothing
  performs a soft reboot.

Contracts of the parsers themselves, worth knowing before consuming them:

- `hostimage.go` executes nothing. Its imports are limited to
  `encoding/json`/`fmt`/`strings`, enforced mechanically by a test over the
  file's AST, so no bootc invocation — least of all a mutation such as
  upgrade, switch, rebase, or rollback — can originate there. Obtaining the
  bytes is the manager's job, and `hostimage_manager.go` imports only
  `context`, so the injected `Runner` is provably the only way a command
  leaves the package.
- A structurally malformed payload returns a non-nil error together with a
  zero `HostImageStatus` (`BootcAvailable` false), never partial data. The
  caller decides whether to record that as `HostImageStatus.BootcError` on an
  otherwise usable report; `ParseBootcStatus` itself never sets `BootcError`.
- "Malformed" covers substance, not just syntax, because a confident but empty
  success would mislead every downstream consumer. Beyond non-JSON, truncated
  JSON, and wrong-typed fields, the parser rejects a document that omits any
  element bootc always emits: `apiVersion` and `kind` are both *required*
  discriminators (an omitted `apiVersion` is a failure, not a bypass — only its
  value is matched loosely, by prefix, so `org.containers.bootc/v2` still
  parses), and the `status` object and its `booted` deployment must be present.
  A payload that satisfies the discriminators but reports nothing — for
  instance `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost"}` —
  is an error rather than a successful `HostImageStatus` with every slot nil.
  Consequently `Booted` is always non-nil on success. Only `staged` and
  `rollback` are optional: a host with nothing pending and nothing to roll
  back to is ordinary, so those slots stay nil without error.
- `SoftRebootCapable` is three-state: non-nil true/false when the host's bootc
  exposes soft-reboot eligibility, nil when it does not. The key is
  `softRebootCapable` on a boot entry, confirmed against bootc's published
  schema (`crates/lib/src/spec.rs`: `BootEntry.soft_reboot_capable`, camelCase,
  `#[serde(default)]`; `HostStatus` has no such field). The parser prefers the
  staged entry — the deployment a soft reboot would activate — and falls back
  to the booted entry when nothing is staged (upstream computes the flag per
  deployment for every reported slot, booted included, so that fallback is not
  a reinterpretation of a staged-only field). A bootc new enough to have the
  field always emits it — it is a plain `bool` with no `skip_serializing_if`,
  so it serializes even when false — which means an absent key reliably
  indicates a bootc predating soft-reboot support: unknown, never a parse
  error and never false.
- rpm-ostree is the *supplementary* source, and its parser's return type says
  so: `ParseRPMOStreeStatus` yields an unexported `rpmOStreeSupplement` (per
  deployment: version string, ostree checksum, plus image/digest/role used
  only for matching), never a `HostImageStatus`, so rpm-ostree output cannot
  stand alone as a host-image report even by accident. rpm-ostree's document
  has no apiVersion/kind discriminator, so the required top-level
  `deployments` array plays that role: a payload without it is a parse error,
  while a payload whose array is empty is a *success* with nothing to add.
  That distinction is the point — it lets the caller tell "rpm-ostree ran but
  its output could not be read" (record `RPMOStreeError`) from "rpm-ostree
  read fine and had nothing to say."
- `MergeHostImage(bootc, rpmOstree)` encodes the spec's precedence rule as
  behavior, not prose. bootc owns deployment identity outright: which slots
  exist, their image reference, their digest, and `SoftRebootCapable` all come
  from bootc alone, and rpm-ostree can only ever fill in `Version`/`Checksum`
  on a slot bootc already reported. It cannot add, remove, or rename a slot —
  merging a full supplement into a failed bootc parse still yields no
  deployments. Entries are matched by the role rpm-ostree itself flags (booted,
  staged) and, for the rollback slot it does not flag, by identity — the digest
  bootc reported, or the image reference when neither side reports a digest,
  compared after stripping the ostree transport decoration rpm-ostree puts in
  front of a reference (`ostree-unverified-registry:`, `…:docker://…`) and
  bootc does not. On conflict the entry is dropped *whole*, version and
  checksum included: a deployment the two sources describe differently is not
  evidently the same deployment, so the failure direction is always less
  detail, never wrong detail.
- `MergeHostImage` returns `RPMOStreeAvailable`/`RPMOStreeError` at their zero
  value and does not carry over an incoming value for either. It only ever
  receives an already-parsed supplement, so it cannot know whether rpm-ostree
  failed, reported nothing, or was never run; the caller that runs the command
  owns those fields and sets them after merging, exactly as it does for bootc.
  The merged result also shares no memory with either argument, so it never
  writes back into the caller's own parse.

