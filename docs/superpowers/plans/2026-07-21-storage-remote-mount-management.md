# Storage Remote Mount Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let administrators safely create, activate, deactivate, and delete Pilothouse-owned NFS and guest or credentialed SMB automount definitions while preserving the fixed broker boundary.

**Architecture:** Fixed broker actions validate exact form shapes and allocate an opaque definition ID during a non-mutating preflight so audit records use `storage/mount/<id>`. A privileged remote-mount manager validates protocol and target safety, atomically persists root-owned manifests/credentials/generated systemd units, and controls only verified Pilothouse-owned definitions through a narrow systemd interface.

**Tech Stack:** Go 1.26.3, standard-library crypto/JSON/filesystem APIs, `golang.org/x/sys/unix` openat2 path safety, go-systemd D-Bus/unit escaping, templ, HTMX, existing broker audit/action framework, testify.

## Global Constraints

- This plan depends on both prior Storage plans being fully implemented and verified.
- Use exactly the seven broker IDs and parameter lists in `docs/superpowers/specs/2026-07-21-storage-module-design.md`.
- Every mutation is administrator-only, CSRF-protected, timeout-bounded, and audited without source, target, username, password, or options.
- Create actions do not require confirmation; unmount and delete require exact confirmation for `storage/mount/<opaque-id>`.
- Accept NFS versions `auto`, `3`, `4`, `4.1`, `4.2`; accept SMB versions `auto`, `2.1`, `3.0`, `3.1.1`; accept read-only only as `true` or `false`.
- Generated mount options always include `nosuid,nodev`; do not accept a free-form option string.
- Reject target `/`, symlinks, non-directories, non-empty existing targets, active mounts nested below the target, any ancestor/descendant overlap with another managed target, unit conflicts, and protected trees `/proc`, `/sys`, `/dev`, `/run`, `/boot`, `/etc`, `/usr`, `/var/lib/pilothouse`, and descendants.
- Write manifests/credentials as root `0600` and units as root `0644`, atomically and durably.
- Never edit `/etc/fstab`, force/lazy unmount, alter unmanaged units, return stored passwords, or display privileged errors.
- Never send credentialed-create actions through the generic confirmation page, because it preserves submitted form fields.
- Run `make generate`, build, tests, formatting, and lint before handoff.

## File Structure

- Modify `internal/broker/actions.go` and tests: add non-mutating action preflight before resource/audit resolution.
- Modify `internal/broker/api.go`: add six Storage action constants.
- Create `internal/modules/storage/remote.go`: create request types, protocol grammar, booleans/versions, opaque IDs, and manager interface.
- Create `internal/modules/storage/paths.go`: openat2-based target validation and safe directory lifecycle.
- Create `internal/modules/storage/manifest.go`: versioned manifest, strict load/validation, ownership marker, atomic file store.
- Create `internal/modules/storage/units.go`: deterministic mount/automount/credential rendering and ownership verification.
- Create `internal/modules/storage/remote_manager.go`: transactional create/mount/unmount/delete lifecycle.
- Create corresponding `*_test.go` files using fake filesystem/systemd boundaries and `t.TempDir()`.
- Modify `internal/modules/storage/manager.go`: merge managed definitions into snapshots without secrets.
- Modify `cmd/pilothoused/main.go` and tests: construct the remote manager and register fixed actions.
- Modify `internal/modules/storage/module.go` and tests: forms, admin checks, actions, confirmation, and redirects.
- Modify `internal/modules/storage/views.templ` and tests: add form and admin-only management controls.

---

### Task 1: Broker Action Preflight

**Files:**
- Modify: `internal/broker/actions.go`
- Modify: `internal/broker/action_safety_test.go`

**Interfaces:**
- Produces: optional `ActionDefinition.Prepare` used only to derive trusted internal parameters before resource resolution; preserves every existing action unchanged.

- [ ] **Step 1: Write failing preflight safety tests**

```go
func TestActionPrepareRunsBeforeAuditResourceAndHandler(t *testing.T) {
	store := &memoryAudit{}
	registry := NewActionRegistry(store)
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.create", Admin: true, Parameters: []string{"value"},
		Prepare: func(_ context.Context, _ auth.Identity, parameters map[string]string) (map[string]string, error) {
			prepared := cloneParameters(parameters)
			prepared["_id"] = "trusted-id"
			return prepared, nil
		},
		Resource: func(parameters map[string]string) (string, error) { return "thing/" + parameters["_id"], nil },
		Handler: func(_ context.Context, _ auth.Identity, parameters map[string]string) error {
			assert.Equal(t, "trusted-id", parameters["_id"])
			return nil
		},
	}))
	require.NoError(t, registry.Execute(context.Background(), auth.Identity{Admin: true}, "test.create", map[string]string{"value": "public"}, ""))
	assert.Equal(t, "thing/trusted-id", store.records[0].Resource)
}
```

Also assert: external `_id` is rejected by exact parameter validation; `Prepare` error creates no audit record and calls no handler; mutation of the returned map cannot alter the caller's map; prepared CR/LF/NUL or oversized values are rejected; existing actions without `Prepare` behave identically.

- [ ] **Step 2: Run broker safety tests and verify failure**

Run: `go test ./internal/broker -run 'TestActionPrepare|TestActionRejects|TestActionsSerialize'`

Expected: FAIL because `ActionDefinition.Prepare` does not exist.

- [ ] **Step 3: Implement preflight without changing existing semantics**

Add:

```go
type ActionPrepare func(context.Context, auth.Identity, map[string]string) (map[string]string, error)

type ActionDefinition struct {
	// existing fields remain unchanged
	Prepare ActionPrepare
}
```

In `Execute`, run existing `validateParameters` first, clone the external map, call `Prepare` when non-nil, then validate the result. `validatePreparedParameters` must require every original external key/value to remain present, allow no new key except `_id`, and enforce non-empty keys, non-empty values, 512-byte values, and no CR/LF/NUL. Resolve resource/lock, audit, and invoke the handler with the prepared clone. `Prepare` must run before any resource lock or audit and must not itself mutate persistent state.

- [ ] **Step 4: Run the full broker package tests**

Run: `go test -race ./internal/broker`

Expected: PASS with no changed existing action behavior.

- [ ] **Step 5: Commit broker support**

```bash
git add internal/broker/actions.go internal/broker/action_safety_test.go
git commit -m "feat: add broker action preflight"
```

### Task 2: Remote Definition Contract And Validation

**Files:**
- Modify: `internal/broker/api.go`
- Create: `internal/modules/storage/remote.go`
- Create: `internal/modules/storage/remote_test.go`

**Interfaces:**
- Produces: action constants, `RemoteManager`, `CreateRequest`, `Definition`, `NewDefinitionID`, and validation functions.

- [ ] **Step 1: Write exact broker-ID and validation tests**

Assert all constants exactly:

```go
assert.Equal(t, "org.frostyard.pilothouse.storage.create-nfs", broker.ActionStorageCreateNFS)
assert.Equal(t, "org.frostyard.pilothouse.storage.create-smb-guest", broker.ActionStorageCreateSMBGuest)
assert.Equal(t, "org.frostyard.pilothouse.storage.create-smb-credentials", broker.ActionStorageCreateSMBCredentials)
assert.Equal(t, "org.frostyard.pilothouse.storage.mount", broker.ActionStorageMount)
assert.Equal(t, "org.frostyard.pilothouse.storage.unmount", broker.ActionStorageUnmount)
assert.Equal(t, "org.frostyard.pilothouse.storage.delete", broker.ActionStorageDelete)
```

Use table tests for valid/invalid DNS names, IPv4/IPv6 literals, NFS absolute exports, SMB share names, exact versions, exact booleans, opaque IDs, usernames, and target lexical form. Include traversal, control characters, newline, empty, and 513-byte cases.

- [ ] **Step 2: Run validation tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(RemoteBrokerIDs|Validate|DefinitionID)'`

Expected: FAIL because constants and validators do not exist.

- [ ] **Step 3: Add constants and exact domain interfaces**

```go
type RemoteManager interface {
	Manager
	Create(context.Context, CreateRequest) error
	Delete(context.Context, string) error
	Mount(context.Context, string) error
	Unmount(context.Context, string) error
}

type CreateRequest struct {
	Export   string
	Host     string
	ID       string
	Password string
	Protocol string
	ReadOnly bool
	Server   string
	Share    string
	Target   string
	Username string
	Version  string
}

type Definition struct {
	CreatedTarget  bool   `json:"created_target"`
	Credential     string `json:"credential,omitempty"`
	Export         string `json:"export,omitempty"`
	FormatVersion  int    `json:"format_version"`
	Host           string `json:"host,omitempty"`
	ID             string `json:"id"`
	Protocol       string `json:"protocol"`
	ProtocolVersion string `json:"protocol_version"`
	ReadOnly       bool   `json:"read_only"`
	Server         string `json:"server,omitempty"`
	Share          string `json:"share,omitempty"`
	State          string `json:"state"`
	Target         string `json:"target"`
	UnitName       string `json:"unit_name"`
	Username       string `json:"username,omitempty"`
}
```

Use 128 random bits encoded as 32 lowercase hex characters for IDs. `NewDefinitionID(io.Reader)` must require exactly 16 random bytes. Define manifest format version `1` separately from protocol version.

- [ ] **Step 4: Implement strict protocol validation**

NFS host is DNS/IP only, export is absolute and cannot contain comma/control/NUL, SMB share excludes slash/backslash/control/NUL, target is clean absolute lexical form, username is 1-256 bytes without control/NUL/newline, password is 1-512 bytes without CR/LF/NUL. Do not log or interpolate rejected values into errors.

- [ ] **Step 5: Run and pass validation tests**

Run: `go test ./internal/modules/storage -run 'Test(RemoteBrokerIDs|Validate|DefinitionID)'`

Expected: PASS.

- [ ] **Step 6: Commit the remote contract**

```bash
git add internal/broker/api.go internal/modules/storage/remote.go internal/modules/storage/remote_test.go
git commit -m "feat: define remote storage actions"
```

### Task 3: Target Path Safety

**Files:**
- Create: `internal/modules/storage/paths.go`
- Create: `internal/modules/storage/paths_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `PathManager.ValidateTarget`, `CreateTarget`, and `RemoveTarget` with no symlink-following race.

- [ ] **Step 1: Write path-policy tests**

Using an injected `pathFS` fake and Linux integration tests under `t.TempDir`, cover: `/`; relative path; every protected root and descendant; symlink in each component position; existing file; existing non-empty directory; existing empty directory; missing leaf under safe ancestor; target with nested mount; target already mounted; conflicting generated unit; creation mode/ownership; removal only when manager-created and empty.

```go
func TestValidateTargetRejectsProtectedTrees(t *testing.T) {
	for _, target := range []string{"/", "/proc", "/proc/x", "/sys/x", "/dev/x", "/run/x", "/boot/x", "/etc/x", "/usr/x", "/var/lib/pilothouse/x"} {
		t.Run(target, func(t *testing.T) {
			assert.Error(t, validator.ValidateTarget(context.Background(), target, nil))
		})
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(ValidateTarget|CreateTarget|RemoveTarget)'`

Expected: FAIL because `PathManager` does not exist.

- [ ] **Step 3: Implement openat2 traversal**

Import `golang.org/x/sys/unix` directly. Walk from an `O_PATH|O_DIRECTORY` fd for `/` using `unix.Openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS`, opening one component at a time. Inspect directories with `Fstat`; never validate by `EvalSymlinks` followed by a separate path-based mutation. Create only the final missing component with `Mkdirat` mode `0755`, then reopen/verify it through openat2.

- [ ] **Step 4: Detect mount and unit conflicts from trusted inventory**

Pass the current fixed-query mount snapshot and generated-unit inventory into validation. Reject target equality, any active mount below `target + "/"`, and a pre-existing `<escaped>.mount` or `.automount` not owned by the same definition.

- [ ] **Step 5: Run path tests including race detection**

Run: `go test -race ./internal/modules/storage -run 'Test(ValidateTarget|CreateTarget|RemoveTarget)'`

Expected: PASS.

- [ ] **Step 6: Commit path safety**

```bash
git add internal/modules/storage/paths.go internal/modules/storage/paths_test.go go.mod go.sum
git commit -m "feat: validate remote mount targets"
```

### Task 4: Manifests, Credentials, And Unit Rendering

**Files:**
- Create: `internal/modules/storage/manifest.go`
- Create: `internal/modules/storage/manifest_test.go`
- Create: `internal/modules/storage/units.go`
- Create: `internal/modules/storage/units_test.go`

**Interfaces:**
- Produces: `ArtifactStore`, strict `LoadDefinition`, `RenderMountUnit`, `RenderAutomountUnit`, `RenderCredentials`, and `VerifyOwnedArtifacts`.

Use exact roots: manifests `/var/lib/pilothouse/storage/mounts/<id>.json`, credentials `/etc/pilothouse/storage/credentials/<id>`, and units `/etc/systemd/system/<escaped-target>.mount` plus `.automount`. Tests inject temporary equivalents of all three roots.

- [ ] **Step 1: Write manifest/atomic-write tests**

Test deterministic JSON, unknown-field rejection, format-version rejection, invalid ID/path/unit rejection after load, exact `0600` mode, root ownership through injected identity checks, same-directory temporary file, file and directory fsync, rename failure cleanup, and refusal to replace an existing unmanaged file.

- [ ] **Step 2: Write exact unit rendering tests**

For an NFS definition with ID `0123456789abcdef0123456789abcdef`, source `nas.example:/exports/media`, and target `/mnt/media`, assert deterministic complete files with:

```ini
# Managed by Pilothouse; definition=0123456789abcdef0123456789abcdef
[Unit]
Description=Pilothouse remote storage 0123456789abcdef0123456789abcdef
Wants=network-online.target
After=network-online.target
[Mount]
What=nas.example:/exports/media
Where=/mnt/media
Type=nfs
Options=nfsvers=4.2,nodev,nosuid,rw
TimeoutSec=30
```

And:

```ini
# Managed by Pilothouse; definition=0123456789abcdef0123456789abcdef
[Unit]
Description=Pilothouse automount 0123456789abcdef0123456789abcdef
[Automount]
Where=/mnt/media
TimeoutIdleSec=300
[Install]
WantedBy=multi-user.target
```

Assert unit mode `0644`, manifest/credential mode `0600`, `%` escaped as `%%`, whitespace/backslash escaped as systemd hex escapes, options sorted deterministically, and no password in units/manifests/snapshots/errors.

- [ ] **Step 3: Run artifact tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(Manifest|Atomic|Render|VerifyOwned)'`

Expected: FAIL because artifact storage/rendering does not exist.

- [ ] **Step 4: Implement durable atomic files**

Create temp files in the destination directory with `O_CREATE|O_EXCL`, write all bytes, `Sync`, `Chmod`, verify/chown root, close, rename without replacing an unmanaged destination, then fsync the directory. On error remove only the temp file. Strict manifest decode uses `json.Decoder.DisallowUnknownFields` and verifies all derived paths/unit names rather than trusting serialized values.

- [ ] **Step 5: Implement deterministic ownership verification**

Use `github.com/coreos/go-systemd/v22/unit.UnitNamePathEscape` for the target-derived basename. Verify exact marker ID, expected path, root owner, exact mode, and deterministic regenerated content before mutation. A mismatch returns a stable ownership error and never rewrites the file.

- [ ] **Step 6: Run and pass artifact tests**

Run: `go test ./internal/modules/storage -run 'Test(Manifest|Atomic|Render|VerifyOwned)'`

Expected: PASS.

- [ ] **Step 7: Commit artifacts**

```bash
git add internal/modules/storage/manifest.go internal/modules/storage/manifest_test.go internal/modules/storage/units.go internal/modules/storage/units_test.go
git commit -m "feat: generate managed mount artifacts"
```

### Task 5: Transactional Remote Manager

**Files:**
- Create: `internal/modules/storage/remote_manager.go`
- Create: `internal/modules/storage/remote_manager_test.go`
- Modify: `internal/modules/storage/manager.go`
- Modify: `internal/modules/storage/manager_test.go`

**Interfaces:**
- Consumes: domain validation, `PathManager`, `ArtifactStore`, and current mount snapshot.
- Produces: a `RemoteManager` implementation and secret-free managed definitions in `Snapshot`.

- [ ] **Step 1: Define and fake the systemd boundary**

```go
type UnitController interface {
	DaemonReload(context.Context) error
	Disable(context.Context, string) error
	Enable(context.Context, string) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
}
```

Write a recording fake and table tests that inject failure at each create step: target creation, credentials, mount unit, automount unit, manifest, reload, enable, and start. Assert reverse-order rollback removes only newly created artifacts and preserves a manifest with `State: "needs-attention"` if cleanup fails.

- [ ] **Step 2: Write mount/unmount/delete lifecycle tests**

Cover valid ID lookup, malformed/missing ID, modified artifact refusal, normal mount, confirmed external action handled later, busy unmount propagation, no force/lazy path, disable/delete order, credentials removed only after deactivation, target removed only when created and empty, daemon reload, and recoverable partial cleanup.

- [ ] **Step 3: Run manager tests and verify failure**

Run: `go test ./internal/modules/storage -run 'TestRemoteManager'`

Expected: FAIL because `SystemRemoteManager` does not exist.

- [ ] **Step 4: Implement create transaction**

Use this order: validate request; obtain fresh mount/unit/manifest inventory; validate target; render all bytes in memory; create target if needed; write credentials if needed; write mount unit; write automount unit; write manifest; daemon reload; enable automount; start automount. Track an undo closure for every completed step and execute in reverse on failure. Drop the request and credential-rendering buffers after the write returns; never copy the password into `Definition`, errors, logs, audit resources, units, or snapshots.

- [ ] **Step 5: Implement existing-definition operations**

 Every operation validates ID, strictly reloads the manifest, regenerates and verifies artifacts, then calls the exact unit operation. Unmount calls only `Stop(<unit>.mount)`. Delete stops mount and automount, disables automount, removes verified units/credential, reloads systemd, removes a manager-created empty target, and removes the manifest last so the definition remains available for recovery until all prior cleanup succeeds.

- [ ] **Step 6: Merge managed definitions into snapshots**

Represent source, target, protocol, username, activation state, and `Managed: true`; never include password or credential path. An incomplete/needs-attention manifest produces warning health and a finding. Existing unmanaged NFS/SMB mounts remain read-only inventory.

- [ ] **Step 7: Run manager tests with race detection**

Run: `go test -race ./internal/modules/storage -run 'Test(RemoteManager|ManagedDefinition)'`

Expected: PASS.

- [ ] **Step 8: Commit remote lifecycle**

```bash
git add internal/modules/storage/remote_manager.go internal/modules/storage/remote_manager_test.go internal/modules/storage/manager.go internal/modules/storage/manager_test.go
git commit -m "feat: manage remote mount definitions"
```

### Task 6: Fixed Action Registration And Audit Locks

**Files:**
- Modify: `cmd/pilothoused/main.go`
- Modify: `cmd/pilothoused/main_test.go`

**Interfaces:**
- Consumes: `RemoteManager`, broker preflight, and six fixed action constants.
- Produces: admin-only create/mount/unmount/delete registrations with safe audit resources.

- [ ] **Step 1: Write registration tests for every action**

For each create variant assert exact parameters, non-admin denial, request parsing into `CreateRequest`, and no source/target/username/password in audit resource. Assert create preflight adds a valid `_id`, audit resource is `storage/mount/<id>`, and create actions use global lock `storage/mounts`.

For mount/unmount/delete assert only `id`, resource and lock `storage/mount/<id>`, admin-only, confirmation false/true/true respectively, and malformed IDs rejected before manager dispatch.

- [ ] **Step 2: Run registration tests and verify failure**

Run: `go test ./cmd/pilothoused -run 'TestRegisterStorage(Action|Create|Mount|Unmount|Delete)'`

Expected: FAIL because action registrations do not exist.

- [ ] **Step 3: Register fixed create actions**

Each `Prepare` clones parameters, allocates `_id` through `storage.NewDefinitionID(rand.Reader)`, and returns it. Each resource is `storage/mount/` plus validated `_id`; each create lock is `storage/mounts`. Parse explicit booleans and versions in the handler and call `manager.Create` under a two-minute request timeout. Never include submitted values in returned errors.

- [ ] **Step 4: Register lifecycle actions**

Use per-ID resource/lock and two-minute timeout. Set `ConfirmationRequired: true` only for unmount and delete. Keep actions synchronous so the web redirect reflects the audited outcome.

- [ ] **Step 5: Run all daemon registration tests**

Run: `go test ./cmd/pilothoused -run 'TestRegisterStorage'`

Expected: PASS.

- [ ] **Step 6: Commit broker wiring**

```bash
git add cmd/pilothoused/main.go cmd/pilothoused/main_test.go
git commit -m "feat: register remote mount actions"
```

### Task 7: Administrator Web Workflows

**Files:**
- Modify: `internal/modules/storage/module.go`
- Modify: `internal/modules/storage/module_test.go`
- Modify: `internal/modules/storage/views.templ`
- Modify: `internal/modules/storage/views_test.go`
- Modify: `internal/web/static/app.css`

**Interfaces:**
- Consumes: fixed actions and existing `platform.Host` CSRF/confirmation/redirect behavior.
- Produces: add form and managed mount controls, usable with and without HTMX.

- [ ] **Step 1: Write handler tests for exact actions and access control**

Cover GET `/storage/mounts/new`; three POST create routes or one protocol-dispatch route; POST `/storage/mounts/{id}/mount`; `/unmount`; `/delete`. Assert non-admin requests make no broker call, all POSTs call `ValidateAction`, create never calls `ConfirmAction`, unmount/delete do, and exact parameter maps include explicit `read_only` and `version`.

Assert credentialed SMB password reaches only `ActionStorageCreateSMBCredentials`; guest and NFS maps contain no password key. Assert raw broker errors never appear in response/redirect.

- [ ] **Step 2: Write redirect and confirmation tests**

Assert normal success/failure redirects are `303`; HTMX responses are `204` with `HX-Redirect`; unmount/delete confirmation resource is exactly `storage/mount/<id>`; credentialed create never renders a confirmation page or reflects password.

- [ ] **Step 3: Write form/control rendering tests**

Assert NFS and SMB fields, guest/credentials choice, exact version options, read-only control, CSRF hidden field, no free-form options input, no credential-path/unit/executable fields, admin-only Add/Mount/Unmount/Delete controls, escaped values, and no literal `@web.` syntax.

- [ ] **Step 4: Run web tests and verify failure**

Run: `go test ./internal/modules/storage -run 'Test(StorageAction|RemoteMountForm|RenderRemote|Redirect)'`

Expected: FAIL because routes and views do not exist.

- [ ] **Step 5: Implement routes and stable redirects**

Validate role before rendering controls or accepting actions, then `ValidateAction`, lexical input normalization, confirmation when required, and `host.Execute`. On failure set only `kind=error&notice=Action+failed.+Review+Activity+for+the+recorded+outcome.`; on success use a fixed action-specific notice. Preserve no password in query strings or logs.

- [ ] **Step 6: Implement forms and controls**

Use normal POST forms with hidden CSRF. Render protocol sections server-side; a protocol query parameter may select NFS, SMB guest, or SMB credentials without JavaScript. HTMX enhancement is limited to normal navigation/redirect behavior.

- [ ] **Step 7: Generate and run web tests**

Run: `make generate && go test ./internal/modules/storage -run 'Test(StorageAction|RemoteMountForm|RenderRemote|Redirect)'`

Expected: PASS.

- [ ] **Step 8: Commit web management**

```bash
git add internal/modules/storage/module.go internal/modules/storage/module_test.go internal/modules/storage/views.templ internal/modules/storage/views_test.go internal/web/static/app.css
git commit -m "feat: add remote mount workflows"
```

### Task 8: Security And Full Verification

**Files:**
- Modify: `docs/modules.md`
- Test: all changed broker, daemon, Storage, and web files.

- [ ] **Step 1: Add explicit security regression tests**

Search rendered HTML, broker errors, audit records, manifests, units, and snapshots for an injected sentinel password; every location except the root-only credential file must not contain it. Add one end-to-end fake action test proving audit resource contains only the opaque ID.

- [ ] **Step 2: Document remote mount ownership and limits**

Add exact action IDs, managed artifact locations, admin requirement, allowed protocol versions, no-free-form-options rule, and the rule that unmanaged mounts remain read-only.

- [ ] **Step 3: Run generation and formatting**

Run: `make generate && make fmt`

Expected: both commands exit 0.

- [ ] **Step 4: Run race-enabled focused tests**

Run: `go test -race ./internal/broker ./internal/modules/storage ./cmd/pilothouse ./cmd/pilothoused ./internal/web`

Expected: PASS with no race reports and no secret sentinel in failures/output.

- [ ] **Step 5: Run required project verification**

Run: `make build && make test && make lint`

Expected: all commands exit 0. If native dependencies are unavailable, use and pass `make docker-build`, `make docker-test`, and `make docker-lint`.

- [ ] **Step 6: Inspect final changes**

Run: `git status --short && git diff --check && git diff --stat`

Expected: only intended broker preflight, Storage management, tests, docs, and dependency metadata appear; `.superpowers/` and generated templ Go files are not staged.

- [ ] **Step 7: Commit docs and verification fixes**

```bash
git add docs/modules.md
git commit -m "docs: describe managed remote mounts"
```
