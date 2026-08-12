# 0002 — One canonical resource key per action: the audited resource is the default lock key

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

Every broker action needs three things tied to the entity it touches: an
audit record that later rows can be joined against, serialization so two
mutations of the same entity cannot interleave, and (for destructive actions)
a typed confirmation string. If those three derive from different strings,
the failure modes are quiet: audit rows for the same entity stop joining,
two actions on one entity take different locks and interleave, or a
confirmation checks a string the audit never records.

## Decision

Each action definition derives a single canonical resource string from its
parameters (`definition.Resource(parameters)`), validated lexically —
non-empty, at most 1024 bytes, no `\r`, `\n`, or NUL
(`internal/broker/actions.go:115-121`; streams:
`internal/broker/streams.go:318-320`). That one string is:

- the audit record's `Resource` (`actions.go:147`, `streams.go:226`),
- the destructive-action confirmation value the caller must echo
  (`actions.go:122`),
- the durable job record's resource (`actions.go:158`), and
- **by default** the serialization-lock key: `lockResource := resource`
  (`actions.go:125`), locked in the plain string-keyed table in
  `internal/broker/serialize.go`.

An action may override only the lock key via `LockResource`
(`actions.go:126`) to serialize *more coarsely* than it audits — never the
audit resource. The landed overrides are deliberate and documented in place:
reboot audits `maintenance/reboot` but locks `maintenance/global`
(`cmd/pilothoused/main.go:818-826, 853-854`); sysext enable/disable audit
`sysext/feature/<name>` but share the `sysext/global` lock
(`main.go:1019-1024, 1052-1056`); storage create audits
`storage/mount/<id>` but locks `storage/mounts` (`main.go:599`).
`TestRebootAndSysextActionsDoNotContendForOneLock`
(`cmd/pilothoused/main_test.go:699`) pins that audit identity and lock
granularity are independently chosen.

Keys follow a `<module>/<type>/<id…>` convention — `services/unit/<unit>`,
`podman/container/<id>`, `incus/instance/<project>/<name>`,
`incus/snapshot/<project>/<instance>/<snapshot>`, `sysext/feature/<name>`,
`storage/mount/<id>` (all in `cmd/pilothoused/main.go`) — with two-segment
keys for module-wide resources (`maintenance/reboot`, `sysext/global`,
`storage/mounts`) and the files module using its configured root ID as the
second segment (`files/<root>/<directory>/<name>`, `main.go:784-793`). The
convention is enforced by per-action tests on the concrete values (e.g. the
`^storage/mount/[a-f0-9]{32}$` regex in `cmd/pilothoused/main_test.go:886`),
not by a runtime grammar check; the runtime check is the lexical validation
above.

## Consequences

- Audit rows for one entity join on one string, and by default the same
  string is what serializes mutations — an action added with the default
  path cannot lock one entity while auditing another.
- Coarser locking is available without corrupting audit identity, but every
  `LockResource` override is a reviewed, comment-carrying decision; the
  overrides are exactly where lock contention semantics differ from audit
  semantics, so they must stay rare and documented.
- Resource-key strings are load-bearing: renaming a key's shape orphans
  historical audit rows for that entity. Changes need the same care as a
  schema migration.
- The grammar being convention-plus-tests means a new module can deviate
  without a build failure; review and the per-action value tests are the
  guard. A violating key costs either collisions under one lock or
  un-joinable audit rows.

## Alternatives considered

- **Separate, independently-derived lock and audit strings per action:** the
  quiet-drift failure mode described in Context; rejected.
- **Structured resource type (module/type/id struct) instead of a string:**
  the string is what audit storage, confirmation echo, and the lock map all
  need; a struct still serializes to one canonical string, adding a layer
  without removing the invariant.
- **Runtime-enforced grammar (regex on every key):** would have to encode the
  legitimate two-segment and files-root shapes, turning a readable convention
  into a lookup table; lexical validation plus per-action tests give the
  enforcement that matters (no injection, pinned concrete values).

## References

- Shapes: [design/overview.md](../design/overview.md) (per-resource action
  serialization), [authentication.md](../authentication.md) (audit and the
  canonical resource), [modules.md](../modules.md) (rules for actions)
- Implemented in: `internal/broker/actions.go`, `internal/broker/streams.go`,
  `internal/broker/serialize.go`, `cmd/pilothoused/main.go`
- Enforced by: `internal/broker/action_safety_test.go`
  (`TestActionsSerializeSameResourceAndAllowDifferentResources`),
  `cmd/pilothoused/main_test.go`
  (`TestRebootAndSysextActionsDoNotContendForOneLock`, per-action resource
  value tests)
- Related: [0001 — broker wire surface](0001-versioned-broker-wire-surface.md)
