# Files Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an administrator-only file browser with bounded listing, download, and collision-safe upload access under explicitly configured host directories.

**Architecture:** Add fixed streaming query/action primitives to the broker, then implement a Linux descriptor-anchored filesystem manager under `internal/modules/files`. The web module uses only fixed broker IDs and server-rendered templ views; it never accesses the host filesystem directly.

**Tech Stack:** Go 1.26.3, standard `net/http`, `golang.org/x/sys/unix`, templ, HTMX only for optional redirects, vanilla CSS, testify.

## Global Constraints

- Keep all Files feature code under `internal/modules/files`; register privileged implementations only in `cmd/pilothoused`.
- The module is administrator-only and exposes no dashboard card.
- Accept at most 64 configured roots; reject `/`, relative roots, duplicate IDs, and root IDs outside `[a-z0-9][a-z0-9-]{0,31}`.
- Permit listing and download on read-only roots; permit upload only on roots configured with `--files-write-root`.
- Accept only relative descendant paths up to 768 bytes; reject absolute paths, `.`, `..`, empty interior segments, NULs, controls, and symlink traversal.
- List at most 2,000 matching entries after scanning at most 10,000 names, and keep JSON below 2 MiB.
- Stream only regular files up to 256 MiB; do not buffer whole transfers in either process.
- Show symlinks and bounded target text, but never navigate, download, or upload through them.
- Upload with `O_TMPFILE`, mode `0640`, owner `root:root`, and `linkat(AT_EMPTY_PATH)` no-replace publication; never expose partial or overwritten files.
- Use a 15-minute transfer timeout and propagate cancellation.
- Never add arbitrary command execution, generic filesystem reads, filesystem paths outside configured roots, or a generic stream proxy.
- Run `make generate` after templ edits; never hand-edit generated `*_templ.go` files.
- For each changed templ component invocation, test that rendered HTML contains component output and no literal `@web.` syntax.
- Before handoff run `make build`, `make test`, `make fmt`, and `make lint`, using Docker equivalents only if native dependencies are unavailable.

---

## File Structure

- `internal/broker/api.go`: fixed Files IDs, stream protocol headers, and stream response metadata.
- `internal/broker/streams.go`: fixed streaming registries, authorization, parameter validation, locking, audit completion, limits, and public error mapping.
- `internal/broker/streams_test.go`: registry unit tests for authorization, validation, limits, locks, audit, and cancellation.
- `internal/broker/client.go`: Unix-socket streaming client methods.
- `internal/broker/server.go`: authenticated streaming HTTP routes.
- `internal/broker/server_test.go`: end-to-end client/server stream protocol tests.
- `internal/platform/module.go`: stream methods and explicit-CSRF validation on `Host`.
- `internal/web/server.go`: platform-to-broker stream forwarding and streaming form validation.
- `internal/web/server_test.go`: stream forwarding and explicit token/origin tests.
- `internal/modules/files/model.go`: Files constants, request/response models, entry types, manager interface, and domain errors.
- `internal/modules/files/config.go`: root flag parsing and validated root specifications.
- `internal/modules/files/config_test.go`: root syntax, duplicate, and invalid-directory tests.
- `internal/modules/files/manager_linux.go`: root descriptors, `openat2` resolution, bounded listings, downloads, and atomic uploads.
- `internal/modules/files/manager_linux_test.go`: real-filesystem manager tests.
- `internal/modules/files/module.go`: manifest, browser filter normalization, URL builders, and redirect helper.
- `internal/modules/files/handler.go`: listing, download, and multipart upload routes.
- `internal/modules/files/module_test.go`: manifest, authorization, query/stream dispatch, multipart, and response tests.
- `internal/modules/files/views.templ`: root selector, breadcrumbs, toolbar, table, upload form, and all states.
- `internal/modules/files/views_test.go`: rendering and escaping tests.
- `internal/web/static/app.css`: Files toolbar, breadcrumbs, metadata rows, and mobile presentation.
- `cmd/pilothoused/main.go`: root flags, manager lifecycle, and fixed privileged registrations.
- `cmd/pilothoused/main_test.go`: Files registration and authorization tests.
- `cmd/pilothouse/main.go`: Files presentation module registration.
- `docs/modules.md`: fixed stream operation and configured-root guidance.
- `README.md`: feature summary and root configuration examples.

---

### Task 1: Fixed Streaming Registries

**Files:**
- Create: `internal/broker/streams.go`
- Create: `internal/broker/streams_test.go`
- Modify: `internal/broker/api.go`

**Interfaces:**
- Produces: `StreamResult`, `StreamQueryDefinition`, `StreamActionDefinition`, `StreamQueryRegistry`, `StreamActionRegistry`, `NewStreamQueryRegistry`, `NewStreamActionRegistry`, `Register`, `Execute`, `NewPublicError`, and `StatusCode`.
- Consumes: existing `auth.Identity`, `auditStore`, `resourceLocks`, `validateParameters` conventions, and `errorCategory`.

- [ ] **Step 1: Write failing registry tests**

Add table-driven tests that register one administrator-only query and action and assert exact parameter names, empty values allowed, unknown names rejected, metadata values over 4 KiB rejected, non-admin identities rejected before handlers run, query bodies over the declared result size rejected and closed, action bodies over `Limit` rejected, same-resource actions serialize, cancellation releases locks, and audit records receive `succeeded`, `failed`, `timeout`, and `cancelled` outcomes.

```go
func TestStreamRegistriesAuthorizeValidateAndLimit(t *testing.T) {
    queries := NewStreamQueryRegistry()
    require.NoError(t, queries.Register(StreamQueryDefinition{
        ID: "test.download", Admin: true, Parameters: []string{"path"}, Limit: 4,
        Handler: func(context.Context, auth.Identity, map[string]string) (StreamResult, error) {
            return StreamResult{Body: io.NopCloser(strings.NewReader("five!")), Size: 5}, nil
        },
    }))
    _, err := queries.Execute(context.Background(), auth.Identity{}, "test.download", map[string]string{"path": "file"})
    assert.ErrorContains(t, err, "not authorized")
    _, err = queries.Execute(context.Background(), auth.Identity{Admin: true}, "test.download", map[string]string{"path": "file", "extra": "x"})
    assert.ErrorContains(t, err, "parameters")
    _, err = queries.Execute(context.Background(), auth.Identity{Admin: true}, "test.download", map[string]string{"path": "file"})
    assert.ErrorIs(t, err, ErrStreamTooLarge)
}
```

- [ ] **Step 2: Run the focused tests to verify failure**

Run: `go test ./internal/broker -run 'TestStream' -count=1`

Expected: FAIL because streaming registry types do not exist.

- [ ] **Step 3: Add fixed IDs and protocol types**

Add these declarations to `internal/broker/api.go`:

```go
const (
    ActionFilesUpload  = "org.frostyard.pilothouse.files.upload"
    QueryFilesDownload = "org.frostyard.pilothouse.files.download"
    QueryFilesList     = "org.frostyard.pilothouse.files.list"
)

const (
    StreamMetadataHeader = "Pilothouse-Stream-Metadata"
    StreamNameHeader     = "Pilothouse-Stream-Name"
)

type StreamResult struct {
    Body      io.ReadCloser
    Filename  string
    MediaType string
    Size      int64
}
```

Add `io` to the file imports.

- [ ] **Step 4: Implement query and action registries**

Implement these exact definitions in `internal/broker/streams.go`:

```go
var ErrStreamTooLarge = errors.New("stream exceeds registered limit")

type StreamQueryHandler func(context.Context, auth.Identity, map[string]string) (StreamResult, error)
type StreamActionHandler func(context.Context, auth.Identity, map[string]string, io.Reader) error

type StreamQueryDefinition struct {
    ID string
    Admin bool
    Parameters []string
    Limit int64
    Timeout time.Duration
    Handler StreamQueryHandler
}

type StreamActionDefinition struct {
    ID string
    Admin bool
    Parameters []string
    Limit int64
    Timeout time.Duration
    Resource func(map[string]string) (string, error)
    LockResource func(map[string]string) (string, error)
    Handler StreamActionHandler
}

type PublicError struct {
    Status int
    Message string
    Category string
    Err error
}

func NewPublicError(status int, message, category string, err error) error
func PublicErrorDetails(err error) (status int, message, category string)
func StatusCode(err error) int
```

Use sorted/deduplicated parameter declarations at registration. `validateStreamParameters` must require the exact key set, permit empty values, reject NUL/CR/LF/control bytes, reject individual values over 4 KiB, and reject total encoded metadata over 8 KiB. Default timeouts are 30 seconds for queries and 15 minutes for actions. Query execution must close invalid or over-limit results. Action execution must use `io.LimitReader(body, Limit+1)`, per-resource locks, and existing audit begin/complete semantics. Match `NewActionRegistry` by defining `NewStreamActionRegistry(stores ...auditStore)` so tests may omit audit storage. `StatusCode` returns a `PublicError` or broker response status and defaults to 503 for unknown failures.

- [ ] **Step 5: Run registry tests**

Run: `go test ./internal/broker -run 'TestStream' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the registry primitive**

```bash
git add internal/broker/api.go internal/broker/streams.go internal/broker/streams_test.go
git commit -m "feat: add fixed broker stream registries"
```

---

### Task 2: Streaming Broker HTTP Protocol

**Files:**
- Modify: `internal/broker/client.go`
- Modify: `internal/broker/server.go`
- Modify: `internal/broker/server_test.go`
- Modify: `cmd/pilothoused/main.go`

**Interfaces:**
- Consumes: registries and `StreamResult` from Task 1.
- Produces: `Client.StreamQuery(ctx, token, id, parameters) (StreamResult, error)` and `Client.StreamAction(ctx, token, id, parameters, body) error`.

- [ ] **Step 1: Write failing protocol tests**

Extend the broker integration fixture with stream registries and test a successful 4-byte download, chunked upload, oversized declared body, oversized actual body, filename header round trip, refreshed administrator denial, body closure, backpressure, and cancellation. Start `http.Server` on `net.Listen("unix", filepath.Join(t.TempDir(), "broker.sock"))` and exercise it through `NewClient(socket)` so the production Unix transport is covered; retain `handlerTransport` only for ordinary JSON tests.

```go
func TestBrokerClientStreamsQueryAndChunkedAction(t *testing.T) {
    var uploaded strings.Builder
    streamQueries := NewStreamQueryRegistry()
    streamActions := NewStreamActionRegistry(&memoryAudit{})
    require.NoError(t, streamQueries.Register(StreamQueryDefinition{
        ID: "test.download", Admin: true, Parameters: []string{"path"}, Limit: 8,
        Handler: func(context.Context, auth.Identity, map[string]string) (StreamResult, error) {
            return StreamResult{Body: io.NopCloser(strings.NewReader("data")), Filename: "a b.txt", MediaType: "application/octet-stream", Size: 4}, nil
        },
    }))
    require.NoError(t, streamActions.Register(StreamActionDefinition{
        ID: "test.upload", Admin: true, Parameters: []string{"name"}, Limit: 8,
        Resource: func(p map[string]string) (string, error) { return "test/" + p["name"], nil },
        Handler: func(_ context.Context, _ auth.Identity, _ map[string]string, body io.Reader) error {
            _, err := io.Copy(&uploaded, body)
            return err
        },
    }))
    // Log in, call StreamQuery and StreamAction, then assert body, metadata, and uploaded content.
}
```

- [ ] **Step 2: Run protocol tests to verify failure**

Run: `go test ./internal/broker -run 'TestBrokerClientStreams' -count=1`

Expected: FAIL because server routes and client methods are missing.

- [ ] **Step 3: Wire stream registries into the broker server**

Change the constructor to:

```go
func NewServer(
    authenticator auth.Authenticator,
    resolver auth.Resolver,
    sessions *SessionStore,
    actions *ActionRegistry,
    queries *QueryRegistry,
    streamActions *StreamActionRegistry,
    streamQueries *StreamQueryRegistry,
    logger *slog.Logger,
) *Server
```

Register `POST /v1/stream-queries/{id}` and `POST /v1/stream-actions/{id}`. Stream queries decode the existing 8 KiB `QueryRequest`, call the registry, set `Content-Length`, `Content-Type`, and base64url `StreamNameHeader`, write status 200, copy exactly `Size`, and always close `Body`. Stream actions decode a base64url `QueryRequest` from `StreamMetadataHeader`, reject headers over 8 KiB, honor an over-limit `Content-Length` before dispatch, and pass the raw request body to the action registry. Map `PublicError` to its stable status/message; return no raw handler error.

- [ ] **Step 4: Implement client methods without buffering**

Add:

```go
func (c *Client) StreamQuery(ctx context.Context, token, id string, parameters map[string]string) (StreamResult, error)
func (c *Client) StreamAction(ctx context.Context, token, id string, parameters map[string]string, body io.Reader) error
```

`StreamQuery` sends a normal JSON `QueryRequest` but does not use `do`, because a successful body may be 256 MiB. Parse and validate non-negative `Content-Length`, decode `StreamNameHeader`, and return the live body. `StreamAction` base64url-encodes `QueryRequest{Parameters: parameters}` into `StreamMetadataHeader`; preserve `Content-Length` only when the supplied reader exposes a known remaining length, otherwise allow chunked transfer. Both methods use the existing bearer token and translate non-2xx bodies through a 4 KiB limit into a response error consumed by `broker.StatusCode`.

- [ ] **Step 5: Update all `NewServer` call sites**

Pass empty stream registries from existing tests and from `cmd/pilothoused/main.go`:

```go
streamActions := broker.NewStreamActionRegistry(auditStore)
streamQueries := broker.NewStreamQueryRegistry()
handler := broker.NewServer(authenticator, resolver, sessions, actions, queries, streamActions, streamQueries, logger)
```

- [ ] **Step 6: Run broker and daemon tests**

Run: `go test ./internal/broker ./cmd/pilothoused -count=1`

Expected: PASS, including unchanged JSON request/response limit tests.

- [ ] **Step 7: Commit the HTTP protocol**

```bash
git add internal/broker/client.go internal/broker/server.go internal/broker/server_test.go cmd/pilothoused/main.go
git commit -m "feat: stream fixed broker operations"
```

---

### Task 3: Platform And Web Stream Plumbing

**Files:**
- Modify: `internal/platform/module.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`
- Modify: `internal/modules/logs/module_test.go`
- Modify: `internal/modules/services/module_test.go`
- Modify: `internal/modules/incus/module_test.go`

**Interfaces:**
- Consumes: broker client stream methods from Task 2.
- Produces: stream methods on `platform.Host`, `web.BrokerClient`, and `web.Server`.

- [ ] **Step 1: Write failing web forwarding and validation tests**

Extend `fakeBroker` to capture stream IDs, parameters, and bodies. Add tests proving no-session forwarding returns `broker.ErrUnauthorized`, valid forwarding uses the session token, and `ValidateActionToken` rejects missing sessions, wrong CSRF values, and foreign origins without reading or parsing the body.

```go
func TestValidateActionTokenChecksExplicitCSRFWithoutReadingBody(t *testing.T) {
    server := newTestServer(t)
    body := &countingReader{Reader: strings.NewReader("unread")}
    request := httptest.NewRequest(http.MethodPost, "/files/root/upload", body)
    request = withTestSession(request, "csrf", "token")
    response := httptest.NewRecorder()

    assert.True(t, server.ValidateActionToken(response, request, "csrf"))
    assert.Zero(t, body.reads)
}
```

- [ ] **Step 2: Run focused tests to verify failure**

Run: `go test ./internal/web -run 'Test(Stream|ValidateActionToken)' -count=1`

Expected: FAIL because the interfaces and methods are missing.

- [ ] **Step 3: Extend platform and broker client interfaces**

Add to `platform.Host`:

```go
StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error
StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error)
ValidateActionToken(http.ResponseWriter, *http.Request, string) bool
```

Add matching token-aware methods to `web.BrokerClient`:

```go
StreamAction(context.Context, string, string, map[string]string, io.Reader) error
StreamQuery(context.Context, string, string, map[string]string) (broker.StreamResult, error)
```

Import `io` and `internal/broker` in `internal/platform/module.go`.

- [ ] **Step 4: Implement web server forwarding**

Implement `StreamAction` and `StreamQuery` exactly like existing `Execute` and `Query`, obtaining the opaque token from request context. Implement `ValidateActionToken` with constant-time CSRF comparison followed by `validateOrigin`; it must not call `ParseForm`, `ParseMultipartForm`, or read `r.Body`.

- [ ] **Step 5: Update existing test hosts**

Add no-op `StreamAction`, `StreamQuery`, and `ValidateActionToken` methods to `logsHost`, Services' `testHost`, and Incus' `fakeHost`. Use `io.NopCloser(strings.NewReader(""))` only where a non-empty stream result is required by a test; otherwise return a zero result.

- [ ] **Step 6: Run platform-facing tests**

Run: `go test ./internal/web ./internal/modules/logs ./internal/modules/services ./internal/modules/incus -count=1`

Expected: PASS.

- [ ] **Step 7: Commit platform plumbing**

```bash
git add internal/platform/module.go internal/web/server.go internal/web/server_test.go internal/modules/logs/module_test.go internal/modules/services/module_test.go internal/modules/incus/module_test.go
git commit -m "feat: expose fixed streams to modules"
```

---

### Task 4: Files Models And Root Configuration

**Files:**
- Create: `internal/modules/files/model.go`
- Create: `internal/modules/files/config.go`
- Create: `internal/modules/files/config_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `RootSpec`, `Root`, `Entry`, `ListRequest`, `State`, `Download`, `Manager`, `RootFlags`, `NewSystemManager`, and domain sentinel errors.
- Consumes: `golang.org/x/sys/unix` as a direct dependency.

- [ ] **Step 1: Write failing root configuration tests**

Test read-only/read-write parsing, IDs at both length bounds, path values containing `=`, duplicate IDs across modes, relative paths, `/`, missing paths, files instead of directories, rejection of a 65th root, and zero-root manager creation.

```go
func TestRootFlagsRejectDuplicateAcrossModes(t *testing.T) {
    var roots RootFlags
    require.NoError(t, roots.Add("logs="+t.TempDir(), false))
    err := roots.Add("logs="+t.TempDir(), true)
    assert.ErrorContains(t, err, "duplicate root id")
}

func TestRootFlagsRejectFilesystemRoot(t *testing.T) {
    var roots RootFlags
    assert.ErrorContains(t, roots.Add("host=/", false), "filesystem root")
}
```

- [ ] **Step 2: Run configuration tests to verify failure**

Run: `go test ./internal/modules/files -run 'TestRoot' -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Define domain models and limits**

Create `model.go` with these public contracts:

```go
const (
    MaxRoots = 64
    MaxEntries = 2_000
    MaxScannedEntries = 10_000
    MaxPathBytes = 768
    MaxTransferBytes int64 = 256 << 20
)

type EntryType string
const (
    EntryRegular EntryType = "regular"
    EntryDirectory EntryType = "directory"
    EntrySymlink EntryType = "symlink"
    EntryOther EntryType = "other"
)

type RootSpec struct { ID, Path string; Writable bool }
type Root struct { ID, Path string; Writable bool }
type Entry struct {
    Name string
    Type EntryType
    Size int64
    Modified time.Time
    UID, GID uint32
    Owner, Group string
    Mode uint32
    LinkTarget string
}
type ListRequest struct { Root, Path, Filter, Sort, Direction string; Hidden bool }
type State struct { Roots []Root; Active Root; Path string; Entries []Entry; Truncated bool; Filters ListRequest }
type Download struct { Body io.ReadCloser; Name string; Size int64 }
type Manager interface {
    List(context.Context, ListRequest) (State, error)
    Download(context.Context, string, string) (Download, error)
    Upload(context.Context, string, string, string, io.Reader) error
    Close() error
}
```

Define `ErrInvalid`, `ErrNotFound`, `ErrReadOnly`, `ErrConflict`, `ErrTooLarge`, and `ErrUnavailable` for composition-root error mapping.

- [ ] **Step 4: Implement root flag parsing and descriptor lifecycle**

`RootFlags.Add(value string, writable bool)` splits on the first `=`, validates the ID regex and an absolute cleaned path no longer than 4 KiB, rejects `/`, stats a directory, rejects duplicates, rejects a 65th root, and preserves insertion-independent deterministic ID order. `NewSystemManager(specs []RootSpec)` opens every root with `unix.Open(path, O_PATH|O_DIRECTORY|O_CLOEXEC|O_NOFOLLOW, 0)`, closes earlier descriptors on failure, and returns a manager that closes every descriptor exactly once. `SystemManager` keeps unexported `maxTransfer`, `maxEntries`, `maxScanned`, and `maxJSONBytes` fields initialized from production constants so focused tests can use small limits without changing exported behavior.

Expose flag adapters:

```go
func (r *RootFlags) Flag(writable bool) flag.Value
func (r *RootFlags) Specs() []RootSpec
```

- [ ] **Step 5: Promote x/sys to a direct dependency and format**

Run: `go get golang.org/x/sys@v0.46.0`

Expected: `golang.org/x/sys v0.46.0` moves into a direct `require` block without changing its version.

- [ ] **Step 6: Run configuration tests**

Run: `go test ./internal/modules/files -run 'TestRoot' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit models and configuration**

```bash
git add go.mod go.sum internal/modules/files/model.go internal/modules/files/config.go internal/modules/files/config_test.go
git commit -m "feat: configure bounded files roots"
```

---

### Task 5: Descriptor-Safe Directory Listings

**Files:**
- Create: `internal/modules/files/manager_linux.go`
- Create: `internal/modules/files/manager_linux_test.go`

**Interfaces:**
- Consumes: models and opened roots from Task 4.
- Produces: `(*SystemManager).List`, `ParseListParameters`, and relative-path validation shared by transfers.

- [ ] **Step 1: Write failing path and listing tests**

Build temporary trees containing regular files, directories, hidden names, Unicode names, FIFO entries, valid and escaping symlinks, and known modes. Cover empty root listing, nested listing, all invalid path forms, symlink intermediates, missing directories, case-insensitive filter, hidden toggle, every sort/direction, directory-first order, deterministic ties, owner/group numeric fallback, disappearing entries, scan cap, return cap, and encoded-size cap.

```go
func TestListShowsButNeverTraversesSymlinks(t *testing.T) {
    root := t.TempDir()
    outside := t.TempDir()
    require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
    manager := newTestManager(t, RootSpec{ID: "safe", Path: root})

    state, err := manager.List(context.Background(), ListRequest{Root: "safe", Hidden: true})
    require.NoError(t, err)
    require.Equal(t, EntrySymlink, entryNamed(t, state, "escape").Type)
    _, err = manager.List(context.Background(), ListRequest{Root: "safe", Path: "escape"})
    assert.ErrorIs(t, err, ErrNotFound)
}
```

- [ ] **Step 2: Run manager listing tests to verify failure**

Run: `go test ./internal/modules/files -run 'Test(List|Path|ParseList)' -count=1`

Expected: FAIL because listing is not implemented.

- [ ] **Step 3: Implement broker parameter parsing**

Add:

```go
func ParseListParameters(parameters map[string]string) (ListRequest, error)
```

Require exactly `root`, `path`, `filter`, `sort`, `direction`, and `hidden`. Permit empty root only for root-summary requests. Enforce filter limits of 200 Unicode code points and 1 KiB, allow sorts `name|size|modified|owner|permissions`, directions `asc|desc`, and hidden values `true|false`. Apply `name`, `asc`, and `false` defaults only when the named value is empty.

- [ ] **Step 4: Implement descriptor-relative resolution**

Implement `validateRelativePath` and `openBeneath`. Split on `/`, enforce the global path rules, and call `unix.Openat2` from the pinned root descriptor with:

```go
unix.OpenHow{
    Flags: uint64(flags | unix.O_CLOEXEC),
    Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
}
```

Duplicate the root descriptor for an empty path. Convert `ENOENT`, `ENOTDIR`, `ELOOP`, and `EXDEV` to `ErrNotFound`; convert unsupported `openat2` to `ErrUnavailable` without a string-based fallback.

- [ ] **Step 5: Implement bounded enumeration and sorting**

Read names in fixed batches, stopping after `maxScanned+1`; never call `ReadDir(-1)` on an untrusted directory. For each visible/filter-matching name, use `unix.Fstatat(..., AT_SYMLINK_NOFOLLOW)`, resolve owner/group names with numeric fallback, and call bounded `unix.Readlinkat` only for symlinks. Sort directories first, then the selected field/direction, then name. Marshal candidate entries to maintain the `maxJSONBytes` aggregate entry budget, stop at `maxEntries`, and mark `Truncated` whenever scan, count, or byte coverage is cut short. Production values are 10,000 scanned names, 2,000 returned entries, and a 1.5 MiB aggregate entry budget.

- [ ] **Step 6: Run listing tests**

Run: `go test ./internal/modules/files -run 'Test(List|Path|ParseList)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit listing support**

```bash
git add internal/modules/files/manager_linux.go internal/modules/files/manager_linux_test.go
git commit -m "feat: list configured file roots safely"
```

---

### Task 6: Safe Download And Atomic Upload

**Files:**
- Modify: `internal/modules/files/manager_linux.go`
- Modify: `internal/modules/files/manager_linux_test.go`

**Interfaces:**
- Consumes: descriptor resolution and root models from Tasks 4-5.
- Produces: `(*SystemManager).Download` and `(*SystemManager).Upload`.

- [ ] **Step 1: Write failing download tests**

Test regular files, empty files, exact transfer boundary using sparse files, over-limit sparse files, directories, final symlinks, intermediate symlinks, missing names, cancellation, and descriptor stability after renaming the path following `Download`. Set the test manager's unexported `maxTransfer` to a small value except for one assertion that the production default is exactly 256 MiB.

```go
func TestDownloadKeepsValidatedDescriptorAfterRename(t *testing.T) {
    root := t.TempDir()
    require.NoError(t, os.WriteFile(filepath.Join(root, "item"), []byte("original"), 0o640))
    manager := newTestManager(t, RootSpec{ID: "safe", Path: root})
    download, err := manager.Download(context.Background(), "safe", "item")
    require.NoError(t, err)
    defer download.Body.Close()
    require.NoError(t, os.Rename(filepath.Join(root, "item"), filepath.Join(root, "moved")))
    data, err := io.ReadAll(download.Body)
    require.NoError(t, err)
    assert.Equal(t, "original", string(data))
}
```

- [ ] **Step 2: Run download tests to verify failure**

Run: `go test ./internal/modules/files -run 'TestDownload' -count=1`

Expected: FAIL because `Download` is not implemented.

- [ ] **Step 3: Implement descriptor-stable downloads**

Open with `O_RDONLY|O_NOFOLLOW`, call `Fstat` on that descriptor, require a regular file and size `<= m.maxTransfer`, check context before returning, and return the same `*os.File` as `Download.Body`. Map invalid path, missing/non-regular/symlink, oversized, cancellation, and unavailable errors to the domain sentinels.

- [ ] **Step 4: Write failing atomic-upload tests**

Test read-only denial, valid nested upload, empty upload, mode `0640`, root UID/GID when tests run as root, collision preservation, invalid names, directory symlink traversal, actual size `maxTransfer+1`, cancelled readers, injected write/sync/link failures, no partial directory entries during a blocked upload, and `O_TMPFILE` unsupported mapping. Use a small test `maxTransfer`. Add unexported operation hooks on `SystemManager` only where deterministic syscall failure injection is required.

```go
func TestUploadNeverPublishesPartialFile(t *testing.T) {
    root := t.TempDir()
    manager := newTestManager(t, RootSpec{ID: "write", Path: root, Writable: true})
    reader := newBlockingReader([]byte("partial"))
    done := make(chan error, 1)
    go func() { done <- manager.Upload(context.Background(), "write", "", "new.txt", reader) }()
    reader.WaitUntilRead(t)
    _, err := os.Stat(filepath.Join(root, "new.txt"))
    assert.ErrorIs(t, err, os.ErrNotExist)
    reader.Finish()
    require.NoError(t, <-done)
}
```

- [ ] **Step 5: Implement unnamed atomic uploads**

Open the destination directory with `openBeneath`. Validate `name` as one base segment. Open an unnamed file with `unix.Openat(dirFD, ".", O_TMPFILE|O_RDWR|O_CLOEXEC, 0o600)`, copy through `io.LimitReader(reader, m.maxTransfer+1)`, reject bytes above the limit, check context during copy, `Fchown(fd, 0, 0)`, `Fchmod(fd, 0o640)`, `Fsync(fd)`, and publish with:

```go
unix.Linkat(fd, "", dirFD, name, unix.AT_EMPTY_PATH)
```

Map `EEXIST` to `ErrConflict`, `EOPNOTSUPP`/`EINVAL` from `O_TMPFILE` to `ErrUnavailable`, and always close descriptors. Fsync the directory after linking. Never create a named temporary file.

- [ ] **Step 6: Run transfer manager tests**

Run: `go test ./internal/modules/files -run 'Test(Download|Upload)' -count=1`

Expected: PASS. Tests requiring root ownership assert UID/GID only when `os.Geteuid() == 0`.

- [ ] **Step 7: Commit transfer operations**

```bash
git add internal/modules/files/manager_linux.go internal/modules/files/manager_linux_test.go
git commit -m "feat: transfer configured files safely"
```

---

### Task 7: Privileged Files Composition

**Files:**
- Modify: `cmd/pilothoused/main.go`
- Modify: `cmd/pilothoused/main_test.go`

**Interfaces:**
- Consumes: Files manager from Tasks 4-6 and broker registries from Tasks 1-2.
- Produces: `registerFiles` and daemon flags `--files-root`, `--files-write-root`.

- [ ] **Step 1: Write failing registration tests**

Add a fake Files manager and assert non-admin identities cannot list, download, or upload; exact list parameters reach `ParseListParameters`; unknown parameters fail; download returns a fixed stream result; upload receives streamed bytes; read-only/conflict/too-large domain errors map to public 403/409/413 errors; and upload resource is `files/<root>/<directory>/<name>`.

```go
func TestRegisterFilesRequiresAdministrator(t *testing.T) {
    queries := broker.NewQueryRegistry()
    streamQueries := broker.NewStreamQueryRegistry()
    streamActions := broker.NewStreamActionRegistry()
    manager := &fakeFilesManager{}
    require.NoError(t, registerFiles(queries, streamQueries, streamActions, manager))

    _, err := queries.Execute(context.Background(), auth.Identity{}, broker.QueryFilesList, validFilesParameters())
    assert.Error(t, err)
    _, err = streamQueries.Execute(context.Background(), auth.Identity{}, broker.QueryFilesDownload, map[string]string{"root": "logs", "path": "a"})
    assert.Error(t, err)
}
```

- [ ] **Step 2: Run daemon tests to verify failure**

Run: `go test ./cmd/pilothoused -run 'TestRegisterFiles' -count=1`

Expected: FAIL because registration is missing.

- [ ] **Step 3: Add root flags and manager lifecycle**

Declare one `files.RootFlags`, register its read-only and writable adapters with `flag.Var`, construct `files.NewSystemManager(filesRoots.Specs())` after audit/query registries, and defer `Close`. Preserve a no-root configuration as valid.

- [ ] **Step 4: Register the three fixed operations**

Implement:

```go
func registerFiles(
    queries *broker.QueryRegistry,
    streamQueries *broker.StreamQueryRegistry,
    streamActions *broker.StreamActionRegistry,
    manager files.Manager,
) error
```

The list query is admin-only and calls `files.ParseListParameters`. The download stream query declares exact parameters `root,path`, `Limit: files.MaxTransferBytes`, and returns `broker.StreamResult{Body: download.Body, Filename: download.Name, MediaType: "application/octet-stream", Size: download.Size}`. The upload stream action declares `root,directory,name`, `Limit: files.MaxTransferBytes`, `Timeout: 15*time.Minute`, and a destination resource/lock. Map domain errors through `broker.NewPublicError` with stable status/message/category; never include `err.Error()` in public text.

- [ ] **Step 5: Run daemon registration tests**

Run: `go test ./cmd/pilothoused -run 'TestRegisterFiles' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit privileged composition**

```bash
git add cmd/pilothoused/main.go cmd/pilothoused/main_test.go
git commit -m "feat: register privileged files operations"
```

---

### Task 8: Files Views And Presentation Module

**Files:**
- Create: `internal/modules/files/module.go`
- Create: `internal/modules/files/views.templ`
- Create: `internal/modules/files/views_test.go`
- Modify: `internal/web/static/app.css`

**Interfaces:**
- Consumes: `files.State`, `files.Entry`, `platform.Module`, and shared web components.
- Produces: `New`, `Manifest`, `Dashboard`, `Page`, `AccessDenied`, and `Unavailable`.

- [ ] **Step 1: Write failing rendering tests**

Render a state with two roots, nested path, active filters, one directory, one regular file, one symlink with `<target>`, and one other entry. Assert root switcher mode labels, breadcrumbs, preserved filter/sort/hidden URLs, sortable headers, upload form only for writable roots, CSRF before file input, escaped names/targets, no symlink link, truncation copy, every empty/error state, component SVG output, and no literal `@web.` syntax.

```go
func TestPageRendersSymlinkWithoutActionableLink(t *testing.T) {
    state := filesViewState()
    html := renderedFilesComponent(t, Page(state, "csrf"))
    assert.Contains(t, html, "&lt;target&gt;")
    assert.NotContains(t, html, "<target>")
    assert.NotContains(t, html, `href="/files/safe/download?path=link`)
    assert.Contains(t, html, `name="csrf" value="csrf"`)
    assert.NotContains(t, html, "@web.")
}
```

- [ ] **Step 2: Run rendering tests to verify failure**

Run: `go test ./internal/modules/files -run 'Test(Page|AccessDenied|Unavailable)' -count=1`

Expected: FAIL because views do not exist.

- [ ] **Step 3: Implement manifest and URL helpers**

Use this manifest and no dashboard cards:

```go
func (*Module) Manifest() platform.Manifest {
    return platform.Manifest{
        ID: "files", Name: "Files", Description: "Browse and transfer configured host files",
        Icon: "disk", Order: 38, Path: "/files",
    }
}
```

Normalize browser filter to 200 runes/1 KiB, default sort/direction/hidden values, and provide URL builders using `net/url.Values`. Never concatenate an unescaped relative path into a URL.

- [ ] **Step 4: Implement templ views**

Create components for access denied, no roots, unavailable/inaccessible, root selector, breadcrumbs, toolbar, upload form, and entry table. Put every `@web.Icon(...)` invocation in its own templ node. Render regular files as download links, directories as navigation links, and symlink/other entries as plain text. Use `data-label` attributes for mobile stacked metadata and `<time datetime>` for modified times.

- [ ] **Step 5: Add focused CSS**

Add `.files-root-bar`, `.files-breadcrumbs`, `.files-toolbar`, `.files-name`, `.files-metadata`, `.files-upload`, and `.files-table` rules. At `max-width: 760px`, hide the table header, render each row as a grid/card-like block, keep Name full width, and show metadata labels from `data-label`. Do not alter existing module selectors.

- [ ] **Step 6: Generate templ output and run view tests**

Run: `make generate`

Run: `go test ./internal/modules/files -run 'Test(Page|AccessDenied|Unavailable)' -count=1`

Expected: PASS, with generated `internal/modules/files/views_templ.go` present.

- [ ] **Step 7: Commit presentation**

```bash
git add internal/modules/files/module.go internal/modules/files/views.templ internal/modules/files/views_templ.go internal/modules/files/views_test.go internal/web/static/app.css
git commit -m "feat: render configured files browser"
```

---

### Task 9: Files HTTP Handlers

**Files:**
- Create: `internal/modules/files/handler.go`
- Create: `internal/modules/files/module_test.go`

**Interfaces:**
- Consumes: platform stream methods from Task 3, fixed broker IDs, models, URL helpers, and views.
- Produces: four mounted Files routes and multipart-to-broker streaming.

- [ ] **Step 1: Write failing listing and download handler tests**

Create a `filesHost` implementing every `platform.Host` method. Test non-admin denial without any broker call, `/files` redirect to sorted first root, no-root page, exact fixed list query parameters, unavailable state without raw errors, download fixed ID/parameters, attachment/nosniff/content-length headers, body streaming, Range rejection, and mapped pre-header failures.

```go
func TestFilesPageDispatchesOnlyFixedListQuery(t *testing.T) {
    host := &filesHost{admin: true, state: filesViewState()}
    response := serveFiles(t, host, http.MethodGet, "/files/safe?path=logs&filter=err&sort=size&direction=desc&hidden=true", nil)
    assert.Equal(t, http.StatusOK, response.Code)
    assert.Equal(t, broker.QueryFilesList, host.queryID)
    assert.Equal(t, map[string]string{
        "root": "safe", "path": "logs", "filter": "err",
        "sort": "size", "direction": "desc", "hidden": "true",
    }, host.parameters)
}
```

- [ ] **Step 2: Run listing/download tests to verify failure**

Run: `go test ./internal/modules/files -run 'TestFiles(Page|Download)' -count=1`

Expected: FAIL because routes are not mounted.

- [ ] **Step 3: Implement listing and download routes**

Mount the four routes from the design. Use an 8-second context for list queries and a 15-minute context for transfers. `/files` requests root summaries with all six exact query keys and redirects by root ID. The root page normalizes browser filters then queries the broker. Download rejects `Range`, calls only `QueryFilesDownload`, validates non-negative `Size <= MaxTransferBytes`, sets attachment headers with `mime.FormatMediaType`, writes status 200, and copies exactly `Size`; close the broker body in all paths. Before headers are written, map broker failures with `broker.StatusCode(err)` and stable local response text.

- [ ] **Step 4: Write failing multipart upload tests**

Build multipart bodies manually to control order. Test missing/wrong CSRF before broker calls, foreign origin delegated to `ValidateActionToken`, file before CSRF, duplicate file, field after file, unexpected field, empty/path/control filenames, 256 MiB+1 streamed bytes, read-only public error, collision notice, broker cancellation, successful raw bytes and exact metadata, 303 redirect, and HTMX `HX-Redirect`/204.

```go
func TestUploadStreamsOneFinalPartWithExactMetadata(t *testing.T) {
    body, contentType := multipartUpload(t, []multipartPart{
        {Field: "csrf", Value: "csrf"},
        {Field: "file", Filename: "report.txt", Value: "payload"},
    })
    host := &filesHost{admin: true, csrf: "csrf"}
    response := serveFilesWithContentType(t, host, http.MethodPost, "/files/write/upload?path=reports", body, contentType)
    assert.Equal(t, http.StatusSeeOther, response.Code)
    assert.Equal(t, broker.ActionFilesUpload, host.streamActionID)
    assert.Equal(t, map[string]string{"root": "write", "directory": "reports", "name": "report.txt"}, host.streamParameters)
    assert.Equal(t, "payload", host.streamBody.String())
}
```

- [ ] **Step 5: Implement sequential multipart streaming**

Wrap the browser body with `http.MaxBytesReader(w, r.Body, MaxTransferBytes+(1<<20))`. Require `multipart/form-data`. Read a `csrf` field first with a 4 KiB field cap, call `host.ValidateActionToken`, then require one `file` part and validate its filename. Create an `io.Pipe`; in one goroutine call `host.StreamAction` with the pipe reader. In the handler goroutine copy at most `MaxTransferBytes+1` bytes from the file part, reject excess bytes, call `NextPart` and require `io.EOF`, then close the pipe writer. On parser/copy failure use `CloseWithError` and wait for the broker goroutine before responding. Never call `ParseMultipartForm` or create a local temporary file.

- [ ] **Step 6: Implement stable redirects and error mapping**

Use `url.Values` to preserve the active directory. Select notices from `broker.StatusCode(err)`. Success notice: `Uploaded <name>`. Conflict notice: `A file with that name already exists.` Read-only notice: `Uploads are disabled for this root.` Other failures: `Upload failed. Review Activity for the recorded outcome.` Never render the broker's raw error. Use 303 for normal forms and `HX-Redirect` plus 204 for HTMX.

- [ ] **Step 7: Run all Files handler tests**

Run: `go test ./internal/modules/files -run 'TestFiles|TestUpload' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit handlers**

```bash
git add internal/modules/files/handler.go internal/modules/files/module_test.go
git commit -m "feat: handle files browsing and transfers"
```

---

### Task 10: Application Composition And Documentation

**Files:**
- Modify: `cmd/pilothouse/main.go`
- Modify: `README.md`
- Modify: `docs/modules.md`

**Interfaces:**
- Consumes: complete Files module and privileged registrations.
- Produces: visible navigation module and operator configuration documentation.

- [ ] **Step 1: Register the web module**

Import `internal/modules/files` in `cmd/pilothouse/main.go` and add `files.New()` immediately after `logs.New()` in `platform.NewRegistry`. Do not pass root paths to the web process.

- [ ] **Step 2: Document the feature and root flags**

Add a README feature bullet for administrator-only configured-root browsing, download, and atomic upload. Add an example:

```bash
sudo ./bin/pilothoused \
  --files-root logs=/var/log \
  --files-write-root imports=/var/lib/pilothouse/imports
```

State that `/` is rejected, symlinks are displayed but never followed, uploads are root-owned mode `0640`, existing names are not overwritten, and each transfer is limited to 256 MiB. In `docs/modules.md`, document fixed stream query/action registration and explicitly prohibit generic stream proxies.

- [ ] **Step 3: Run generation and focused tests**

Run: `make generate`

Run: `go test ./internal/broker ./internal/web ./internal/modules/files ./cmd/pilothoused -count=1`

Expected: PASS.

- [ ] **Step 4: Run the complete required verification**

Run: `make build`

Expected: both `bin/pilothouse` and `bin/pilothoused` build successfully.

Run: `make test`

Expected: all packages PASS.

Run: `make fmt`

Expected: command succeeds; inspect formatting changes and retain only intended files.

Run: `make lint`

Expected: no lint findings. If native PAM/systemd dependencies are unavailable, run `make docker-build`, `make docker-test`, `make docker-fmt`, and `make docker-lint` instead.

- [ ] **Step 5: Inspect the final diff and generated files**

Run: `git status --short`

Run: `git diff --check`

Run: `git diff --stat`

Expected: only Files feature, broker stream infrastructure, required interface updates, generated templ output, CSS, and documentation are changed; no whitespace errors or hand-edited generated files.

- [ ] **Step 6: Commit composition and documentation**

```bash
git add cmd/pilothouse/main.go README.md docs/modules.md internal/modules/files/views_templ.go
git commit -m "feat: enable configured files management"
```
