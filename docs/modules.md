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

Podman container diagnostics likewise use the fixed read-only
`org.frostyard.pilothouse.podman.logs` query. The
`/podman/containers/{id}/logs` page polls for a bounded 200-line tail; only the
broker daemon accesses the root-equivalent Podman socket.

Query handlers receive the refreshed system identity just like action handlers. Return narrow presentation models; do not expose generic filesystem reads, command output, instance environment variables, secrets, or root-equivalent sockets. Managers must rediscover resources and validate identifiers or names before every mutation.

## Design conventions

Use cards for discrete resources, quiet badges for state, and reserve red actions for destructive work. Pages must remain usable without JavaScript; HTMX is progressive enhancement. No module should require a network-loaded stylesheet, font, script, or icon.
