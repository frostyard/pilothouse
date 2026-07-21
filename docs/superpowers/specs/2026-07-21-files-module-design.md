# Files Module Design

## Purpose

Add the first independently shippable Pilothouse equivalent of Cockpit Files. The module gives administrators a bounded file browser for explicitly configured host directories while preserving Pilothouse's fixed-operation broker boundary.

The module appears in navigation at `/files` and does not add a dashboard card. It is an administrator-only operational tool, not a general user home-directory browser.

## Scope

The first milestone will:

- Browse explicitly configured directory roots.
- Support separate read-only and read/write roots.
- Navigate directories with breadcrumbs.
- Show bounded file metadata.
- Filter entries by name, sort entries, and toggle hidden entries.
- Show symbolic links without following them.
- Download regular files up to 256 MiB.
- Upload one regular file at a time, up to 256 MiB, into read/write roots.
- Reject uploads when the destination name already exists.
- Work without JavaScript, with HTMX used only as optional progressive enhancement.

The first milestone will not:

- Browse the entire host filesystem through an unrestricted `/` root.
- Provide file previews, inline display, or HTTP range downloads.
- Edit files or create empty files and directories.
- Rename, copy, move, or delete entries.
- Create or follow symbolic links.
- Change ownership, groups, permissions, ACLs, extended attributes, or SELinux labels.
- Create or extract archives.
- Support multi-selection, drag-and-drop, upload progress JavaScript, live filesystem watching, or polling.
- Impersonate non-administrator login UIDs or enforce per-user Unix access.

Those capabilities require separate designs after the path and transfer boundary has shipped and been evaluated.

## Architecture

Add a vertical slice under `internal/modules/files` containing the web module, presentation models, root and relative-path validation, privileged filesystem manager, templ views, and focused tests. The unprivileged web process receives no direct filesystem access.

`cmd/pilothouse` registers the presentation module. `cmd/pilothoused` constructs the privileged manager from configured roots and registers three administrator-only capabilities:

- `org.frostyard.pilothouse.files.list`, a fixed JSON query for roots and directory entries.
- `org.frostyard.pilothouse.files.download`, a fixed streaming query for one regular file.
- `org.frostyard.pilothouse.files.upload`, a fixed streaming action for one new regular file.

The broker will gain narrowly typed streaming query and action registries alongside its existing JSON registries. A streaming registration declares a fixed ID, administrator requirement, exact metadata parameters, transfer limit, timeout, and handler. Streaming actions additionally declare audit resource and lock-resource resolvers. The broker refreshes the system identity before dispatch exactly as it does for existing queries and actions.

The extension is not a generic stream proxy. Only composition-root registrations can add implementations, and each registration accepts a narrow metadata map. It cannot select an arbitrary URL, socket, command, or absolute filesystem path.

## Root Configuration

Roots are configured only on `pilothoused` with repeatable flags:

```text
--files-root <id>=<absolute-path>
--files-write-root <id>=<absolute-path>
```

`--files-root` creates a read-only root. `--files-write-root` creates a root that permits uploads in addition to listing and downloading. Splitting the flags keeps read-only behavior as the explicit default. The value is split on its first `=`, so later `=` characters remain part of the path.

A root ID must match `[a-z0-9][a-z0-9-]{0,31}`. Startup rejects:

- Duplicate IDs across either flag.
- Empty or relative paths.
- The filesystem root `/`.
- Paths that do not exist or are not directories.
- Roots that cannot be opened safely.

Nested configured roots are allowed because a read-only parent and narrower read/write child is a useful explicit policy. Each root is evaluated independently. The manager opens and retains a directory descriptor for every configured root at startup; requests resolve from that descriptor rather than reinterpreting its configured absolute path. The manager closes all descriptors during daemon shutdown.

If no roots are configured, the module remains in navigation and renders a stable administrator-facing configuration-empty state. The broker registers all three fixed operations, but listing returns no roots and transfers reject unknown root IDs.

The web process learns only safe root summaries from the list query: ID, configured absolute path for display, and read/write mode. It does not need matching mounts or configuration.

## Broker Streaming Contract

The broker client and server add explicit streaming methods without weakening the existing 8 KiB JSON request and 2 MiB JSON response limits.

### Streaming Queries

A streaming query uses `POST /v1/stream-queries/{id}` with the same bounded JSON metadata envelope as an ordinary query. A successful response supplies bounded, sanitized metadata headers and a raw response body. The client exposes the response as an `io.ReadCloser` plus declared size, media type, and download filename. It does not buffer the body.

For Files downloads, the only metadata parameters are `root` and `path`. The server does not emit the configured absolute path or raw operating-system errors.

### Streaming Actions

A streaming action uses `POST /v1/stream-actions/{id}`. It carries a URL-safe base64 encoding of a bounded JSON metadata envelope in one `Pilothouse-Stream-Metadata` header and uses the raw HTTP request body as the stream. The header is limited to 8 KiB before decoding and accepts only registered parameter names. Files upload metadata contains only `root`, `directory`, and `name`.

When `Content-Length` is present, the broker rejects a value above the registered transfer limit before dispatch. A missing length permits chunked forwarding from a browser multipart part. In both cases, the broker wraps the body in a `limit + 1` reader and fails the action if the actual content exceeds the limit. The web client therefore does not need to buffer a multipart file merely to determine its size.

The streaming action registry applies administrator authorization, exact-parameter validation, destination locking, audit begin/completion, timeout, cancellation, and stable error mapping. The upload audit resource is `files/<root-id>/<relative-destination>`. Uploads to the same destination serialize; unrelated destinations may proceed concurrently.

The operation has a 15-minute deadline. Client cancellation closes the Unix-socket request and propagates to the filesystem handler.

## Filesystem Manager

The privileged manager has three operations with narrow input and output types:

- `List` accepts a root ID, relative directory, filter, sort field, direction, and hidden-entry flag, then returns root summaries, the active directory, bounded entries, and truncation state.
- `Download` accepts a root ID and relative file path, then returns an opened regular file and sanitized transfer metadata.
- `Upload` accepts a root ID, relative directory, filename, and reader, then atomically creates one regular file.

The manager never accepts an absolute request path. A request relative path is represented as normalized slash-separated segments. It rejects absolute paths, empty interior segments, `.`, `..`, NULs, control characters, segments longer than the filesystem name limit, and total paths beyond a fixed 768-byte request bound. The path bound keeps upload audit resource names within the broker's existing 1 KiB resource limit. An empty relative directory identifies the configured root.

### Descriptor-Based Resolution

Descendants are opened on Linux with `openat2` constraints equivalent to:

```text
RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS | RESOLVE_NO_SYMLINKS
```

Containment therefore does not depend on string-prefix checks or pre-resolution followed by a vulnerable second open. Symbolic links in any traversed position fail resolution. Mounts genuinely located beneath a configured root remain accessible.

Directory listing uses no-follow metadata operations for each child. For a symbolic-link row, the manager may call `readlinkat` on that final directory entry and return a bounded target string for display. The target is never resolved or used as input to another operation.

### Directory Listing

The list query accepts exactly these parameters:

| Parameter | Values |
| --- | --- |
| `root` | Configured root ID, or empty only when requesting root summaries. |
| `path` | Valid relative directory path; empty selects the root. |
| `filter` | Trimmed case-insensitive name substring, at most 200 Unicode code points and 1 KiB. |
| `sort` | `name`, `size`, `modified`, `owner`, or `permissions`; defaults to `name`. |
| `direction` | `asc` or `desc`; defaults to `asc`. |
| `hidden` | `true` or `false`; defaults to `false`. |

The manager scans at most 10,000 child names and returns at most 2,000 matching entries while keeping the serialized JSON result below the broker's 2 MiB response cap. Reaching either scan, entry, or encoded-size coverage limit marks the result truncated. Directories sort before all other entry types in either direction; the selected field then orders entries, with name as a deterministic tie-breaker.

Each entry contains only:

- Base name.
- Type: regular file, directory, symbolic link, or other.
- Size for regular files.
- Modification time.
- Numeric UID and GID.
- Resolved owner and group names when lookup succeeds.
- Permission and special-mode bits.
- Bounded symbolic-link target text when the entry is a link.

Owner or group lookup failure retains the numeric ID instead of failing the listing. An entry disappearing during enumeration is skipped. Failure to open or enumerate the requested directory fails the query. No file content is read while listing.

### Download

Download opens the final path beneath the selected root with no symlink traversal and requires a regular file. The manager obtains metadata from the opened descriptor, rejects files larger than 256 MiB, and returns that same descriptor for streaming so a rename cannot switch the downloaded object after validation.

The broker streams exactly the validated size. The web response uses `application/octet-stream`, `Content-Length`, `X-Content-Type-Options: nosniff`, and a safely encoded attachment filename derived only from the final base name. Range requests are rejected rather than forwarded.

### Upload

Upload requires a read/write root. The destination directory is opened beneath its root descriptor with no symlink traversal. The filename must be one valid base-name segment and cannot be `.`, `..`, or contain a path separator, NUL, or control character.

The manager creates an unnamed regular file in the destination filesystem with `O_TMPFILE`. It streams through a 256 MiB hard limit, rejects excess data, applies ownership `root:root` and mode `0640`, syncs the file, and links it to the requested filename with `linkat(AT_EMPTY_PATH)`. The link operation fails if that name already exists. The manager then syncs the destination directory and closes the file before returning success. Empty files are valid uploads. A filesystem that does not support `O_TMPFILE` remains browsable and downloadable but reports uploads as unavailable; the implementation does not fall back to a partially visible named temporary file.

Any read, write, sync, metadata, link, close, cancellation, deadline, or collision failure closes the unnamed file and releases it without creating a directory entry. The requested destination name is never partially visible, and an existing destination is never overwritten.

## Web Routes And Data Flow

The module registers:

```text
GET  /files
GET  /files/{rootID}
GET  /files/{rootID}/download
POST /files/{rootID}/upload
```

`GET /files` obtains root summaries through the fixed list query and redirects to the first root by ID. If there are no configured roots, it renders the configuration-empty page.

`GET /files/{rootID}?path=<directory>&filter=<text>&sort=<field>&direction=<direction>&hidden=<bool>` checks the web session's administrator flag before making the fixed list query. It normalizes safe filter and sort defaults for ordinary browser URLs; the broker independently rejects unsupported direct parameters or malformed values. Breadcrumbs are built from validated relative segments, and browser-visible URLs never contain absolute host paths.

`GET /files/{rootID}/download?path=<file>` checks administrator access and rejects Range requests, then calls the fixed streaming query and pipes the broker body directly to the browser. It does not buffer or inspect file content.

`POST /files/{rootID}/upload?path=<directory>` accepts `multipart/form-data`. The web handler parses parts sequentially, validates request origin and the CSRF token before accepting file bytes, and permits exactly one file. The CSRF field must precede the file part, and the file part must be the final part. The handler copies the file part into a pipe connected to the broker request, verifies that the multipart stream ends after that part, and only then closes the pipe successfully. It rejects multiple files, unexpected parts, empty filenames, path separators, dot names, control characters, oversized headers, and bodies beyond the multipart overhead plus 256 MiB.

After web validation, the file part streams directly into the fixed broker action. The web process does not create a transfer spool or temporary upload file. A successful upload redirects to the active directory with a notice. An HTMX request receives an `HX-Redirect`; a normal form receives a 303 response.

The platform host gains explicit streaming query/action methods and a streaming-action validation path that can validate origin and a supplied multipart CSRF value without invoking `ParseMultipartForm`. Existing JSON query/action behavior and limits remain unchanged.

## Interface

The Files page follows Pilothouse's existing design language rather than reproducing Cockpit's PatternFly implementation.

A root switcher and breadcrumb trail appear above a compact toolbar. The toolbar contains:

- A name filter.
- A hidden-entry toggle.
- The displayed result count.
- A truncation disclosure when coverage limits were reached.
- An **Upload file** control only for read/write roots.

The filter and hidden controls submit normal GET requests. Sortable column headings are links that preserve the active root, directory, filter, and hidden state while setting field and direction.

The details table shows Name, Size, Modified, Owner, Group, and Permissions. Directories are navigable links. Regular filenames are download links. Symbolic links and other entries are plain text with quiet type badges; the symbolic-link target is displayed as escaped metadata and is not actionable.

The page distinguishes:

- No configured roots.
- An empty directory.
- No entries matching the active filter.
- A truncated result.
- A read-only root.
- An inaccessible or missing directory.
- A temporarily unavailable broker or filesystem operation.
- Non-administrator access.

The upload form uses a normal file picker and submit button. It states the 256 MiB maximum and that existing names are never overwritten. It does not claim upload progress before the browser completes the request.

On narrow screens, Name remains primary and metadata stacks beneath it. Toolbar controls wrap without widening the page. The table may scroll inside its own container as a final fallback. A normal refresh reflects external filesystem changes; the module does not poll.

## Authorization And Security

Every web route checks `host.Identity(r).Admin` before contacting the broker. Every broker registration independently requires a freshly resolved administrator identity. Removing a user from the configured admin group therefore blocks the next operation even if the browser session still exists.

Configured root IDs, relative paths, fixed operation IDs, exact metadata parameters, descriptor-based resolution, no-symlink traversal, count limits, byte limits, timeouts, and cancellation form the privilege boundary. The module never grants the web process arbitrary filesystem access or turns the broker into a generic path service.

Filename, target, owner, group, and error text are always HTML-escaped. Responses never disclose configured absolute paths except the explicit root path shown to authenticated administrators in the root selector, and never include raw privileged operating-system errors.

## Error Handling

Errors map to stable web outcomes:

| Status | Condition |
| --- | --- |
| `400 Bad Request` | Malformed root, relative path, filter, sort, multipart request, or filename. |
| `403 Forbidden` | Non-administrator access or upload to a read-only root. |
| `404 Not Found` | Unknown root or missing, symlink, or non-regular download target. |
| `409 Conflict` | Upload destination already exists. |
| `413 Content Too Large` | Upload or download exceeds 256 MiB. Directory coverage limits instead return a successful partial listing marked truncated. |
| `503 Service Unavailable` | Broker or filesystem operation is unavailable. |

Listing failures render an in-page inaccessible or unavailable state without privileged details. Download errors discovered before response headers produce the mapped status. If a read fails after streaming begins, the server terminates the response because an HTTP error body can no longer replace it.

Upload failures remove temporary state and redirect back with a stable error notice. The privileged server log and audit record retain only a bounded error category such as `invalid_request`, `not_found`, `read_only`, `conflict`, `too_large`, `timeout`, `cancelled`, or `operation_failed`.

## Testing

### Configuration Tests

Cover:

- Valid read-only and read/write roots.
- Duplicate IDs across both flag types.
- Invalid and empty IDs.
- Relative, missing, non-directory, and `/` paths.
- Paths containing `=` after the first delimiter.
- Nested roots with independent modes.
- Empty configuration behavior.
- Descriptor cleanup.

### Manager Tests

Use temporary directories and cover:

- Root and nested-directory listing.
- Regular files, directories, symbolic links, and other entry types.
- Metadata and owner/group numeric fallback.
- Case-insensitive filtering, hidden entries, every sort field, both directions, directory-first order, and deterministic ties.
- Entry, scan, and encoded-size truncation.
- Entries disappearing during enumeration.
- Every invalid path form and root-escape attempt.
- Symlink display with no navigation, download, upload-directory, or intermediate traversal.
- Mount behavior where the test environment permits it.
- Descriptor-stable download after rename.
- Regular-file and 256 MiB download enforcement.
- Atomic upload success, empty files, mode and ownership, collisions, cancellation, timeout, excess bodies, write failures, sync failures, unsupported `O_TMPFILE`, and unnamed-file cleanup.
- Read-only root enforcement.

### Broker Tests

Cover:

- Fixed streaming registration and duplicate rejection.
- Refreshed administrator authorization.
- Exact metadata parameter validation.
- Declared and actual transfer limits.
- Timeout and cancellation propagation.
- Per-destination serialization.
- Upload audit resource, success, failure, and stable categories.
- Existing JSON request and response limits remaining unchanged.
- No generic stream handler or arbitrary path registration in the web composition root.

### Handler Tests

Use fake JSON and streaming host implementations to cover:

- Administrator access and non-administrator denial without broker calls.
- Root/path/filter/sort encoding.
- Safe defaults for browser query parameters.
- Origin and CSRF validation before file forwarding.
- Multipart ordering, unexpected fields, multiple files, invalid filenames, oversized bodies, and cancellation.
- Download content headers, Range rejection, and body forwarding.
- HTMX and ordinary redirects after uploads.
- Stable notices and absence of privileged error disclosure.

### Rendering Tests

Cover:

- Root selector and read/write indicators.
- Breadcrumb links.
- Filter, hidden toggle, result count, and sort links preserving state.
- Metadata for all supported entry types.
- Upload control present only on read/write roots.
- Configuration-empty, directory-empty, filtered-empty, truncated, inaccessible, unavailable, and access-denied states.
- Symbolic links rendered without actionable navigation or download links.
- Escaped filenames and target strings.
- Responsive markup.
- Rendered component output with no literal `@web.` syntax.

### Transport Tests

Exercise the real broker client and server over a Unix socket with bounded payloads to cover:

- Streaming without whole-body buffering.
- Backpressure.
- Interrupted uploads and downloads.
- Oversized declared and actual request bodies.
- Chunked request bodies without a declared length.
- Client cancellation and server deadlines.
- Stream closure and connection reuse after successful transfers.

After editing templ files, run `make generate`. Before handoff, run `make build`, `make test`, `make fmt`, and `make lint`, using the matching Docker targets if native dependencies are unavailable.
