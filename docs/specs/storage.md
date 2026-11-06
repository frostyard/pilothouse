# Spec: Storage module

The contract of the Storage module: the 1 parameterless inventory query, its
snapshot limits, timeouts and health rules, the 8 administrator-only
remote-mount actions with their parameters and grammar, the artifacts the
remote-mount manager owns, and the snapshot anchors. Consumers:
`cmd/pilothoused` (`registerStorage`, `registerStorageActions`),
`internal/modules/storage` (collectors, normalizer, remote-mount manager,
web module). The web process never inspects devices, mounts, sysfs or procfs,
never runs a storage tool, and never reads a credential.

## Interface

### Inventory query

| Item | Value |
| --- | --- |
| ID | `broker.QueryStorageState` = `org.frostyard.pilothouse.storage.state` |
| Admin | no: every authenticated user |
| Parameters | none |
| Result | `storage.Snapshot`: `backends`, `collected_at`, `findings`, `mounts`, `relations`, `resources`, `summary`, `truncated` |

### Snapshot limits (`internal/modules/storage/model.go`)

| Limit | Value |
| --- | --- |
| Resources | 4,096 |
| Relations | 8,192 |
| Mounts | 1,024 |
| Findings | 512 |
| Details per resource | 32 |
| Text field | 4 KiB |
| Captured tool output per adapter | 4 MiB (`commandRunner{limit: maxAdapterBytes}`) |
| Graph depth | 32 |
| Serialized snapshot | 2 MiB |

### Backend status values

`available`, `unsupported` (tool absent), `unavailable` (run or parse
failure), `timed-out`, `truncated` (a record or size limit was reached; the
snapshot also sets `truncated`).

### Remote-mount actions (`registerStorageActions`)

All are admin-only, registered only when the host has the `Systemd`
capability, and each handler runs with `storageActionTimeout` (2 minutes).

| ID (`org.frostyard.pilothouse.storage.…`) | Parameters | Confirmation |
| --- | --- | --- |
| `create-nfs` | `host`, `export`, `target`, `version`, `read_only` | no |
| `create-smb-guest` | `server`, `share`, `target`, `version`, `read_only` | no |
| `create-smb-credentials` | `server`, `share`, `username`, `password`, `target`, `version`, `read_only` | no |
| `create-smb-guest-owned` | `server`, `share`, `target`, `version`, `read_only`, `uid`, `gid` | no |
| `create-smb-credentials-owned` | `server`, `share`, `username`, `password`, `target`, `version`, `read_only`, `uid`, `gid` | no |
| `mount` | `id` | no |
| `unmount` | `id` | yes |
| `delete` | `id` | yes |

### Parameter grammar (`internal/modules/storage/remote.go`)

The broker action registry first requires every declared parameter to be
present, non-empty, at most 512 bytes, and free of CR, LF and NUL
(`internal/broker/actions.go`); this is why an owned SMB definition uses its
own action instead of empty `uid`/`gid` values. The manager then applies:

| Parameter | Rule |
| --- | --- |
| `id` | 32 lowercase hex characters (`ValidateDefinitionID`); allocated from 16 random bytes by `prepareStorageCreate` |
| `host`, `server` | an IP literal or a DNS name (`ValidateHost`) |
| `export` | absolute path, no `,`, no control characters |
| `share` | non-empty, no `/` or `\`, no control characters |
| `target` | absolute, already `path.Clean`, no control characters, plus the target rules below |
| `version` (NFS) | `auto`, `3`, `4`, `4.1`, `4.2` |
| `version` (SMB) | `auto`, `2.1`, `3.0`, `3.1.1` |
| `read_only` | exactly `true` or `false` |
| `username` | 1 to 256 bytes, no control characters |
| `password` | 1 to 512 bytes, no NUL, CR or LF |
| `uid`, `gid` | both or neither; ASCII digits only; 0 to 4294967294 (4294967295 refused); stored as canonical decimal without leading zeroes (`ParseSMBOwnership`); NFS refuses them |

### Web routes (`internal/modules/storage/module.go`)

| Route | Gate | Notes |
| --- | --- | --- |
| `GET /storage` | none | inventory, 12-second query context; `#storage-snapshot` polls itself every 30 s (`hx-get="/storage"`, `hx-select`/`hx-target` `#storage-snapshot`, `hx-swap="outerHTML"`) |
| `GET /storage/mounts/new` | `Systemd`, admin | the create form |
| `POST /storage/mounts` | `Systemd`, admin | picks 1 of the 5 create actions; an owned action only when both `uid` and `gid` are set; 2-minute context |
| `POST /storage/mounts/{id}/{action}` | `Systemd`, admin | `action` is `mount`, `unmount` or `delete`; `unmount` and `delete` go through `host.ConfirmAction` for `storage/mount/<id>` |

## Rules

### Collection and health

- Each adapter or enricher runs under a 5-second timeout inside a 12-second
  overall deadline (`SystemManager`). A core (`lsblk`, `findmnt`) failure
  makes the snapshot unavailable; an optional backend failure is only its
  backend status.
- Tool paths come only from fixed absolute candidates; a candidate may be a
  symlink whose resolved target is a root-owned regular file that is not
  group- or world-writable, and the original candidate path is executed
  (`ResolveOptionalTool`, `internal/modules/storage/tools.go`). No shell, and
  no argument from a request.
- Only the SMART enricher is cached (`CacheHealth`), for 5 minutes
  (`healthCacheTTL`), keyed by the enricher name. When a refresh after expiry
  fails, the old result is served with the detail `Health data: Stale`.
  Capacity and mounts are collected again for every snapshot.
- Fixed tool invocations: `smartctl --json=c --all <disk>` (the reported
  `device.name` must equal the requested path); `multipathd show maps raw
  format "%n|%w|%d|%N|%t"` and `show paths raw format "%m|%d|%t|%o"`;
  `findmnt --json --list --bytes --output
  TARGET,SOURCE,FSTYPE,OPTIONS,SIZE,USED,AVAIL,USE%,MAJ:MIN`.
- `findmnt` capacity: when `USE%` is empty, `-` or `null`, every capacity
  value MUST be a placeholder or `0`, and the mount gets zero capacity; a
  mix of placeholders and numbers fails the adapter. An absent or `null`
  `lsblk` size is zero; a present size must be a strict unsigned integer.
- Mount health: used ≥ 90 % is a critical finding, ≥ 80 % a warning; a
  read-only mount that Pilothouse does not manage is a warning. A mount with
  filesystem `erofs` gets neither finding (`isExpectedImmutableMount`) but
  stays in the inventory with its capacity and read-only badge.
- Usable capacity counts a mount only when it has a `ResourceID`, is not
  `overlay`, and its resource is not already counted; a network or ambiguous
  mount (empty `ResourceID`) is not in the total.

### Remote-mount artifacts

| Artifact | Path | Mode |
| --- | --- | --- |
| Manifest | `/var/lib/pilothouse/storage/mounts/<id>.json` | `0600` root |
| SMB credentials (`username=…\npassword=…\n`) | `/etc/pilothouse/storage/credentials/<id>` | `0600` root |
| Mount unit, automount unit | `/etc/systemd/system/<systemd-escaped-target>.mount`, `.automount` | `0644` root |
| Created target directory | the target | `mkdirat` asks for `0755` (`PathManager.CreateTarget`, `paths.go`); the process umask applies and nothing changes the mode later, so under the packaged broker units (`UMask=0077`) the directory is `0700` |

- The 2 unit files start with `# Managed by Pilothouse; definition=<id>`
  (`artifactMarker`). The manifest is plain JSON (`marshalManifest`) and the
  credentials file holds only its `username=` and `password=` lines
  (`RenderCredentials`); neither carries the comment. Every artifact is
  written through a temporary file in the same directory, `fdatasync`ed,
  then put in place.
- Verification (`VerifyOwnedArtifacts`, which mount and unmount reach through
  `loadVerified` unless the state is `needs-attention`): the manifest and the
  2 units are compared byte for byte with freshly rendered content, plus
  their mode and root ownership (`verifyFile`); the credentials file is
  checked for metadata only (a regular file, mode `0600`, root owner and
  group: `verifyMetadata`), never for its content. Delete checks each
  artifact the same way just before it removes it. A mismatch stops the
  action; Pilothouse never changes an unmanaged unit or `/etc/fstab`.
- Mount unit: `Wants=` and `After=network-online.target`, `TimeoutSec=30`,
  and sorted `Options=` built only from `nodev`, `nosuid`, `ro` or `rw`,
  `nfsvers=<v>` or `vers=<v>` (not for `auto`), `guest` or
  `credentials=<path>` for SMB, and `uid=`/`gid=` for an owned SMB
  definition. Type `nfs` with source `host:/export` (`[ipv6]:/export`), or
  type `cifs` with `//server/share`. Automount unit: `TimeoutIdleSec=300`,
  `WantedBy=multi-user.target`.
- Manifest format version 2 (`ManifestFormatVersion`) carries `uid`/`gid`
  for an owned SMB definition; version 1 still loads and verifies byte for
  byte, with no migration.
- The password never appears in the audit resource, a manifest, the
  snapshot, a log, a response or an error.

### Target rules (`internal/modules/storage/paths.go`)

- Refused: `/`, a path under `/proc`, `/sys`, `/dev`, `/run`, `/boot`,
  `/etc`, `/usr` or `/var/lib/pilothouse` (`protectedTargetRoots`), a symlink
  in any component (each component opens with `openat2` and
  `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS | RESOLVE_NO_MAGICLINKS`), a
  non-directory, a non-empty existing directory, an active mount at or below
  the target, an ancestor or descendant of another managed target, and a
  `.mount`/`.automount` unit name that another definition owns
  (`hasConflict`).
- A missing target is created with `mkdirat` mode `0755`; the manifest
  records that. Delete removes the target only when the manifest says
  Pilothouse created it.

### Lifecycle and serialization

- Create: validate, write artifacts, `DaemonReload`, enable and start the
  `.automount` unit. On failure, the steps it took are undone in reverse
  order (only a target it created is removed); when an undo fails, the
  manifest is kept in state `needs-attention`. `writeArtifact` never
  replaces an existing file whose content differs.
- Mount: start the `.automount` unit. Unmount: stop the `.automount`, then
  the `.mount` unit (no forced or lazy unmount). Both refuse a definition in
  state `needs-attention`.
- Delete: first persist state `needs-attention`, then stop the automount and
  mount units, disable the automount, remove each verified artifact,
  `DaemonReload`, remove a created empty target, and remove the manifest
  last. A failure keeps the manifest in `needs-attention` so delete can run
  again.
- Audit resource: `storage/mount/<id>` for every action. Lock: create holds
  `storage/mounts` (and the manager's `createMu`); mount, unmount and delete
  hold `storage/mount/<id>`. Inside the manager, each ID has its own lock and
  `State` takes none.

### Snapshot anchors (`snapshotAnchorSet` in `views.templ`)

1. Each resource row gets `storageAnchor(resource.ID)` (every character
   outside `[A-Za-z0-9]` becomes `-`); a used value gets `-` appended until
   it is unique.
2. A mount row gets `mount-<anchor>`, or the bare anchor when a finding
   targets that mount ID and no resource owns it.
3. A finding links to its resource row, else its mount row, else the first
   Attention row with that `ResourceID` gets the anchor; later findings for
   the same ID get no `id`. A finding with an empty `ResourceID` links to
   `/storage` with no fragment.
4. No empty anchor span is a direct child of `.storage-snapshot` (a CSS
   grid). `Module.Health` and the topology links use the same allocation.

### Tests that pin this contract

- `internal/modules/storage/`: `core_test.go`, `normalize_test.go`,
  `manager_test.go`, `cache_test.go`, `smart_test.go`,
  `devicemapper_test.go`, `tools_test.go` (collection, limits, health,
  cache, resolver); `remote_test.go`, `manifest_test.go`, `units_test.go`,
  `paths_test.go`, `remote_manager_test.go` (grammar, artifacts, target
  rules, lifecycle, locks); `views_test.go` and `module_test.go` (routes,
  confirmation, anchors, polling).
- `cmd/pilothoused/main_test.go`: storage registration, parameters and
  audit resource.

## References

- Rationale: [ADR-0002](../adr/0002-audited-resource-key-doubles-as-lock-key.md),
  [ADR-0006](../adr/0006-opt-in-capabilities-zero-io-omission.md)
- Context: [design/overview.md](../design/overview.md),
  [design/capability-gating.md](../design/capability-gating.md),
  [modules.md](../modules.md) ("Managed remote mounts")
