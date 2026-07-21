# Storage Module Design

## Purpose

Add a Storage module that gives authenticated users an in-depth, coherent view of local and mounted storage. It will combine block devices, layered storage, filesystems, local mounts, and network mounts into one topology instead of exposing unrelated command outputs.

Administrators will also be able to add and manage persistent NFS and SMB mounts. These definitions will use Pilothouse-managed systemd mount and automount units and will default to on-demand activation so an unavailable remote server does not block boot.

## Scope

The module will:

- Inventory physical, virtual, removable, loop, and device-mapper block devices.
- Inventory local, bind, overlay, NFS, SMB, and other mounted filesystems.
- Correlate partitions, filesystems, mounts, MD RAID, LVM, LUKS/device-mapper, multipath, ZFS, and Btrfs resources in a normalized topology.
- Report SMART and NVMe identity, health, temperature, wear, and concise diagnostics when supported.
- Show capacity, utilization, read-only state, mount state, and normalized health findings.
- Refresh the coherent storage snapshot every 30 seconds while the page is open.
- Let all authenticated users view storage state and dashboard/Attention findings.
- Let administrators create NFS and guest or credentialed SMB definitions, mount or unmount them, and delete them.
- Persist remote definitions through generated systemd `.mount` and `.automount` units owned by Pilothouse.

The module will not:

- Partition, format, resize, wipe, unlock, repair, scrub, replace, or otherwise reconfigure local storage.
- Create or alter MD RAID, LVM, LUKS, multipath, ZFS, or Btrfs resources.
- Modify arbitrary existing mount definitions or `/etc/fstab`.
- Mount arbitrary local block devices.
- Provide arbitrary command execution, command arguments, filesystem reads, or a generic device API through the broker.
- Return passwords, credential-file contents, raw command output, or filesystem contents.

## Architecture

Add a self-contained vertical slice under `internal/modules/storage`:

- `module.go` defines the manifest, dashboard and health contributions, routes, authorization checks, fixed broker calls, form parsing, and redirects.
- `manager.go` defines the privileged manager, normalized snapshot model, graph aggregation, health normalization, limits, and remote-mount lifecycle.
- Focused adapter files implement bounded discovery for core block/mount state and optional storage backends.
- `views.templ` renders the dashboard card, hybrid operations page, topology, inventory, details, forms, and unavailable states.
- Tests remain in the module and cover adapters, aggregation, actions, handlers, and rendering.

Register the web module in `cmd/pilothouse` and privileged implementations only in `cmd/pilothoused`. The module uses one fixed read query and fixed actions for remote-definition creation, mount, unmount, and deletion. The query is available to authenticated users; every mutation requires an administrator.

The web process never reads block metadata, invokes storage tools, writes system configuration, opens a device, or reads stored credentials. It receives only the bounded presentation model from the privileged broker.

### Broker Contract

Use these exact broker IDs:

- `broker.QueryStorageState`: `org.frostyard.pilothouse.storage.state`, with no parameters.
- `broker.ActionStorageCreateNFS`: `org.frostyard.pilothouse.storage.create-nfs`, with `host`, `export`, `target`, `read_only`, and `version`.
- `broker.ActionStorageCreateSMBGuest`: `org.frostyard.pilothouse.storage.create-smb-guest`, with `server`, `share`, `target`, `read_only`, and `version`.
- `broker.ActionStorageCreateSMBCredentials`: `org.frostyard.pilothouse.storage.create-smb-credentials`, with `server`, `share`, `target`, `username`, `password`, `read_only`, and `version`.
- `broker.ActionStorageMount`: `org.frostyard.pilothouse.storage.mount`, with `id`.
- `broker.ActionStorageUnmount`: `org.frostyard.pilothouse.storage.unmount`, with `id`.
- `broker.ActionStorageDelete`: `org.frostyard.pilothouse.storage.delete`, with `id`.

The current broker requires every declared action parameter to be present and non-empty. Boolean values therefore use the exact strings `true` and `false`; automatic protocol-version selection uses `auto`. The privileged handlers reject unknown values even after the registry validates the parameter set.

## Adapter Model

The privileged manager composes independent adapters for:

- Core block-device and filesystem topology.
- Current local and network mounts.
- SMART and NVMe health.
- MD RAID.
- LVM physical volumes, volume groups, and logical volumes.
- LUKS/device-mapper mappings and multipath devices.
- ZFS pools and datasets.
- Btrfs filesystems, devices, and subvolumes.

Each adapter has one narrow interface and returns typed resources, typed relations, health observations, and its own availability state. Implementations use fixed executable paths or kernel/system APIs with fixed argument shapes. No executable, output field, match expression, path to inspect, or free-form argument comes from an HTTP or broker parameter.

Adapters run concurrently under five-second individual timeouts and one twelve-second overall query deadline. Failure or absence of an optional adapter does not discard valid results from other adapters. Core block and mount discovery failure makes the snapshot unavailable because the manager cannot safely correlate or aggregate the remaining data.

Each adapter applies explicit limits to records, field lengths, parser depth, and raw input. The aggregator applies a final aggregate response-size limit. Reaching a safe record or response cap returns a successful snapshot marked as truncated; malformed or internally inconsistent privileged output fails that adapter closed.

Known executable dependencies are resolved from compile-time allowlists of absolute candidate paths when `pilothoused` starts and retained as absolute paths. Candidates must be regular root-owned files that are not group- or world-writable. An unresolved optional executable marks that backend unsupported. Requests cannot alter resolution or trigger lookup of another executable.

## Normalized Model

The fixed query returns a `Snapshot` containing:

- A collection timestamp.
- Summary totals for usable capacity, used and free space, active mounts, unhealthy resources, and unavailable backends.
- Typed resources with stable IDs, display names, class, size, usage, state, normalized health, and a bounded set of typed details.
- Typed relations such as `contains`, `member-of`, `backs`, `maps-to`, and `mounted-at`.
- Mount records with source, target, filesystem type, capacity, utilization, read-only state, activation state, safe presentation options, and Pilothouse ownership.
- Backend status records: `available`, `unsupported`, `unavailable`, `timed-out`, or `truncated`.
- Normalized health findings with severity, affected resource, title, and concise detail.

Resource classes cover disks, partitions, arrays, encrypted mappings, multipath devices, LVM objects, ZFS pools and datasets, Btrfs filesystems and subvolumes, filesystems, remote shares, and mount points.

Stable IDs are derived only from validated backend identities and include a type namespace. Presentation labels may contain device names, filesystem labels, mount paths, server/share names, or pool names, but labels are never accepted as mutation authority. Remote-mount mutations use a separate opaque managed-definition ID.

The aggregator validates relation endpoints, rejects cycles, identifies orphans, and produces deterministic ordering. It calculates usable capacity from mounted filesystems and top-level allocatable pools rather than summing every layer, which would double-count the same bytes. Resources whose ownership overlaps ambiguously remain visible but are excluded from the aggregate and marked accordingly.

## Health And Detail Semantics

The model normalizes backend-specific observations into `healthy`, `warning`, `critical`, and `unknown` while preserving concise typed details:

- SMART/NVMe: overall result, temperature, wear or percentage used, media/data-integrity errors, and selected lifetime counters.
- MD RAID: level, active/expected members, degraded state, and recovery/resync progress.
- LVM: partial or missing-device state and data/metadata utilization where reported.
- LUKS/device-mapper and multipath: active mapping state, path counts, and degraded path state.
- ZFS: pool health, degraded/faulted devices, capacity, and reported error counts.
- Btrfs: device completeness, allocation, and reported device error counters.
- Filesystems and mounts: capacity thresholds, read-only transitions, inaccessible sources, and inactive managed definitions.

Expensive health reads are cached for five minutes in the broker daemon. Each health result includes its collection time; a result older than five minutes is stale and remains labeled stale if a refresh attempt fails. Capacity and mount state are recollected for each snapshot.

The module implements `platform.HealthProvider`. Critical and warning observations contribute findings to Attention and link to the affected resource anchor on `/storage`. Its dashboard card shows aggregate capacity, active mounts, and the highest current health severity.

The existing System module retains its `/var` capacity card and threshold finding unchanged. Storage does not import or reuse the System collector, and System does not depend on Storage.

## Web Interface

The `/storage` page uses the approved hybrid operations layout:

1. A summary row shows usable capacity, used and free space, active mounts, and health.
2. An Attention panel shows degraded arrays or pools, failing devices, nearly full filesystems, inactive managed mounts, unavailable backends, and truncation.
3. A Mounted Storage table shows local and remote source, target, type, usage, read-only state, activation state, and available management controls.
4. A compact topology tree shows the path from physical or remote source through intermediate layers to filesystem and mount point.
5. Filterable inventory sections cover disks, RAID, encryption/multipath, LVM, ZFS, Btrfs, filesystems, and remote definitions.
6. Expandable resource details show bounded backend-specific identity and health fields without secrets.

The main snapshot region polls `GET /storage` every 30 seconds with HTMX and replaces itself as one unit so summary, topology, inventory, and findings always describe one coherent snapshot. The page and all forms remain usable without JavaScript.

All authenticated users see the same storage inventory and health data. Only administrators see mutation controls. Errors use stable user-facing descriptions and never render privileged error text or command output.

## Remote Mount Form

`GET /storage/mounts/new` renders one form with protocol-specific fields.

NFS accepts:

- A validated host name or IP literal.
- An absolute exported path.
- An absolute local target.
- Read-only or read-write mode.
- Protocol version `auto`, `3`, `4`, `4.1`, or `4.2`.

SMB accepts:

- A validated server name or IP literal.
- A validated share name.
- Guest mode or a username and password.
- An absolute local target.
- Read-only or read-write mode.
- Protocol version `auto`, `2.1`, `3.0`, or `3.1.1`.

Free-form comma-separated mount options are not accepted. The generated unit applies network-online ordering and always adds `nosuid` and `nodev`, then adds only the selected read-only and protocol-version settings. Credentialed SMB also receives the manager-generated credentials path. Credentials, credential-file paths, executable paths, and systemd directives cannot be supplied as options. Domain authentication, custom ports, UID/GID mapping, file/directory modes, soft NFS mounts, Kerberos, and arbitrary mount options are outside v1.

Every new definition defaults to an enabled on-demand automount. Creation enables and starts the `.automount` unit; it does not force network access to the share. An administrator can explicitly mount or unmount the definition from the inventory.

## Managed Definition Ownership

Each remote mount receives an opaque ID and a root-owned manifest under `/var/lib/pilothouse/storage/mounts`. The manifest records the protocol, validated source fields, target, supported options, activation mode, whether Pilothouse created the target directory, generated unit paths, credential reference, and a format version.

The manager derives the required systemd unit name from the target using systemd path escaping and atomically generates the corresponding `.mount` and `.automount` files under `/etc/systemd/system`. Generated files contain an ownership marker and deterministic content. Before every mutation, the manager reloads the manifest, revalidates it, and verifies that each generated file matches the expected ownership marker and definition. A mismatch stops the action; the manager never overwrites an administrator-modified unit.

Existing systemd mounts, `/etc/fstab` mounts, and unmanaged network mounts appear in inventory but have no mutation controls. Broker actions identify managed definitions only by opaque ID and rediscover their manifests before operating.

## Target Path Safety

Administrators may choose any absolute target, subject to broker-side safety validation. The manager canonicalizes the existing path prefix and rejects:

- `/` itself.
- Relative paths, empty components, dot traversal, NULs, control characters, or paths above the length limit.
- Symlinks in any existing component.
- Non-directory targets.
- Pseudo-filesystem and critical system trees, including `/proc`, `/sys`, `/dev`, `/run`, `/boot`, `/etc`, `/usr`, and their descendants.
- Pilothouse state and configuration under `/var/lib/pilothouse` and `/etc/pilothouse`.
- A target that contains an active nested mount.
- A target already owned by another managed definition or an incompatible existing mount unit.

An existing empty directory may be used. A missing target may be created with fixed ownership and permissions after validating its nearest existing ancestor. Deletion removes a target only when its manifest records that Pilothouse created it and it is empty after unmount.

## SMB Credential Handling

Guest SMB definitions have no credential file. Credentialed definitions submit username and password through the authenticated, CSRF-protected web action to a dedicated fixed broker action. The password is a transient broker parameter; it is never used in the action resource string, audit record, error text, logs, response, snapshot, or manifest.

The broker writes a root-owned `0600` credential file atomically under `/etc/pilothouse/storage/credentials` and references it from the generated mount unit. The manifest stores only the credential-file reference and non-secret authentication mode. Snapshot data may show the configured username but never the password or credential-file contents.

Deleting a definition removes its credential file only after successful deactivation and ownership verification. Any error returned across the broker boundary uses a stable category and cannot contain submitted values.

## Action Lifecycle

Use separate fixed action definitions where parameter shape differs, including NFS creation, guest SMB creation, and credentialed SMB creation. All storage actions are administrator-only and validate exact parameter names.

Creation performs these steps:

1. Validate and normalize all source, target, and option fields.
2. Recheck target safety and conflicts against current mounts, units, and managed manifests.
3. Allocate an opaque ID and construct deterministic manifest and unit content.
4. Create the target if required.
5. Atomically write a required credential file with mode `0600`, generated units with mode `0644`, and the manifest with mode `0600`; all files are owned by root.
6. Reload systemd, enable the automount, and start the automount unit.
7. Roll back newly written artifacts if a later step fails.

Mount and unmount actions accept only the opaque ID, rediscover and verify the definition, then start or stop its `.mount` unit. Unmount requires confirmation because active consumers may fail. Actions reject targets with nested active mounts and rely on systemd's normal busy-mount refusal; they do not use forced or lazy unmount.

Delete requires confirmation. It verifies ownership, stops and disables the units, and proceeds only after successful unmount. It then removes generated units, credentials, and the manifest, reloads systemd, and removes a Pilothouse-created target only if empty. If deactivation fails, the definition and files remain recoverable. If cleanup cannot complete after deactivation, the manifest remains or is restored and the snapshot reports the definition as needing attention.

Actions are serialized per managed definition. Their audit resource is `storage/mount/<opaque-id>`, never a source, target, username, password, or option string. Each action has a bounded timeout and returns the established HTMX redirect or normal `303` redirect.

## Security Boundaries And Limits

The storage query is a fixed, parameterless broker query available to authenticated users. Storage actions are fixed administrator-only definitions with exact parameter lists. The web route performs role checks for controls, but broker authorization remains authoritative.

Collectors never accept user-controlled commands, executable paths, device paths, output selectors, or backend arguments. Remote-definition input is used only by the dedicated manager after strict grammar, length, canonical-path, conflict, and allowlist validation. External tools are invoked without a shell.

The manager applies:

- Per-adapter and overall timeouts.
- Maximum resources, relations, mounts, findings, and backend detail fields.
- Maximum field and raw-input lengths.
- Maximum graph depth and aggregate serialized size.
- Short bounded caching for expensive device-health reads.
- Stable error categories that exclude privileged output and user-supplied secrets.

Concrete initial limits are 4,096 resources, 8,192 relations, 1,024 mounts, 512 findings, 32 detail fields per resource, 4 KiB per text field, 4 MiB of raw input per adapter, graph depth 32, and 2 MiB of aggregate serialized snapshot data. A submitted source, target, username, or option value remains subject to the broker's existing 512-byte action-parameter cap; passwords use the same cap. The manager applies stricter protocol grammar limits where applicable.

HTML rendering escapes all labels, paths, sources, and health details. The UI does not reveal stored passwords or credential-file contents to any user, including administrators.

## Error Handling

If core block or mount discovery fails, the initial page renders an unavailable snapshot region and subsequent 30-second polls retry. Optional backend failures are localized and shown as capability statuses while valid inventory remains visible. Unsupported tools are distinguished from transient failure. Truncation and stale health are explicit.

The web layer may return field-level form validation before dispatch, but the privileged manager repeats every validation. Action failures redirect to a stable notice and direct the administrator to Activity for the non-sensitive audited outcome.

Creation tracks every artifact it creates and removes those artifacts in reverse order on failure. Rollback never removes a pre-existing target or file. If rollback cannot safely complete, the manager preserves or restores a manifest so residue remains visible and manageable rather than becoming an untracked configuration.

## Testing

Adapter tests cover:

- Representative and empty output for every supported backend.
- Malformed, oversized, contradictory, and truncated output.
- Missing tools, unsupported capabilities, permission errors, and timeouts.
- SMART/NVMe, MD RAID, LVM, LUKS/device-mapper, multipath, ZFS, and Btrfs health normalization.
- Local, network, bind, overlay, loop, removable, and virtual mount/device inventory.

Aggregator tests cover:

- Stable namespaced IDs and deterministic ordering.
- Parent/child relation merging across adapters.
- Missing endpoints, orphans, duplicate relations, and cycle rejection.
- Capacity aggregation without double-counting layered devices or datasets.
- Ambiguous ownership exclusion and health severity aggregation.
- Record, graph-depth, and aggregate-size limits.

Remote-manager tests cover:

- NFS, guest SMB, and credentialed SMB validation and generated files.
- Protocol-specific option allowlists and rejection of free-form directives.
- Absolute target validation, protected trees, traversal, symlinks, nested mounts, conflicts, and non-directory targets.
- Correct systemd path escaping and deterministic `.mount`/`.automount` content.
- Root-only manifest and credential permissions, atomic writes, password redaction, and non-sensitive audit resources.
- Ownership-marker enforcement and refusal to alter unmanaged or modified units.
- Creation rollback at each failure point.
- Mount, confirmed unmount, confirmed delete, busy mount, partial cleanup, and target-directory ownership behavior.
- Per-definition action serialization and timeout behavior.

Handler and broker-registration tests cover:

- Authenticated read access and administrator-only mutations.
- Dispatch through only the fixed storage query and action IDs.
- Exact parameter sets and independent broker-side validation.
- CSRF checks, confirmation behavior, HTMX redirects, and normal `303` redirects.
- No broker mutation call for non-administrators or invalid HTTP input.
- Stable unavailable and action-error views without privileged details or secrets.

Rendering tests cover:

- Dashboard capacity, mount count, and highest health severity.
- The hybrid summary, Attention panel, mount table, topology, inventory filters, and resource details.
- Local and remote resources across all supported backend classes.
- Thirty-second HTMX polling of one coherent snapshot region.
- Unsupported, unavailable, stale, truncated, empty, and fully unavailable states.
- Administrator-only action controls and protocol-specific form fields.
- Escaped paths, sources, labels, and diagnostic text.
- Rendered component output with no literal `@web.` syntax.

Existing System tests will continue to assert its `/var` storage metric and health thresholds unchanged.

After templ changes, run `make generate`. Before handoff, run `make build`, `make test`, `make fmt`, and `make lint`, using matching Docker targets if native dependencies are unavailable.
