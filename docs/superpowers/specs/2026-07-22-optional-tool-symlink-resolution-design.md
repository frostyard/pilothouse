# Optional Tool Symlink Resolution Design

## Problem

The privileged broker validates optional storage executables before startup.
The resolver currently uses `os.Lstat` and rejects every symbolic link. Common
Linux LVM packages install `pvs`, `vgs`, and `lvs` as root-owned symlinks to the
regular `/usr/sbin/lvm` multicall executable, so a standard installation causes
`pilothoused` to exit during storage initialization.

The resulting unavailable broker prevents login. A browser can surface an
`invalid csrf token` response when it submits a login page rendered by a prior
web-process instance, but that is secondary to the broker startup failure.

## Design

The optional-tool resolver will accept a fixed candidate path when its complete
symlink chain resolves to a safe executable target. Validation applies to the
resolved target, which must be:

- a regular file;
- owned by root; and
- not writable by group or others.

The resolver will return the original fixed candidate path rather than the
resolved path. This preserves multicall behavior based on `argv[0]`, including
LVM's `pvs`, `vgs`, and `lvs` entry points.

Missing candidates remain unsupported without error. Broken symlinks,
resolution failures, non-regular targets, non-root-owned targets, and
group/world-writable targets remain startup errors. The existing fail-closed
behavior also remains: if any configured candidate exists but is unsafe, a
previously found safe candidate does not hide it.

## Scope

Production `newStorageManager` passes `NewOptionalToolResolver` to
`NewToolsetWithResolver`, so this shared resolver also permits the fixed core
`lsblk` and `findmnt` candidates to be safe symlinks. Candidate lists, tool
invocation arguments, storage data parsing, broker protocols, authentication,
and the separate `resolveSystemTool` behavior do not change.

## Testing

Resolver tests will prove that:

- a symlink to a safe regular target is accepted and returns the symlink path;
- a broken symlink is rejected;
- a symlink to a non-regular target is rejected;
- unsafe target ownership or permissions are rejected; and
- an unsafe present candidate still invalidates the complete candidate set.

The repository's required build, test, formatting, and lint targets will run
after implementation.

## Documentation

Authentication troubleshooting will identify broker startup as the first issue
to investigate when login is unavailable. Storage module documentation will
state that fixed optional-tool paths may be symlinks, but their resolved targets
must satisfy the executable ownership, type, and permission checks.
