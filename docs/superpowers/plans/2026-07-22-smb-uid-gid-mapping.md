# SMB UID/GID Mapping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow administrators to create Pilothouse-managed SMB mounts whose files are presented with an optional paired numeric local UID and GID.

**Architecture:** Preserve the two existing SMB create actions for unmapped mounts and add two fixed owned variants whose exact schemas require `uid` and `gid`. Parse and normalize numeric ownership into comparable string fields, persist new definitions as manifest version 2 while continuing to verify version 1 artifacts byte-for-byte, and generate only fixed sorted CIFS options.

**Tech Stack:** Go 1.26.5, standard-library numeric/JSON validation, systemd mount units, templ, HTMX, existing broker action framework, testify.

## Global Constraints

- UID and GID are optional as a pair; reject a request that supplies only one.
- Accept unsigned base-10 digits representing 0 through 4294967294, normalize leading zeroes, and reject signs, whitespace, alternate bases, overflow, invalid UTF-8, and 4294967295.
- Apply ownership mapping only to guest and credentialed SMB definitions; NFS must reject it.
- Keep `org.frostyard.pilothouse.storage.create-smb-guest` and `org.frostyard.pilothouse.storage.create-smb-credentials` unchanged.
- Add only `org.frostyard.pilothouse.storage.create-smb-guest-owned` and `org.frostyard.pilothouse.storage.create-smb-credentials-owned`; do not add generic optional broker parameters.
- New manifests use format version 2. Existing format version 1 manifests must remain loadable and exactly verifiable without migration or rewriting.
- Never accept free-form mount options or user/group names, and never expose passwords or privileged error details.
- Mutations remain administrator-only, CSRF-protected, synchronously audited against `storage/mount/<opaque-id>`, globally serialized for creation, and bounded by the existing two-minute timeout.
- Run `make generate` after editing `*.templ`; never hand-edit generated `*_templ.go` files.
- Review and update `README.md`, relevant `docs/` documents, and `yeti/OVERVIEW.md` with the source change.

## File Structure

- Modify `internal/modules/storage/remote.go` and `remote_test.go`: ownership value, strict parsing/normalization, manifest version constants, and request/definition fields.
- Modify `internal/modules/storage/manifest.go` and `manifest_test.go`: strict version 1/version 2 compatibility and paired ownership validation.
- Modify `internal/modules/storage/units.go` and `units_test.go`: deterministic fixed `uid=` and `gid=` CIFS options.
- Modify `internal/modules/storage/remote_manager.go` and `remote_manager_test.go`: normalize requests into persisted definitions and preserve ownership through lifecycle verification.
- Modify `internal/broker/api.go`, `cmd/pilothoused/main.go`, and their tests: two exact owned action IDs and registrations.
- Modify `internal/modules/storage/module.go`, `module_test.go`, `views.templ`, and `views_test.go`: paired form handling, fixed-action selection, and SMB-only numeric fields.
- Regenerate `internal/modules/storage/views_templ.go` with `make generate`; do not edit it manually.
- Modify `README.md`, `docs/modules.md`, `docs/superpowers/specs/2026-07-21-storage-module-design.md`, and `yeti/OVERVIEW.md`: document the supported mapping and fixed actions.

---

### Task 1: Ownership Contract And Manifest Compatibility

**Files:**
- Modify: `internal/modules/storage/remote.go`
- Modify: `internal/modules/storage/remote_test.go`
- Modify: `internal/modules/storage/manifest.go`
- Modify: `internal/modules/storage/manifest_test.go`
- Modify: `internal/modules/storage/units.go`

**Interfaces:**
- Produces: `LegacyManifestFormatVersion`, `ManifestFormatVersion`, `SMBOwnership`, `ParseSMBOwnership(string, string) (SMBOwnership, error)`, and flattened `UID`/`GID` JSON fields on `Definition`.
- Consumes: existing UTF-8 validation and stable `errInvalidManifest` behavior.

- [ ] **Step 1: Write failing ownership parser tests**

Add table tests to `remote_test.go` that assert normalization, pair enforcement, and safe stable errors:

```go
func TestParseSMBOwnership(t *testing.T) {
	for _, test := range []struct {
		name    string
		uid     string
		gid     string
		want    SMBOwnership
		valid   bool
	}{
		{"absent", "", "", SMBOwnership{}, true},
		{"zero", "0", "0", SMBOwnership{UID: "0", GID: "0"}, true},
		{"maximum", "4294967294", "4294967294", SMBOwnership{UID: "4294967294", GID: "4294967294"}, true},
		{"normalizes leading zeroes", "001000", "000100", SMBOwnership{UID: "1000", GID: "100"}, true},
		{"uid only", "1000", "", SMBOwnership{}, false},
		{"gid only", "", "1000", SMBOwnership{}, false},
		{"sentinel", "4294967295", "1000", SMBOwnership{}, false},
		{"overflow", "4294967296", "1000", SMBOwnership{}, false},
		{"negative", "-1", "1000", SMBOwnership{}, false},
		{"positive sign", "+1", "1000", SMBOwnership{}, false},
		{"hex", "0x10", "1000", SMBOwnership{}, false},
		{"internal whitespace", "10 00", "1000", SMBOwnership{}, false},
		{"invalid utf8", string([]byte{0xff}), "1000", SMBOwnership{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseSMBOwnership(test.uid, test.gid)
			assertValidation(t, err, test.valid, test.uid)
			if test.valid {
				assert.Equal(t, test.want, got)
			}
		})
	}
}
```

Extend `TestRemoteTextValidatorsRejectInvalidUTF8` with a `ParseSMBOwnership` case.

- [ ] **Step 2: Run the parser test and verify it fails**

Run: `go test ./internal/modules/storage -run 'Test(ParseSMBOwnership|RemoteTextValidators)'`

Expected: FAIL because `SMBOwnership` and `ParseSMBOwnership` do not exist.

- [ ] **Step 3: Implement strict ownership parsing and comparable fields**

In `remote.go`, import `strconv`, keep version 1 as the legacy value, and make version 2 current:

```go
const (
	LegacyManifestFormatVersion = 1
	ManifestFormatVersion       = 2
)

var errInvalidSMBOwnership = errors.New("invalid SMB ownership")

type SMBOwnership struct {
	UID string `json:"uid,omitempty"`
	GID string `json:"gid,omitempty"`
}

func ParseSMBOwnership(uid, gid string) (SMBOwnership, error) {
	if uid == "" && gid == "" {
		return SMBOwnership{}, nil
	}
	if uid == "" || gid == "" {
		return SMBOwnership{}, errInvalidSMBOwnership
	}
	parse := func(value string) (string, error) {
		if !validText(value) || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return "", errInvalidSMBOwnership
		}
		number, err := strconv.ParseUint(value, 10, 32)
		if err != nil || number == uint64(^uint32(0)) {
			return "", errInvalidSMBOwnership
		}
		return strconv.FormatUint(number, 10), nil
	}
	canonicalUID, err := parse(uid)
	if err != nil {
		return SMBOwnership{}, err
	}
	canonicalGID, err := parse(gid)
	if err != nil {
		return SMBOwnership{}, err
	}
	return SMBOwnership{UID: canonicalUID, GID: canonicalGID}, nil
}
```

Embed `SMBOwnership` in both structs so the manifest fields stay flat and the structs remain value-comparable for `UpdateManifest`. Place the embedding after every existing `Definition` field so omitted ownership preserves the exact version 1 JSON field order; do not put it at the start of `Definition`:

```go
type CreateRequest struct {
	// Keep every existing field unchanged.
	SMBOwnership
}

type Definition struct {
	// Keep every existing field and JSON tag unchanged.
	SMBOwnership
}
```

- [ ] **Step 4: Run parser tests and verify they pass**

Run: `go test ./internal/modules/storage -run 'Test(ParseSMBOwnership|RemoteTextValidators)'`

Expected: PASS.

- [ ] **Step 5: Write failing version compatibility tests**

Replace the current unsupported-version fixture in `manifest_test.go` with version 3, then add explicit compatibility tests:

```go
func TestLoadDefinitionAcceptsLegacyVersionOneWithoutOwnership(t *testing.T) {
	store := newArtifactStore(t)
	require.NoError(t, os.MkdirAll(store.ManifestRoot, 0o700))
	path := filepath.Join(store.ManifestRoot, testDefinitionID+".json")
	legacy := []byte(`{"created_target":false,"export":"/exports/media","format_version":1,"host":"nas.example","id":"0123456789abcdef0123456789abcdef","protocol":"nfs","protocol_version":"4","read_only":false,"state":"active","target":"/mnt/media","unit_name":"mnt-media.mount"}` + "\n")
	require.NoError(t, os.WriteFile(path, legacy, 0o600))

	definition, err := store.LoadDefinition(testDefinitionID)
	require.NoError(t, err)
	assert.Equal(t, LegacyManifestFormatVersion, definition.FormatVersion)
	assert.Empty(t, definition.UID)
	assert.Empty(t, definition.GID)
	marshaled, err := marshalManifest(definition)
	require.NoError(t, err)
	assert.Equal(t, legacy, marshaled)
}

func TestLoadDefinitionValidatesVersionTwoOwnershipPair(t *testing.T) {
	store := newArtifactStore(t)
	require.NoError(t, os.MkdirAll(store.ManifestRoot, 0o700))
	path := filepath.Join(store.ManifestRoot, testDefinitionID+".json")
	base := `{"format_version":2,"id":"0123456789abcdef0123456789abcdef","protocol":"smb","protocol_version":"3.1.1","server":"nas.example","share":"media","state":"active","target":"/mnt/media","unit_name":"mnt-media.mount"`

	require.NoError(t, os.WriteFile(path, []byte(base+`,"uid":"1000","gid":"100"}`), 0o600))
	definition, err := store.LoadDefinition(testDefinitionID)
	require.NoError(t, err)
	assert.Equal(t, SMBOwnership{UID: "1000", GID: "100"}, definition.SMBOwnership)

	for _, suffix := range []string{`,"uid":"1000"}`, `,"gid":"100"}`, `,"uid":"01000","gid":"100"}`} {
		require.NoError(t, os.WriteFile(path, []byte(base+suffix), 0o600))
		_, err := store.LoadDefinition(testDefinitionID)
		assert.ErrorIs(t, err, errInvalidManifest)
	}
}
```

Also add a case proving version 1 rejects `uid`/`gid` and version 2 NFS rejects them.
Add this lifecycle-level compatibility assertion so compatibility covers exact
artifact verification rather than JSON loading alone:

```go
func TestVerifyOwnedArtifactsAcceptsLegacyVersionOne(t *testing.T) {
	store := newArtifactStore(t)
	definition := testDefinition()
	definition.FormatVersion = LegacyManifestFormatVersion
	require.NoError(t, store.WriteManifest(definition))
	require.NoError(t, store.WriteMountUnit(definition))
	require.NoError(t, store.WriteAutomountUnit(definition))
	require.NoError(t, store.VerifyOwnedArtifacts(definition))
}
```

- [ ] **Step 6: Run manifest tests and verify they fail**

Run: `go test ./internal/modules/storage -run 'Test(LoadDefinition|Manifest)'`

Expected: FAIL because artifact validation still accepts only the current version and does not enforce ownership/version rules.

- [ ] **Step 7: Implement shared manifest ownership validation**

Add this helper in `remote.go`:

```go
func validateDefinitionOwnership(formatVersion int, protocol string, ownership SMBOwnership) error {
	if formatVersion != LegacyManifestFormatVersion && formatVersion != ManifestFormatVersion {
		return errInvalidSMBOwnership
	}
	if formatVersion == LegacyManifestFormatVersion {
		if ownership != (SMBOwnership{}) {
			return errInvalidSMBOwnership
		}
		return nil
	}
	canonical, err := ParseSMBOwnership(ownership.UID, ownership.GID)
	if err != nil || canonical != ownership || protocol != "smb" && ownership != (SMBOwnership{}) {
		return errInvalidSMBOwnership
	}
	return nil
}
```

Return the ownership-specific stable error here because `remote.go` builds on non-Linux platforms while `errInvalidManifest` is Linux-only. Linux artifact/render callers map any helper error to `errInvalidManifest` as shown below.

In both `validateArtifactDefinition` in `manifest.go` and `validateRenderDefinition` in `units.go`, replace the exact `definition.FormatVersion != ManifestFormatVersion` condition with:

```go
if validateDefinitionOwnership(definition.FormatVersion, definition.Protocol, definition.SMBOwnership) != nil {
	return errInvalidManifest
}
```

Keep all existing ID, protocol, target, state, unit-name, source, and credential checks.

- [ ] **Step 8: Run focused domain and manifest tests**

Run: `go test ./internal/modules/storage -run 'Test(ParseSMBOwnership|RemoteTextValidators|LoadDefinition|Manifest)'`

Expected: PASS.

- [ ] **Step 9: Commit the ownership contract**

```bash
git add internal/modules/storage/remote.go internal/modules/storage/remote_test.go internal/modules/storage/manifest.go internal/modules/storage/manifest_test.go internal/modules/storage/units.go
git commit -m "feat: define SMB ownership mapping"
```

### Task 2: Unit Rendering And Remote Manager Integration

**Files:**
- Modify: `internal/modules/storage/units.go`
- Modify: `internal/modules/storage/units_test.go`
- Modify: `internal/modules/storage/remote_manager.go`
- Modify: `internal/modules/storage/remote_manager_test.go`

**Interfaces:**
- Consumes: `SMBOwnership`, `ParseSMBOwnership`, and dual manifest-version validation from Task 1.
- Produces: normalized version 2 definitions and deterministic sorted CIFS `uid=`/`gid=` options.

- [ ] **Step 1: Write failing unit rendering tests**

Extend `TestRenderSMBMountUnitGuestAndCredentialOptions` so each auth mode carries `SMBOwnership{UID: "1000", GID: "100"}` and expects both sorted options:

```go
definition.SMBOwnership = SMBOwnership{UID: "1000", GID: "100"}
```

Expected guest options:

```text
gid=100,guest,nodev,nosuid,rw,uid=1000,vers=3.1.1
```

Expected credentialed options:

```text
credentials=/etc/pilothouse/storage/credentials/0123456789abcdef0123456789abcdef,gid=100,nodev,nosuid,rw,uid=1000,vers=3.1.1
```

Add separate assertions that unmapped version 2 SMB and version 1 NFS retain their existing exact `Options=` lines.

- [ ] **Step 2: Run rendering tests and verify they fail**

Run: `go test ./internal/modules/storage -run 'TestRender(SMB)?MountUnit'`

Expected: FAIL because `mountSettings` does not emit ownership options.

- [ ] **Step 3: Render fixed ownership options for SMB only**

In the SMB branch of `mountSettings`, after protocol validation and before sorting, add:

```go
if definition.UID != "" {
	options = append(options, "uid="+definition.UID, "gid="+definition.GID)
}
```

Do not add ownership logic to the NFS branch and do not change the final `sort.Strings(options)` call.

- [ ] **Step 4: Run rendering tests and verify they pass**

Run: `go test ./internal/modules/storage -run 'TestRender(SMB)?MountUnit'`

Expected: PASS.

- [ ] **Step 5: Write failing manager normalization and lifecycle tests**

Add to `remote_manager_test.go`:

```go
func TestRemoteManagerCreateNormalizesAndPersistsSMBOwnership(t *testing.T) {
	store := testArtifactStore(t)
	request := testSMBRequest(t)
	request.SMBOwnership = SMBOwnership{UID: "001000", GID: "000100"}
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})

	require.NoError(t, manager.Create(context.Background(), request))
	definition, err := store.LoadDefinition(request.ID)
	require.NoError(t, err)
	assert.Equal(t, ManifestFormatVersion, definition.FormatVersion)
	assert.Equal(t, SMBOwnership{UID: "1000", GID: "100"}, definition.SMBOwnership)
	mountPath, err := store.MountUnitPath(definition)
	require.NoError(t, err)
	contents, err := os.ReadFile(mountPath)
	require.NoError(t, err)
	assert.Contains(t, string(contents), "gid=100")
	assert.Contains(t, string(contents), "uid=1000")
}

func TestRemoteManagerRejectsInvalidOrNFSOwnershipBeforeMutation(t *testing.T) {
	for _, request := range []CreateRequest{
		func() CreateRequest { value := testSMBRequest(t); value.UID = "1000"; return value }(),
		func() CreateRequest { value := testNFSRequest(t); value.SMBOwnership = SMBOwnership{UID: "1000", GID: "100"}; return value }(),
	} {
		store := testArtifactStore(t)
		manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{})
		require.Error(t, manager.Create(context.Background(), request))
		assert.NoDirExists(t, request.Target)
		assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
	}
}
```

Extend `TestRemoteManagerMountAndUnmountUseExactUnits` or add a mapped equivalent proving `loadVerified` accepts the persisted mapped unit, and modify its `uid=` option on disk to prove verification fails closed.
Add a mapped rollback case using `recordingUnitController{fail: "start"}` and
assert the manifest, mount unit, automount unit, credential, and manager-created
target are absent after `Create` returns an error:

```go
func TestRemoteManagerMappedCreateRollsBackWhenStartFails(t *testing.T) {
	store := testArtifactStore(t)
	request := testSMBRequest(t)
	request.SMBOwnership = SMBOwnership{UID: "1000", GID: "100"}
	manager := NewSystemRemoteManager(staticManager{}, store, &recordingUnitController{fail: "start"})

	require.Error(t, manager.Create(context.Background(), request))
	assert.NoFileExists(t, filepath.Join(store.ManifestRoot, request.ID+".json"))
	assert.NoFileExists(t, filepath.Join(store.UnitRoot, mountUnitName(request.Target)))
	assert.NoFileExists(t, filepath.Join(store.UnitRoot, automountUnitName(request.Target)))
	assert.NoFileExists(t, filepath.Join(store.CredentialRoot, request.ID))
	assert.NoDirExists(t, request.Target)
}
```

- [ ] **Step 6: Run manager tests and verify they fail**

Run: `go test ./internal/modules/storage -run 'TestRemoteManager(CreateNormalizes|RejectsInvalid|MountAndUnmount)'`

Expected: FAIL because `definition` does not normalize or copy ownership.

- [ ] **Step 7: Normalize ownership in definition construction**

At the start of `SystemRemoteManager.definition`, parse ownership and enforce protocol scope before constructing the definition:

```go
ownership, err := ParseSMBOwnership(request.UID, request.GID)
if err != nil || request.Protocol != "smb" && ownership != (SMBOwnership{}) {
	return Definition{}, errInvalidManifest
}
```

Embed `SMBOwnership: ownership` in the new `Definition`. Keep `FormatVersion: ManifestFormatVersion`, which now writes version 2, and keep every existing protocol/authentication check.

- [ ] **Step 8: Run all storage package tests with race detection**

Run: `go test -race ./internal/modules/storage`

Expected: PASS with no race report.

- [ ] **Step 9: Commit rendering and manager support**

```bash
git add internal/modules/storage/units.go internal/modules/storage/units_test.go internal/modules/storage/remote_manager.go internal/modules/storage/remote_manager_test.go
git commit -m "feat: render SMB ownership options"
```

### Task 3: Fixed Owned Broker Actions

**Files:**
- Modify: `internal/broker/api.go`
- Modify: `internal/modules/storage/remote_test.go`
- Modify: `cmd/pilothoused/main.go`
- Modify: `cmd/pilothoused/main_test.go`

**Interfaces:**
- Consumes: `ParseSMBOwnership` and `CreateRequest.SMBOwnership` from Task 1.
- Produces: `broker.ActionStorageCreateSMBGuestOwned` and `broker.ActionStorageCreateSMBCredentialsOwned` with exact required parameter lists.

- [ ] **Step 1: Write failing action-ID and registration tests**

Extend `TestRemoteBrokerIDs`:

```go
assert.Equal(t, "org.frostyard.pilothouse.storage.create-smb-guest-owned", broker.ActionStorageCreateSMBGuestOwned)
assert.Equal(t, "org.frostyard.pilothouse.storage.create-smb-credentials-owned", broker.ActionStorageCreateSMBCredentialsOwned)
```

Add these cases to `TestRegisterStorageCreateActionsUseTrustedIDsAndGlobalLock`:

```go
{"smb guest owned", broker.ActionStorageCreateSMBGuestOwned,
	map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "001000", "gid": "000100"},
	storage.CreateRequest{Protocol: "smb", Server: "nas.example", Share: "media", Target: "/mnt/media", Version: "3.1.1", SMBOwnership: storage.SMBOwnership{UID: "1000", GID: "100"}}},
{"smb credentials owned", broker.ActionStorageCreateSMBCredentialsOwned,
	map[string]string{"server": "nas.example", "share": "media", "username": "mount-user", "password": "secret", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000", "gid": "100"},
	storage.CreateRequest{Protocol: "smb", Server: "nas.example", Share: "media", Username: "mount-user", Password: "secret", Target: "/mnt/media", Version: "3.1.1", SMBOwnership: storage.SMBOwnership{UID: "1000", GID: "100"}}},
```

Assert `manager.create.SMBOwnership` equals the expected value. Add cases proving missing, extra, sentinel, and malformed ownership parameters fail before `manager.Create`, while the audit resource contains only the opaque ID.

- [ ] **Step 2: Run registration tests and verify they fail**

Run: `go test ./internal/modules/storage ./cmd/pilothoused -run 'Test(RemoteBrokerIDs|RegisterStorageCreate)'`

Expected: FAIL because the owned constants and registrations do not exist.

- [ ] **Step 3: Add exact broker constants**

Add to `internal/broker/api.go` beside the existing SMB constants:

```go
ActionStorageCreateSMBGuestOwned       = "org.frostyard.pilothouse.storage.create-smb-guest-owned"
ActionStorageCreateSMBCredentialsOwned = "org.frostyard.pilothouse.storage.create-smb-credentials-owned"
```

- [ ] **Step 4: Register owned action definitions**

Refactor only the local create-action table in `registerStorageActions`. Add a boolean such as `owned bool` to its SMB entries, and register these exact schemas:

```go
{id: broker.ActionStorageCreateSMBGuestOwned,
	parameters: []string{"server", "share", "target", "version", "read_only", "uid", "gid"},
	request: smbCreateRequest(false, true)},
{id: broker.ActionStorageCreateSMBCredentialsOwned,
	parameters: []string{"server", "share", "username", "password", "target", "version", "read_only", "uid", "gid"},
	request: smbCreateRequest(true, true)},
```

Define the focused parser near `registerStorageActions`:

```go
func smbCreateRequest(credentials, owned bool) func(map[string]string) (storage.CreateRequest, error) {
	return func(parameters map[string]string) (storage.CreateRequest, error) {
		readOnly, err := storage.ParseReadOnly(parameters["read_only"])
		if err != nil {
			return storage.CreateRequest{}, errors.New("invalid remote mount parameter")
		}
		ownership := storage.SMBOwnership{}
		if owned {
			ownership, err = storage.ParseSMBOwnership(parameters["uid"], parameters["gid"])
			if err != nil || ownership == (storage.SMBOwnership{}) {
				return storage.CreateRequest{}, errors.New("invalid remote mount parameter")
			}
		}
		request := storage.CreateRequest{ID: parameters["_id"], Protocol: "smb", Server: parameters["server"], Share: parameters["share"], Target: parameters["target"], Version: parameters["version"], ReadOnly: readOnly, SMBOwnership: ownership}
		if credentials {
			request.Username, request.Password = parameters["username"], parameters["password"]
		}
		return request, nil
	}
}
```

Use this helper for all four SMB registrations. Do not alter NFS, lifecycle actions, preflight, lock, audit resource, or timeout behavior.

- [ ] **Step 5: Run broker registration and package tests**

Run: `go test ./internal/broker ./internal/modules/storage ./cmd/pilothoused -run 'Test(RemoteBrokerIDs|RegisterStorage|Action)'`

Expected: PASS.

- [ ] **Step 6: Commit fixed action support**

```bash
git add internal/broker/api.go internal/modules/storage/remote_test.go cmd/pilothoused/main.go cmd/pilothoused/main_test.go
git commit -m "feat: register owned SMB mount actions"
```

### Task 4: SMB Ownership Form Workflow

**Files:**
- Modify: `internal/modules/storage/module.go`
- Modify: `internal/modules/storage/module_test.go`
- Modify: `internal/modules/storage/views.templ`
- Modify: `internal/modules/storage/views_test.go`
- Generate: `internal/modules/storage/views_templ.go` via `make generate`

**Interfaces:**
- Consumes: the two owned broker IDs and `ParseSMBOwnership`.
- Produces: optional paired Owner UID/GID controls on both SMB forms and exact action selection.

- [ ] **Step 1: Write failing handler dispatch tests**

Extend the existing SMB handler tests with mapped guest and credentialed cases:

```go
{"smb guest owned",
	"protocol=smb-guest&server=nas.example&share=media&target=%2Fmnt%2Fmedia&version=3.1.1&read_only=false&uid=001000&gid=000100",
	broker.ActionStorageCreateSMBGuestOwned,
	map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000", "gid": "100"}},
```

Add an equivalent credentialed assertion using `ActionStorageCreateSMBCredentialsOwned` and preserving `username`/`password`. Add a table test for UID-only, GID-only, sentinel, signed, and malformed values that asserts `host.executeID` stays empty and the response is a stable error redirect without submitted values.

Keep assertions that blank ownership selects the existing action and emits no `uid` or `gid` parameter.

- [ ] **Step 2: Run handler tests and verify they fail**

Run: `go test ./internal/modules/storage -run 'TestStorageActionCreate'`

Expected: FAIL because `remoteCreateAction` ignores ownership and cannot select owned actions.

- [ ] **Step 3: Parse and dispatch optional ownership pairs**

In the SMB branch of `remoteCreateAction`, parse trimmed values and add only canonical configured ownership:

```go
ownership, err := ParseSMBOwnership(strings.TrimSpace(form.Get("uid")), strings.TrimSpace(form.Get("gid")))
if err != nil {
	return "", nil, "", fmt.Errorf("invalid remote mount form")
}
owned := ownership != (SMBOwnership{})
if owned {
	parameters["uid"], parameters["gid"] = ownership.UID, ownership.GID
}
```

For `smb-guest`, return `ActionStorageCreateSMBGuestOwned` when `owned`, otherwise the existing guest action. For `smb-credentials`, return `ActionStorageCreateSMBCredentialsOwned` when `owned`, otherwise the existing credential action. Keep password exclusive to credentialed maps and keep NFS unchanged.

- [ ] **Step 4: Run handler tests and verify they pass**

Run: `go test ./internal/modules/storage -run 'TestStorageActionCreate'`

Expected: PASS.

- [ ] **Step 5: Write failing form rendering tests**

Update `views_test.go` so both SMB protocols require these fragments:

```go
[]string{
	`name="uid"`, `name="gid"`, `type="number"`,
	`min="0"`, `max="4294967294"`, `step="1"`,
	`Owner UID`, `Owner GID`, `both`,
}
```

Add `name="uid"` and `name="gid"` to the NFS absent list. Preserve the assertion that output contains no free-form `options`, credential path, unit, executable, or literal `@web.` syntax.

- [ ] **Step 6: Run rendering tests and verify they fail**

Run: `go test ./internal/modules/storage -run 'TestRemoteMountForm'`

Expected: FAIL because the SMB fields are not rendered.

- [ ] **Step 7: Add SMB-only numeric fields**

In the SMB branch of `RemoteMountForm`, after the version control and before Target, add:

```templ
<label>Owner UID <input type="number" name="uid" min="0" max="4294967294" step="1" inputmode="numeric"/></label>
<label>Owner GID <input type="number" name="gid" min="0" max="4294967294" step="1" inputmode="numeric"/></label>
<p class="field-help">Set both values to map mounted files to a local numeric owner. Leave both blank for default ownership.</p>
```

Do not add these controls to the NFS branch and do not add client-side JavaScript.

- [ ] **Step 8: Generate templ output and run storage tests**

Run: `make generate && go test ./internal/modules/storage`

Expected: both commands exit 0 and all storage tests PASS. Confirm `views_templ.go` changed only through generation.

- [ ] **Step 9: Commit the web workflow**

```bash
git add internal/modules/storage/module.go internal/modules/storage/module_test.go internal/modules/storage/views.templ internal/modules/storage/views_test.go internal/modules/storage/views_templ.go
git commit -m "feat: add SMB ownership fields"
```

### Task 5: Documentation And Full Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/modules.md`
- Modify: `docs/superpowers/specs/2026-07-21-storage-module-design.md`
- Modify: `yeti/OVERVIEW.md`
- Verify: all changed source, generated, test, and documentation files.

**Interfaces:**
- Consumes: completed mapped SMB workflow from Tasks 1-4.
- Produces: accurate human and AI-facing documentation plus repository-wide verification evidence.

- [ ] **Step 1: Document user-visible behavior**

In `README.md`, add a What Works bullet and a short managed-mount note stating:

```markdown
- Optional numeric local UID/GID ownership mapping for Pilothouse-managed SMB mounts
```

Explain that both IDs are required together, blank fields preserve default ownership, and names/free-form options are not accepted.

- [ ] **Step 2: Document the fixed broker and artifact behavior**

In `docs/modules.md`, add the two exact owned action IDs and document that owned actions require canonicalized numeric `uid` and `gid`, persist them in version 2 manifests, and render fixed CIFS options. State that version 1 definitions remain supported without migration.

In `docs/superpowers/specs/2026-07-21-storage-module-design.md`, retain the historical v1 exclusion but append a follow-up note referencing issue 44 and `docs/superpowers/specs/2026-07-22-smb-uid-gid-mapping-design.md`; do not rewrite the original v1 decision as if mapping existed then.

- [ ] **Step 3: Update AI architecture context**

In `yeti/OVERVIEW.md`, update the Storage module and broker-contract descriptions with the two fixed owned actions, paired numeric validation, version 1 compatibility, version 2 persistence, and deterministic `uid=`/`gid=` rendering. Keep the privilege-boundary guidance explicit.

- [ ] **Step 4: Run generation and formatting**

Run: `make generate && make fmt`

Expected: both commands exit 0.

- [ ] **Step 5: Run focused race-enabled tests**

Run: `go test -race ./internal/broker ./internal/modules/storage ./cmd/pilothoused`

Expected: PASS with no race reports.

- [ ] **Step 6: Run required repository verification**

Run these separately so a failure identifies its stage:

```bash
make build
make test
make fmt
make lint
```

Expected: every command exits 0. If native PAM or systemd dependencies are unavailable, run and pass `make docker-build`, `make docker-test`, `make docker-fmt`, and `make docker-lint` instead.

- [ ] **Step 7: Inspect generated and final changes**

Run:

```bash
git status --short
git diff --check
git diff --stat
```

Expected: only the intended SMB ownership source, tests, generated templ output, and documentation are changed; `git diff --check` prints nothing.

- [ ] **Step 8: Commit documentation and verification fixes**

```bash
git add README.md docs/modules.md docs/superpowers/specs/2026-07-21-storage-module-design.md yeti/OVERVIEW.md
git commit -m "docs: describe SMB ownership mapping"
```
