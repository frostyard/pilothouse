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
- Submit a fixed action ID with `host.Execute`; never execute a privileged command in a web module.
- Submit privileged reads through a fixed `host.Query`; never grant the web process access to a root-equivalent API socket.
- Register the privileged implementation in `cmd/pilothoused`, marking whether it requires an administrator.
- Validate identifiers again inside the broker-side domain manager.
- Pass command arguments separately with `exec.CommandContext`; never invoke a shell.
- Put a timeout around external work.
- Run long privileged mutations through the broker's durable background-action definition; never launch detached goroutines from a web module.
- Test web handlers with a fake host and broker actions with fake domain managers.
- Return an `HX-Redirect` for HTMX requests and a 303 redirect for normal forms.

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
