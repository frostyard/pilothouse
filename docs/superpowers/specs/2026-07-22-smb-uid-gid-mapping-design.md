# SMB UID/GID Mapping Design

## Purpose

Fix issue 44 by allowing an administrator to assign numeric local UID and GID
ownership to files presented through a Pilothouse-managed SMB mount. Ownership
mapping is optional; an SMB definition without a mapping preserves the current
mount behavior.

## Scope

This change will:

- Add optional paired Owner UID and Owner GID fields to guest and credentialed
  SMB creation forms.
- Accept numeric IDs only. Names are not resolved through NSS.
- Require both IDs when ownership mapping is requested.
- Persist the mapping in the managed definition and render deterministic CIFS
  `uid=` and `gid=` mount options.
- Keep existing managed definitions and existing SMB create actions working.

This change will not:

- Apply UID/GID mapping to NFS mounts.
- Add arbitrary mount options.
- Resolve user or group names.
- Edit ownership on an existing managed definition.
- Display ownership mapping in the storage snapshot or mount table.
- Change filesystem permissions, modes, ACLs, or server-side ownership.

## Broker Contract

Keep the existing SMB create actions unchanged for definitions without an
ownership mapping:

- `org.frostyard.pilothouse.storage.create-smb-guest`
- `org.frostyard.pilothouse.storage.create-smb-credentials`

Add two fixed administrator-only actions for the distinct parameter shapes:

- `org.frostyard.pilothouse.storage.create-smb-guest-owned`
- `org.frostyard.pilothouse.storage.create-smb-credentials-owned`

The owned guest action accepts `server`, `share`, `target`, `version`,
`read_only`, `uid`, and `gid`. The owned credential action accepts the same
parameters plus `username` and `password`. They use the existing non-mutating
create preflight, opaque definition ID, `storage/mount/<id>` audit resource,
global `storage/mounts` creation lock, administrator check, and two-minute
timeout.

The web process selects an owned action only when both ownership fields are
present. It rejects a one-sided pair or malformed decimal input before broker
dispatch. The privileged action registration and remote manager independently
repeat all validation.

## Ownership Model And Validation

Represent an ownership mapping as one optional paired domain value containing
`uint32` UID and GID fields. Do not use zero values to represent absence because
UID or GID 0 is a valid explicit mapping.

Submitted values must contain only unsigned base-10 digits and represent an
integer in the inclusive range 0 through 4294967294. Reject signs, whitespace
within a value, alternate bases, overflow, and 4294967295, which is the Linux
unmapped-ID sentinel. Normalize accepted values to canonical decimal strings
before crossing into artifact rendering.

The mapping is valid only for protocol `smb`. Both values must be absent or both
must be present. Guest and credentialed SMB definitions support the same
mapping behavior. NFS requests and definitions reject ownership fields.

## Manifest Compatibility

New definitions use manifest format version 2. Add optional `uid` and `gid`
fields that are emitted together only for a mapped SMB definition.

The strict manifest loader continues to accept format version 1 definitions.
Version 1 must not contain ownership fields and regenerates the same manifest
and unit bytes as before. Version 2 accepts both ownership fields or neither and
applies the protocol restrictions above. Supporting both versions allows all
persisted definitions to remain mountable, unmountable, verifiable, and
deletable without an in-place migration.

Artifact verification continues to compare exact deterministic content. A
loaded version 1 definition remains version 1 during verification, so its
manifest and generated units are not rewritten or treated as tampered.

## Unit Rendering

For a mapped SMB definition, add these options to the generated CIFS mount
unit:

```text
uid=<canonical-decimal-uid>
gid=<canonical-decimal-gid>
```

Sort them with the existing fixed options before rendering the `Options=` line.
Unmapped SMB definitions and all NFS definitions retain byte-for-byte existing
unit output. The options remain manager-generated; no free-form option reaches
the broker or artifact renderer.

Persisting the mapping in the manifest makes ownership verification regenerate
the same unit. A modified `uid=` or `gid=` option therefore causes the existing
fail-closed artifact ownership behavior.

## Web Interface

Both SMB form variants show optional `Owner UID` and `Owner GID` numeric fields.
Adjacent help text explains that both values are required to map mounted files
to a local account and that leaving both blank preserves default ownership.
NFS forms do not render these fields.

Use HTML numeric constraints for usability, including minimum 0, maximum
4294967294, and step 1. These constraints are not security controls; the web
handler and privileged manager validate the submitted strings independently.

Creation remains a normal CSRF-protected POST and does not use confirmation.
Validation and action failures use the existing stable notice and never reflect
UID, GID, credentials, or privileged error details.

## Error Handling

Reject the request without broker dispatch when the web form contains only one
ownership field or either field fails strict numeric validation. At the broker
boundary, reject malformed action parameters before calling the manager. The
manager rejects inconsistent requests before creating a target or writing any
artifact.

Failures after artifact mutation use the existing reverse-order rollback. The
new mapping adds no separate artifact or cleanup operation, so rollback and
`needs-attention` behavior are unchanged.

## Testing

Domain tests cover:

- Valid IDs 0 and 4294967294.
- Rejection of 4294967295, overflow, signs, internal whitespace, alternate
  bases, empty values, and invalid UTF-8; normalization of leading zeroes.
- Pair enforcement and SMB-only enforcement.

Broker and handler tests cover:

- Exact new action IDs and exact parameter schemas.
- Guest and credentialed owned request construction.
- Selection of existing actions when both fields are blank.
- Selection of owned actions when both fields are present.
- No dispatch for one-sided or malformed pairs.
- Existing administrator, CSRF, audit-resource, timeout, and secret-redaction
  behavior.

Manifest and unit tests cover:

- Loading and exact verification of existing version 1 definitions.
- Strict version 2 paired-field validation and unknown-field rejection.
- Deterministic `uid=` and `gid=` options for guest and credentialed SMB.
- Byte-for-byte unchanged output for version 1, unmapped version 2 SMB, and NFS
  definitions.
- Detection of a modified ownership option by artifact verification.
- Existing create rollback behavior with mapped requests.

Rendering tests cover both SMB forms, absence from NFS, numeric constraints,
help text, escaping, and the required check that rendered HTML contains no
literal `@web.` invocation syntax.

## Documentation And Verification

Update `README.md`, `docs/modules.md`, the relevant storage design context, and
`yeti/OVERVIEW.md` to document optional numeric SMB UID/GID mapping and the two
new fixed actions.

After templ changes, run `make generate`. Before handoff, run `make build`,
`make test`, `make fmt`, and `make lint`, using the matching Docker targets if
native dependencies are unavailable.
