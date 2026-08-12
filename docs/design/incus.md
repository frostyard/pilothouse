# Incus module

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md` — its phase narrative is historical record, see
[../branding.md](../branding.md)). It covers the four Incus phases beyond the
initial inventory: instance depth (allowlisted detail + bounded logs),
snapshots and force stop, read-only networks and profiles, and instance
creation as the one background action. The module's whole-module `Incus`
capability gate is described in [capability-gating.md](capability-gating.md);
the broker-ID bindings are in [../capabilities.md](../capabilities.md).

## Incus instance depth (allowlisted detail + bounded logs)

The Incus module reports what an instance is actually doing, not just that it
exists. Three things carry it:

- **One read, more of it.** `LocalClient.Instances` calls
  `GetInstancesFull(api.InstanceTypeAny)` rather than `GetInstances`, so a
  single round trip returns each instance's configuration, runtime state and
  snapshot list together. `listInstance` projects that onto `incus.Instance`,
  which gained `Addresses`, `CPUTime`, `Memory`, `Processes`, `Snapshots` and
  `StartedAt`. `State` is nil for a stopped instance, so every live field
  stays at its Go zero value — the page renders those as a dash rather than
  as a measured zero. A *running* instance can also fail to report a given
  counter, and Incus does not always spell that as zero: it returns
  **`processes: -1`** when it cannot determine a process count, which is the
  normal case for a virtual machine whose guest agent is not running. Only a
  positive process count and a positive CPU usage are treated as
  measurements; anything else falls back to the same "not reported" zero, so
  no page ever renders `-1`. Check each counter's own unknown spelling before
  trusting it — this one was found by running the module against a real VM,
  not by any fixture. Addresses are filtered to Incus's `global` scope, which
  drops loopback (`local`) and link-local (`link`) without matching on
  interface names, then sorted so map iteration order cannot reorder them.
  This makes the dashboard card's `QueryIncusState` call heavier than it was;
  that cost is accepted rather than split, since it is still one call.

- **`QueryIncusInstance` returns an allowlisted model.** `detail()` in
  `internal/modules/incus/detail.go` builds `Detail` by copying only keys on
  a fixed list — `configKeys`, the `image.` prefix in `configPrefixes`, and
  the per-device-type `deviceProperties` map — never the instance's expanded
  configuration wholesale. This is a security boundary, not tidiness: an
  Incus instance's configuration routinely carries `user.user-data`
  (cloud-init payloads with SSH keys and passwords), `environment.*` and
  `raw.*`, and `docs/modules.md` forbids exposing instance environment
  variables or secrets to the web process. Allowlisting rather than
  denylisting means a key or device type added by a future Incus release is
  excluded until it is reviewed. `detail_test.go` proves this behaviorally:
  it drives the real manager over a fixture whose configuration carries each
  of those namespaces and asserts none of it appears anywhere in the
  serialized model, alongside a positive assertion that the allowlisted keys
  *are* present, so the exclusion assertions cannot pass vacuously.

- **`QueryIncusLogs` takes a source, never a filename.** The query's `source`
  parameter accepts exactly `console` (the instance's console ring buffer)
  or `log` (the supervisor logfile); `SystemManager.Logs` rejects anything
  else before reading. For `log` the filename is derived from the resolved
  instance's own type — `lxc.log` for a container, `qemu.log` for a virtual
  machine — matching what `incus info --show-log` resolves `default` to
  (`cmd/incus/info.go`). No caller-supplied string ever reaches
  `GetInstanceLogfile`, so there is no path-traversal surface to reason
  about. `readLines` keeps a 200-line, 256 KiB tail, dropping an over-cap
  single line whole rather than truncating it. This is a local
  implementation rather than a reuse of podman's `boundedLogLines`: Incus
  logfiles are plain text with neither Docker's 8-byte stream framing nor an
  RFC3339 timestamp prefix, so the shared parts would have been the trivial
  half.

Two routes render it, both behind the module's existing whole-module gate:
`GET /incus/instances/{name}` (detail) and `GET /incus/instances/{name}/logs`
(logs, defaulting to the console source and polling every 5s via HTMX, the
same shape as podman's and docker's logs pages). The web handler rejects an
unsupported `source` itself with a 404, before issuing any broker query.
Neither route is a mutation and this phase declares no new `Action*`
constant: the action total stays at 35 while the query total moves to 21.

Separately, `SystemManager.storage` no longer fails the whole query when one
pool cannot be read. Each pool's volume and bucket reads are independent: a
failure contributes a message to `State.Warnings` and no rows for that pool,
leaving every other pool's inventory intact, and the page renders the
warnings in a "Partial inventory" card. A driver that simply has no bucket
support stays silent, since that is a capability gap rather than a degraded
read. Only the pool list itself is still fatal, because without it there is
nothing to enumerate.

## Incus snapshots and force stop

Snapshots are the first Incus surface that mutates something other than an
instance's run state, and they are the daemon's first **three-identifier**
actions.

- **A new registration shape.** `registerProjectActions` in
  `cmd/pilothoused/main.go` binds exactly two parameters (`project` plus one
  more), which cannot express a snapshot's `project`/`instance`/`snapshot`
  triple. `registerSnapshotActions` is its three-identifier sibling: same
  admin-only, fixed-ID, confirmation-carrying shape, but its `Resource`
  builds `incus/snapshot/<project>/<instance>/<snapshot>`, so two snapshots
  sharing a name on different instances are distinct resources for
  confirmation and audit. Reach for it — not a wider `registerProjectActions`
  — when a future action needs a third identifier.

- **Rediscover before mutating.** `CreateSnapshot` validates the *new* name
  against `validSnapshotName` (stricter than Incus itself: no `/`, no
  spaces, no leading or trailing `-`/`.`, 63 characters) and refuses a name
  the instance already carries, so a create can never silently overwrite.
  `DeleteSnapshot` and `RestoreSnapshot` do **not** use that validator —
  they resolve the name against the instance's live snapshot list instead,
  the same way `RemoveImage` resolves a fingerprint against the live image
  list. That is both stricter (a name that does not exist is rejected before
  any API call) and more permissive in the right direction: a snapshot
  created outside Pilothouse under a laxer name stays manageable.

- **Restore has a precondition, checked here.** Incus refuses to restore a
  running instance from a non-stateful snapshot, and Pilothouse only ever
  creates non-stateful ones (a stateful snapshot needs CRIU, and there is no
  way to ask for one). `RestoreSnapshot` rejects a running instance itself
  with "stop the instance before restoring a snapshot" rather than letting
  that surface as an opaque API error, and the detail page replaces the
  Restore control with an explanation while the instance runs. Delete stays
  available either way.

- **Force stop is its own broker ID.** `ActionIncusStopForce` is deliberately
  not a `force` parameter on `ActionIncusStop`: killing an instance outright
  is materially more dangerous than asking it to shut down, and a distinct ID
  makes it read distinctly in the audit trail. It exists because the graceful
  path gives an instance 30 seconds and then fails, which previously left a
  wedged instance with no way to stop it from the console at all. Both stops
  require confirmation.

Two routes carry the UI, both behind the module's existing whole-module gate:
`POST /incus/instances/{name}/snapshots` (create, whose name arrives in the
form because it names something that does not exist yet) and
`POST /incus/instances/{name}/snapshots/{snapshot}/{action}` for restore and
delete. Both redirect back to the instance's own detail page rather than the
list, so the result is visible where the action was taken.

One incidental fix rode along, and then spread. The instance-action success
message was built as `fmt.Sprintf("Instance %sd", action)`, which is correct
only for "remove" — it produced "Instance startd" and "Instance stopd", and
"stop-force" has no correct derived form at all. The identical
`fmt.Sprintf("Container %sd", ...)` construction was live in the podman and
docker modules too. All three now use explicit `confirmTitles` and
`successMessages` maps keyed by action, and each module carries a
`TestContainerActionMessagesReadAsEnglish` /
`TestInstanceActionMessagesReadAsEnglish` test asserting the rendered notice
text, so the derived form cannot come back. Derive an action's *ID* from its
URL segment if you like; never derive its English.

## Incus networks and profiles (read-only)

The third Incus phase adds two read-only surfaces and, more usefully, tests
whether the instance allowlist generalises. It does — but only one of the two
could reuse it.

- **Profiles reuse the instance allowlist verbatim.** A profile carries
  exactly the same configuration and device shape as an instance, including
  the same `user.*`, `environment.*` and `raw.*` namespaces, so
  `ProfileDetail` calls the same `allowedConfig`/`allowedDevices` helpers with
  the profile's single config map passed as both halves of the
  expanded/local merge. This is the stronger case for the allowlist rather
  than a convenience: a profile's cloud-init payload applies to *every*
  instance that inherits it, so a leak there is broader than a leak from one
  instance.

- **Networks needed their own allowlist, for a concrete reason.** Incus
  network configuration has a different secret surface:
  `bgp.peers.<name>.password` is a real BGP session password (see
  `internal/server/network/driver_bridge.go` in the Incus source), and the
  `ovn.*` and `tunnel.*` families carry credentials and keys.
  `networkConfigKeys` admits only addressing, NAT, DHCP and DNS shape.
  Crucially the *list* model reads its IPv4/IPv6 columns through
  `allowedNetworkValue`, which consults the same predicate — a summary that
  read the raw config directly would have been a bypass around the detail
  model's filter, and a test asserts the serialized list carries no secret.

- **Observed networks are reported, not hidden.** An Incus host's network
  list is mostly interfaces it does not manage — physical NICs, loopback, a
  foreign bridge such as `docker0` — and omitting them would misrepresent the
  host. They render with an "Observed" badge rather than "Managed". Leases
  are the one thing that genuinely depends on management, since Incus tracks
  them only for its own networks, so `NetworkDetail` carries a separate
  `LeasesAvailable` flag: an unmanaged network is never asked for leases at
  all, and the page says Incus tracks none for it rather than showing the
  same "no leases" text a managed network with none would show. A managed
  network whose lease read *fails* degrades to the same unavailable state
  while the rest of the detail survives.

Both new sections join `QueryIncusState` (one call each) and degrade the same
way storage does — an unreadable list costs its own section plus a warning,
never the page. `GET /incus/networks/{name}` and `GET /incus/profiles/{name}`
render the detail, both behind the module's existing whole-module gate.
Neither page contains a form or a button, and `views_test.go` asserts that
directly: there is no mutating counterpart in the broker's ID vocabulary for
either surface.

## Incus instance creation (the one background action)

`ActionIncusCreate` is the Incus module's only background action and the
daemon's first outbound network operation. Both facts drive its shape.

- **Background, because an image download takes minutes.** It registers with
  `Background: true` and a 30-minute timeout, so the broker enqueues a
  durable job (`jobs.db`), holds the new instance's own
  `incus/instance/<project>/<name>` lock for the duration, and returns
  immediately — the same mechanism `ActionSysextUpdate` uses. Two creates of
  the same name in the same project cannot race: the second fails with
  "resource already has a maintenance job" rather than queueing behind the
  first. The web notice says creation *started*; the outcome lands in
  Activity. Locking per instance rather than globally is deliberate —
  creating two different instances at once is ordinary.

- **The remote is a constant, never a parameter.** `imageRemote` is
  `https://images.linuxcontainers.org`, the server the `images:` remote
  points at, and the action's parameter set (`project`, `name`, `image`,
  `type`, `profile`) has no remote, server or URL field. That is what keeps
  this a fixed operation rather than a generic fetcher the broker could be
  pointed anywhere, and it is asserted from both ends: a manager-side test
  pins the constant, and a web-side test posts `remote`/`server`/`url` form
  fields and proves none of them reaches the action's parameters.

- **Resolve the alias explicitly.** The Incus CLI's `getImgInfo` takes a
  shortcut for a simplestreams remote: it builds a synthetic
  `api.Image{Fingerprint: alias, Public: true}` and lets the server resolve
  the alias during the copy. `CreateFromImage` instead calls
  `GetImageAliasType(instanceType, alias)` and `GetImage` against the fixed
  remote first, so a nonexistent alias fails immediately with a readable
  message rather than several minutes later as a failed background job. The
  remote conversation is bounded by its own 60-second HTTP client; the
  transfer itself is the daemon's work and is bounded by the action timeout.

- **`RemoteOperation.Wait()` takes no context.** Unlike `Operation`, the
  remote-copy operation's `Wait` cannot be cancelled, so calling it directly
  would make the background action's timeout meaningless — the call would
  block until Incus gave up on its own. `waitRemote` races `Wait()` against
  `ctx.Done()` and calls `CancelTarget()` on expiry, so the timeout actually
  bounds the work and a cancelled transfer is abandoned rather than left
  running unattended. Check for this whenever an SDK hands back a waiter
  with no context parameter.

Validation is broker-side and happens before anything reaches the network:
the instance name against the same rule every other instance action uses, the
type against the closed `container`/`virtual-machine` pair, the profile
against the project's live profile list (rediscovered, not trusted), and the
alias against a shape check that rejects empty, `.` and `..` path segments.
A name that already exists is refused. Instances Pilothouse creates carry no
caller-supplied device or configuration overrides.

