# Spec: Files module

The contract of the administrator-only Files module: the broker flags that
configure its roots, the 3 fixed broker operations, the streaming broker
transport they use, the privileged filesystem manager, and the web routes.
Consumers: `cmd/pilothoused` (registration), `internal/modules/files`
(manager and web module), `internal/broker` (stream registries, client and
server). The web process never opens a host path; it calls only the 3 fixed
IDs below.

## Interface

### Root configuration (`pilothoused` only)

| Flag | Repeatable | Value | Effect |
| --- | --- | --- | --- |
| `--files-root` | yes | `<id>=<absolute-path>` | read-only root: list and download |
| `--files-write-root` | yes | `<id>=<absolute-path>` | writable root: list, download and upload |

`RootFlags.Add` in `internal/modules/files/config.go` splits the value on the
first `=`, so later `=` characters stay in the path.

| Constraint | Value |
| --- | --- |
| Root ID | `^[a-z0-9][a-z0-9-]{0,31}$` (`rootIDPattern`), unique across both flags |
| Root count | at most 64 (`MaxRoots`) |
| Root path | absolute after `filepath.Clean`, at most 4 KiB (`maxRootPathBytes`), not `/` |
| Root path type | an existing directory; `os.Lstat` refuses a final symlink |
| Root descriptor | `NewSystemManager` opens each root once with `O_PATH \| O_DIRECTORY \| O_CLOEXEC \| O_NOFOLLOW` and keeps it until `Close` |

With no roots, the module stays in the navigation, the list query returns no
roots, and every transfer is refused as an unknown root.

### Broker operations

| ID (`internal/broker/api.go`) | Registry | Admin | Parameters |
| --- | --- | --- | --- |
| `QueryFilesList` = `org.frostyard.pilothouse.files.list` | `QueryRegistry` (JSON) | yes | `root`, `path`, `filter`, `sort`, `direction`, `hidden` |
| `QueryFilesDownload` = `org.frostyard.pilothouse.files.download` | `StreamQueryRegistry` | yes | `root`, `path` |
| `ActionFilesUpload` = `org.frostyard.pilothouse.files.upload` | `StreamActionRegistry` | yes | `root`, `directory`, `name` |

`registerFiles` in `cmd/pilothoused/main.go` is the only registration site.
Both transfers have `Limit: files.MaxTransferBytes` (256 MiB); the upload has
`Timeout: 15 * time.Minute`.

### List parameters (`ParseListParameters`)

All 6 keys MUST be present (exactly 6 keys); an empty value takes the default.

| Parameter | Values | Default |
| --- | --- | --- |
| `root` | configured root ID; empty returns root summaries only (then `path` MUST be empty) | — |
| `path` | relative directory (see path rules); empty is the root | — |
| `filter` | trimmed, case-insensitive name substring, at most 200 code points and 1 KiB | empty |
| `sort` | `name`, `size`, `modified`, `owner`, `permissions` | `name` |
| `direction` | `asc`, `desc` | `asc` |
| `hidden` | `true`, `false` | `false` |

### Web routes (`internal/modules/files/handler.go`)

| Route | Behavior |
| --- | --- |
| `GET /files` | list query for root summaries; `303` to the first root by ID, or the no-roots page |
| `GET /files/{root}?path=&filter=&sort=&direction=&hidden=` | list query, 8-second context |
| `GET /files/{root}/download?path=` | stream query, 15-minute context |
| `POST /files/{root}/upload?path=` | multipart upload into stream action, 15-minute context |

## Rules

### Path resolution

- A request path is relative and slash-separated. `validateRelativePath`
  refuses a leading `/`, an empty segment, `.`, `..`, a segment longer than
  `NAME_MAX`, NUL and control characters (below 0x20, and 0x7f), and a path
  longer than 768 bytes (`MaxPathBytes`).
- Every descendant opens from the root descriptor with `openat2` and
  `RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS | RESOLVE_NO_SYMLINKS`
  (`openBeneath`). A symlink in any position fails resolution; a mount below a
  root stays reachable. No string-prefix check and no second open by name.
- `openBeneath` maps `ENOENT`, `ENOTDIR`, `ELOOP` and `EXDEV` to not found, and
  `ENOSYS` (no `openat2`) to unavailable.

### Listing

- At most 10,000 names scanned (`MaxScannedEntries`), at most 2,000 entries
  returned (`MaxEntries`), and at most 1.5 MiB of encoded entries
  (`maxEntryJSONBytes`), which keeps the response below the broker's 2 MiB
  JSON cap. Reaching any limit returns the partial list with `Truncated: true`.
- Directories sort first in both directions; then the chosen field; then the
  name as tie-breaker. `permissions` compares `Mode & 0o7777`.
- Each child is read with `fstatat(AT_SYMLINK_NOFOLLOW)`. An entry carries
  name, type (`regular`, `directory`, `symlink`, `other`), size, mtime, UID,
  GID, owner and group names (numeric string when lookup fails), mode, and for
  a symlink the `readlinkat` target (at most 4096 bytes), which is display
  text only. A child that vanishes (`ENOENT`) is skipped. No file content is
  read.

### Download

- `Download` opens the path with `O_RDONLY | O_NOFOLLOW | O_NONBLOCK`,
  `fstat`s that descriptor, requires a regular file of at most 256 MiB, and
  returns the same descriptor, so a rename after validation cannot swap the
  object. A FIFO does not block the open.
- The broker streams exactly the validated size; the filename is
  `path.Base` of the request path.
- The web handler refuses any `Range` header with `416` before a broker call,
  and answers with `Content-Disposition: attachment` (via
  `mime.FormatMediaType`), `Content-Type: application/octet-stream`,
  `X-Content-Type-Options: nosniff` and `Content-Length`.

### Upload

- Only a writable root (else `ErrReadOnly`). The name is 1 valid base-name
  segment (`validBaseName`); the web handler also refuses `.`, `..`, `/`, `\`
  and control characters (`validUploadName`).
- `Upload` opens the destination directory beneath the root, creates an
  unnamed file with `openat(dir, ".", O_TMPFILE | O_RDWR | O_CLOEXEC, 0600)`,
  copies at most 256 MiB (one more byte is `ErrTooLarge`), sets owner
  `root:root` (`fchown 0 0`) and mode `0640`, `fsync`s the file, publishes it
  with `linkat(fd, "", dir, name, AT_EMPTY_PATH)`, then `fsync`s the
  directory.
- `linkat` never replaces: an existing name is `ErrConflict`. Any earlier
  failure or cancellation closes the unnamed file, so no partial or
  overwritten name is ever visible. An empty file is a valid upload.
- No named-file fallback: when the filesystem refuses `O_TMPFILE`
  (`EOPNOTSUPP`, `EINVAL`) the upload is `ErrUnavailable`, while list and
  download keep working.
- Web side: the body is capped at 256 MiB + 1 MiB. The first multipart part
  MUST be the form field `csrf` (at most 4 KiB), checked with
  `host.ValidateActionToken` before any file byte is read. The second part
  MUST be the file field `file`, and it MUST be the last part. The handler
  pipes the part into the broker without a spool file. Success and mapped
  failures redirect to the directory with a notice: `HX-Redirect` with `204`
  for HTMX, else `303`.

### Streaming broker contract (`internal/broker`)

- Stream query: `POST /v1/stream-queries/{id}` with the normal JSON body
  (8 KiB cap). The response carries `Content-Length` (the client refuses a
  response without one), `Content-Type`, and the filename in
  `Pilothouse-Stream-Name` as unpadded base64url. The registry's default
  timeout is 30 seconds when `Timeout` is 0; a result larger than `Limit` is
  `ErrStreamTooLarge`.
- Stream action: `POST /v1/stream-actions/{id}`; the raw body is the stream,
  and the parameters travel in `Pilothouse-Stream-Metadata` as unpadded
  base64url JSON, at most 8 KiB before and after decoding. A
  `Content-Length` above `Limit` is refused with `413` before dispatch; a
  chunked body is allowed and wrapped in a `Limit + 1` reader, so excess data
  fails with `ErrStreamTooLarge` (`413`).
- Both registries require the exact registered parameter set; each value is
  at most 4 KiB with no control characters, and names plus values total at
  most 8 KiB (`validateStreamParameters`). Admin registrations refuse a
  non-admin identity, which the broker refreshes before dispatch.
- Stream action order: resolve `Resource` and `LockResource` (each at most
  1024 bytes, no CR, LF or NUL), take the per-resource lock, write the audit
  intent, run the handler under the timeout (default 15 minutes), then record
  the outcome. The audit failure category is only `timeout`, `cancelled` or
  `operation_failed` (`errorCategory`).
- Files upload audit and lock resource: `files/<root>/<directory>/<name>`
  (`files/<root>/<name>` at the root), at most 768 bytes of destination
  (`filesResource`). Uploads to one destination serialize; other
  destinations run in parallel.

### Status mapping (`filesPublicError`)

| Domain error | HTTP status | Public message | Category |
| --- | --- | --- | --- |
| `ErrInvalid` | 400 | invalid files request | `invalid_request` |
| `ErrReadOnly` | 403 | files root is read-only | `read_only` |
| `ErrNotFound` | 404 | files resource not found | `not_found` |
| `ErrConflict` | 409 | files conflict | `conflict` |
| `ErrTooLarge` | 413 | file transfer is too large | `too_large` |
| `ErrUnavailable` or any other error | 503 | files service unavailable | `unavailable` |

The broker returns only the public message; raw operating-system errors never
cross it. A non-admin web request renders the "Administrator access required"
page with no broker call; a non-admin broker request gets `403`. A listing
failure renders an in-page missing (`404`) or unavailable state. A download
error before headers uses the mapped status; after streaming starts the
response is cut. Upload failures redirect with a fixed notice
(`409` → name exists, `403` → uploads disabled, else see Activity).

### Tests that pin this contract

- `internal/modules/files/config_test.go`: root flags, ID bounds, `=` in
  paths, duplicates, `/`, final symlink, 65th root, descriptor close.
- `internal/modules/files/manager_linux_test.go`: `TestValidateRelativePath`,
  `TestListShowsButNeverTraversesSymlinks`,
  `TestListAppliesScanEntryAndJSONBounds`,
  `TestListProductionJSONBudgetLeavesBrokerEnvelopeHeadroom`,
  `TestDownloadKeepsValidatedDescriptorAfterRename`,
  `TestDownloadRejectsFIFOWithoutBlocking`,
  `TestUploadPublishesCompleteFilesWithRestrictedMetadata`,
  `TestUploadNeverPublishesPartialFile`,
  `TestUploadCleansUpOnCancellationAndSyscallFailures`.
- `internal/modules/files/module_test.go`: Range refusal, CSRF-first and
  final-part ordering, size limit, notices, `HX-Redirect`.
- `cmd/pilothoused/main_test.go`: `TestRegisterFilesRequiresAdministratorAndUsesFixedParameters`,
  `TestRegisterFilesMapsDomainErrorsToPublicErrors`,
  `TestRegisterFilesBoundsAndAuditsUploadDestination`.
- `internal/broker/streams_test.go` and `internal/broker/server_test.go`:
  stream registration, parameter validation, limits, serialization,
  cancellation, chunked bodies, metadata limit, backpressure.

## References

- Rationale: [ADR-0001](../adr/0001-versioned-broker-wire-surface.md) (the
  `/v1` stream routes and headers),
  [ADR-0002](../adr/0002-audited-resource-key-doubles-as-lock-key.md) (the
  audited resource is the lock key)
- Context: [design/overview.md](../design/overview.md),
  [modules.md](../modules.md), [install.md](../install.md) (operator setup)
