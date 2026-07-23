# Adding management modules

Pilothouse modules are vertical slices. Keep the collector or manager, HTTP actions, views, and tests together under `internal/modules/<id>`.

## Contract

Implement `platform.Module`:

```go
type Module interface {
    Dashboard(context.Context, Host) ([]DashboardCard, error)
    Manifest() Manifest
    Mount(*http.ServeMux, Host)
}
```

`Manifest` gives the shell everything it needs for navigation. `Dashboard` contributes reusable templ components to the overview without coupling the overview to a concrete module. `Mount` registers standard-library Go 1.22 method-and-path patterns and receives a `Host` for common layout rendering and action validation.

## Recommended shape

```text
internal/modules/network/
├── collector.go       domain types and a Collector interface
├── collector_linux.go Linux implementation
├── module.go          manifest, dashboard cards, routes
├── views.templ        module-specific presentation
└── collector_test.go  parser and behavior tests
```

Keep shell-level primitives generic. A network-specific table belongs in the network module; a generally useful badge or button belongs in `internal/web`.

## Minimal module

```go
type Module struct {
    service Service
}

func (m *Module) Manifest() platform.Manifest {
    return platform.Manifest{
        ID: "network",
        Name: "Network",
        Path: "/network",
        Icon: "network",
        Order: 30,
    }
}

func (m *Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
    state, err := m.service.State(ctx)
    if err != nil {
        return nil, err
    }
    return []platform.DashboardCard{{
        Component: Summary(state),
        Order: 40,
        Span: platform.SpanHalf,
    }}, nil
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
    mux.HandleFunc("GET /network", m.page(host))
}
```

Register one constructor in `cmd/pilothouse/main.go`; navigation and dashboard placement then happen automatically.

## Rules for actions

- Use POST for mutations and call `host.ValidateAction` before doing work.
  The one exception is a fixed multipart stream upload (Files), which must
  read the CSRF token from a parsed multipart part before the body is fully
  buffered and validates it with `host.ValidateActionToken` instead — see
  `internal/modules/files/handler.go`.
- Submit a fixed action ID with `host.Execute`; never execute a privileged command in a web module.
- Submit privileged reads through a fixed `host.Query`; never grant the web process access to a root-equivalent API socket.
- Register the privileged implementation in `cmd/pilothoused`, marking whether it requires an administrator.
- Validate identifiers again inside the broker-side domain manager.
- Pass command arguments separately with `exec.CommandContext`; never invoke a shell.
- Put a timeout around external work.
- Run long privileged mutations through the broker's durable background-action definition; never launch detached goroutines from a web module.
- Test web handlers with a fake host and broker actions with fake domain managers.
- Return an `HX-Redirect` for HTMX requests and a 303 redirect for normal forms.
  The exceptions are handlers that end the session or system state outright
  (`POST /logout` in `internal/web/server.go`, `POST /maintenance/reboot` in
  `internal/modules/maintenance/module.go`), which always return a plain 303.

Example web action:

```go
if !host.ValidateAction(w, r) {
    return
}
err := host.Execute(r.Context(), r, "org.frostyard.pilothouse.network.configure", map[string]string{"interface": name})
```

The corresponding broker action is registered once in `cmd/pilothoused`. The action registry rechecks the user's current system groups before dispatch.

## Capability-guarded registration

`pilothoused` probes host capabilities once at startup
(`internal/capability.Probe`, called from `cmd/pilothoused/main.go`'s
`run()`) and exposes the result over the authenticated, non-admin
`org.frostyard.pilothouse.capabilities.list` query
(`broker.QueryCapabilities`). Every privileged registration in
`cmd/pilothoused` that depends on optional host tooling (a container engine
socket, systemd, journald, `updex`, `systemd-sysext`, bootc, rpm-ostree)
must be guarded by the probed `capability.Set` per `docs/capabilities.md`'s
binding table: a `registerX` function checks `caps.Has(...)`/`caps.HasAll(...)`
and registers nothing for that module when its capability is absent, rather
than letting a missing or unreachable dependency fail daemon startup. See
`registerPodman`/`registerDocker`/`registerIncus` in
`cmd/pilothoused/main.go` for the pattern: each takes the probed
`capability.Set` as its last parameter and no-ops immediately when its
engine capability isn't present. New modules that depend on optional host
tooling should follow the same shape from the start.

When a module's registrations have *mixed* capability requirements —
`registerServices` is the example: `QueryServicesState` and every services
lifecycle action need only `Systemd`, while `QueryServicesJournal`
additionally needs `Journald` because it resolves the unit against the
systemd client before reading journal entries — guard each
`Register`/`RegisterDefinition` call (or logical group of calls sharing the
same requirement) individually against `caps.Has(...)`/`caps.HasAll(...)`
rather than gating the whole function on the broadest requirement. That way
a host with `Systemd` but not `Journald` still gets every registration that
doesn't actually need `Journald`, instead of losing the whole module.
`registerLogs` needs both `Systemd` and `Journald` uniformly, so its single
registration is guarded by one `caps.HasAll(...)` check.

## Whole-module web-side capability gating

The guard above is daemon-side and per-registration. A separate,
web-side mechanism lets a whole `platform.Module` declare that its entire
surface — nav entry, dashboard cards, and routes — depends on host
capabilities the web process itself can check per request, via the
`capability.Set` cache described in `yeti/OVERVIEW.md`'s "Web-side
capability fetch/cache". The set reaches both halves of the mechanism
through `Host.Capabilities(context.Context) capability.Set`, a method added
to the `platform.Host` interface in #54 and satisfied by
`internal/web.Server` from the cache above; because it takes a
`context.Context` (not `*http.Request`), it is callable from both HTTP
handlers and `Module.Dashboard(ctx, host)`. `platform.Gate` calls it itself
(`host.Capabilities(r.Context())`) on every request it wraps;
`platform.Available` instead takes an already-fetched `capability.Set` as a
parameter, which `internal/web.Server` obtains from that same method before
filtering the nav list or the dashboard loop. Implement
`platform.CapabilityGate` on your `Module`:

```go
func (m *Module) RequiredCapabilities() []capability.ID {
    return []capability.ID{capability.Docker}
}
```

A module that does not implement `CapabilityGate` has no requirement and is
always available — this is the default for `system`, `files`, `activity`,
`fleet`, and storage's own inventory reads. `internal/web.Server` filters
`CapabilityGate` modules out of both the shell's navigation list and the
dashboard's per-module loop when a required capability is absent; skipped
dashboard modules are omitted entirely (no `Dashboard()` call, no card, no
error placeholder), since an unavailable surface is not rendered, not shown
degraded.

Routes stay mounted on the shared mux regardless — never register a route
conditionally at startup based on capability. Instead, wrap the handler
passed to `mux.HandleFunc` in `Mount` with `platform.Gate`:

```go
func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
    mux.HandleFunc("GET /docker", platform.Gate(host, m.RequiredCapabilities(), m.page(host)))
}
```

`Gate` 404s the request when `host.Capabilities(ctx)` doesn't have every
required capability, and otherwise delegates to the wrapped handler
unchanged — including the zero-capabilities case, which always delegates.
This keeps a capability-gated module indistinguishable from a route that
simply doesn't exist, both in navigation/dashboard and at the URL itself.

`Gate`/`Available`/the dashboard loop only cover requests that arrive
through a module's own `Mount`-registered routes, or the web-side
nav/dashboard registries in `internal/web/server.go` — they do nothing for
other in-process code that holds a reference to the module (or one of its
narrower interfaces) and calls it directly. `internal/modules/attention`
is exactly that: it holds a `[]platform.HealthProvider` and calls
`provider.Health(ctx, host)` on each one to build the aggregated
"Attention" view. If your module implements `CapabilityGate` or
`CapabilityGateAny` (below) and is also passed to `attention.New(...)` in
`cmd/pilothouse/main.go`, its `Health` must not be reachable when the
required capabilities are absent either — `attention.Module.findings`
handles this by type-asserting each provider to *both* gate interfaces and
skipping both the `Health` call and any "unavailable" finding when
`host.Capabilities(ctx)` doesn't satisfy its `RequiredCapabilities`
(`HasAll`) or its `RequiredAnyCapabilities` (`HasAny`). The two checks are
an AND of two defaults, matching `moduleAvailable`; a provider implementing
neither interface is always collected. When adding a new capability-gated module, grep
for every caller of its exported methods and every cross-module interface
it implements — not just `Mount()` — and apply the same
type-assert-and-check wherever one of those calls happens outside the
module's own package.

### Route-level gating for a partial-gate module

Not every module fits the whole-module shape above. `storage` gates only
its three remote-mount routes on `capability.Systemd`
(`GET /storage/mounts/new`, `POST /storage/mounts`, and
`POST /storage/mounts/{id}/{action}`, which covers mount, unmount, and
delete) while its inventory (nav entry, dashboard card, `GET /storage`)
stays available with no capability requirement at all, per
`docs/capabilities.md`'s `QueryStorageState` exception. `storage.Module`
deliberately does **not** implement `platform.CapabilityGate` — implementing
it would make the whole module (including inventory) disappear when
`Systemd` is absent, which is wrong here. Instead, `Mount` wraps just the
three routes directly:

```go
var remoteMountCapabilities = []capability.ID{capability.Systemd}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
    mux.HandleFunc("GET /storage", m.page(host)) // ungated
    mux.HandleFunc("GET /storage/mounts/new", platform.Gate(host, remoteMountCapabilities, m.newMount(host)))
    // ...POST /storage/mounts and POST /storage/mounts/{id}/{action} likewise
}
```

When a module gates only a subset of its routes like this, audit every
view element that targets one of the gated routes — not just the ones a
spec happens to name by example — and collapse them behind one flag as a
unit. `storage`'s `GET /storage` handler computes
`host.Capabilities(r.Context()).Has(capability.Systemd)` once and threads
it into `views.templ`'s `ManagedPage`/`ManagedSnapshotRegion`/
`ManagedMountTable` as a `remoteMountsAvailable bool` parameter (a sibling
of the existing `admin bool`): the "Add remote mount" link and the entire
per-mount `Mount`/`Unmount`/`Delete` actions block are gated on that single
flag, so a host missing `Systemd` never renders a link, button, or form
pointing at a route that would 404 — see
`docs/agents/skills/partial-gate-modules-need-full-view-element-audit.md`
for the failure mode this avoids.

### Any-of whole-module gating (`CapabilityGateAny`)

`CapabilityGate`/`Available` above always require *every* listed capability
(`capability.Set.HasAll` semantics). Some modules will instead need *any
one* of several capabilities — e.g. a module whose surface works given
either of two alternative container engines. For that shape,
`internal/capability`'s `Set` gained a sibling predicate,
`HasAny(ids ...ID) bool`, that reports true iff at least one given id is
present; unlike `HasAll`'s zero-ids case (vacuously true), `HasAny()` with
zero ids is always false, since "any of nothing" has no capability to
satisfy — a nil/zero-value `Set`'s `HasAny` is nil-safe like `Has`/`HasAll`.

`internal/platform` mirrors the whole `CapabilityGate`/`Gate`/`Available`
trio with an any-of sibling set, deliberately kept as separate types rather
than adding an any-of flag to `CapabilityGate` — no module needs both AND
and OR semantics on its whole-module gate at once, and separate interfaces
avoid ambiguity about which test applies to a given module:

```go
type CapabilityGateAny interface {
    RequiredAnyCapabilities() []capability.ID
}
```

`platform.GateAny(host, ids, next)` is `Gate`'s any-of counterpart: it 404s
the request unless `host.Capabilities(ctx).HasAny(ids...)`, and otherwise
delegates to `next` unchanged. `platform.AvailableAny(module, caps)` is
`Available`'s any-of counterpart: it returns true when `module` implements
`CapabilityGateAny` and `caps.HasAny` on its `RequiredAnyCapabilities` is
true, and returns true (available) when `module` does not implement
`CapabilityGateAny` at all — the same default-available convention
`Available` uses for `CapabilityGate`.

`internal/web/server.go`'s `moduleAvailable(module, caps)` — the single
choke point `availableManifests` (nav) and the dashboard card loop both
call — composes the two: `platform.Available(module, caps) &&
platform.AvailableAny(module, caps)`. Because each half defaults to `true`
for a module that doesn't implement its respective interface, this
AND-of-two-defaults composition is exactly correct for all three shapes a
module can be in (`CapabilityGate` only, `CapabilityGateAny` only, or
neither) with no type-switching in `server.go` itself, and needs no further
change if a module later switches from one interface to the other.

Reach for `CapabilityGateAny` instead of `CapabilityGate` when a module's
whole surface should appear as soon as *any one* of a set of alternative
capabilities is present, rather than requiring all of them together. No
production module implements `CapabilityGateAny` yet — the mechanism above
(`HasAny`, `GateAny`, `AvailableAny`, and the `moduleAvailable` composition)
is proven with synthetic fake modules in `internal/capability/capability_test.go`,
`internal/platform/capability_test.go`, `internal/web/server_test.go`, and
`internal/modules/attention/module_test.go` only, the same way
`CapabilityGate` itself was proven before its first real adopter.

Because `CapabilityGateAny` is a whole-module gate, it carries the same
every-call-path obligation as `CapabilityGate`: the `attention` aggregator
described above already honors it, and any future direct caller of a gated
module's methods must apply both tests too.

## Privileged reads

Some read operations are themselves privileged or must use the same system context as mutations. Container engines are the canonical example: access to the Docker, Podman, or Incus API socket is effectively root access, and rootless, remote, and system inventories are distinct.

Register a fixed broker query and call it through `host.Query` from both `Dashboard` and page handlers:

```go
var state State
err := host.Query(ctx, broker.QueryPodmanState, nil, &state)
```

Services diagnostics use the fixed `org.frostyard.pilothouse.services.journal`
query. The daemon validates and resolves one supported unit, then returns only a
bounded hour of whitelisted journal fields; the web process never opens journald.

The administrator-only Logs module uses the fixed
`org.frostyard.pilothouse.logs.list` query. The daemon accepts only bounded
message, priority, unit, and recent-window filters, walks the system journal
newest-first, and returns at most 200 entries from a capped scan. The web
process never opens journald, and the query never accepts arbitrary journal
fields, match expressions, date ranges, or command arguments.

Docker container diagnostics use the fixed read-only
`org.frostyard.pilothouse.docker.logs` query. The
`/docker/containers/{id}/logs` page polls for a bounded 200-line tail; only the
broker daemon accesses the root-equivalent Docker socket.

Storage inventory uses the fixed `broker.QueryStorageState` query. The web
process never invokes storage tools; the broker collects each supported backend
independently, so an unavailable optional backend does not prevent other
inventory from being returned. Unmanaged mounts, including NFS and SMB mounts,
remain read-only inventory.

Optional storage tools are selected only from fixed absolute candidates. A
candidate may be a symbolic link, as is common for LVM's `pvs`, `vgs`, and
`lvs` entry points, but its fully resolved target must be a root-owned regular
file that is not writable by group or others. Missing tools degrade their
backend to unsupported; a present broken or unsafe candidate fails broker
startup.

## Managed remote mounts

Storage remote-mount mutations are administrator-only broker actions:
`org.frostyard.pilothouse.storage.create-nfs`,
`org.frostyard.pilothouse.storage.create-smb-guest`,
`org.frostyard.pilothouse.storage.create-smb-credentials`,
`org.frostyard.pilothouse.storage.create-smb-guest-owned`,
`org.frostyard.pilothouse.storage.create-smb-credentials-owned`,
`org.frostyard.pilothouse.storage.mount`,
`org.frostyard.pilothouse.storage.unmount`, and
`org.frostyard.pilothouse.storage.delete`. Pilothouse owns only definitions it
created: manifests are `/var/lib/pilothouse/storage/mounts/<id>.json`, SMB
credentials are `/etc/pilothouse/storage/credentials/<id>`, and generated
`.mount`/`.automount` units are rooted at `/etc/systemd/system`.

The supported NFS versions are `auto`, `3`, `4`, `4.1`, and `4.2`; supported
SMB versions are `auto`, `2.1`, `3.0`, and `3.1.1`. Forms accept no free-form
mount options. The manager generates only its fixed safe options, including
`nosuid,nodev`, read-only mode, the selected protocol version, and an SMB
credential path when needed. IPv6 NFS hosts are rendered in bracketed
`[host]:/export` form. Unmanaged mounts are never modified, activated,
deactivated, or deleted by these actions.

The two owned SMB actions require canonicalized numeric `uid` and `gid`
parameters together and persist them in version 2 manifests. The manager
renders those values only as fixed deterministic CIFS `uid=` and `gid=`
options; names and free-form options are never accepted. Version 1 managed
definitions remain supported without migration.

Lifecycle operations wait for systemd job completion before touching
artifacts. Unmount stops the `.automount` trigger before the `.mount` unit so
the target cannot silently remount on access; delete does the same before
removing artifacts. Delete verifies each artifact individually before removing
it: a tampered unit file stops the definition's units, durably marks the
manifest `needs-attention`, and preserves the foreign file for inspection,
after which delete can be retried to finish cleanup. A corrupt manifest is
reported as a warning finding in the storage snapshot without hiding the rest
of the inventory, while create and delete refuse to proceed until it is
resolved.

Podman container diagnostics likewise use the fixed read-only
`org.frostyard.pilothouse.podman.logs` query. The
`/podman/containers/{id}/logs` page polls for a bounded 200-line tail; only the
broker daemon accesses the root-equivalent Podman socket.

The administrator-only Files module uses three fixed registrations:
`org.frostyard.pilothouse.files.list`,
`org.frostyard.pilothouse.files.download`, and
`org.frostyard.pilothouse.files.upload`. The list query accepts bounded listing
parameters, while download and upload use fixed stream query/action parameter
sets and a 256 MiB transfer limit. Register those stream operations explicitly;
never add a generic stream or filesystem proxy to the broker protocol.

Query handlers receive the refreshed system identity just like action handlers. Return narrow presentation models; do not expose generic filesystem reads, command output, instance environment variables, secrets, or root-equivalent sockets. Managers must rediscover resources and validate identifiers or names before every mutation.

## Design conventions

Use cards for discrete resources, quiet badges for state, and reserve red actions for destructive work. Pages must remain usable without JavaScript; HTMX is progressive enhancement. No module should require a network-loaded stylesheet, font, script, or icon.
