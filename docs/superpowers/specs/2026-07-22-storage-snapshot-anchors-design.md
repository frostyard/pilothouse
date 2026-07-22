# Storage Snapshot Anchors Design

## Purpose

Fix issue #46, where the `/storage` snapshot begins with a large empty area.
The empty area is caused by resource and finding anchors rendered as empty
direct children of the `storage-snapshot` CSS grid. Each span occupies a grid
row, so the grid's gap accumulates before the visible cards.

The fix must also improve the existing deep-link behavior. Attention and
topology links should land on the matching visible storage row instead of the
top of the snapshot.

## Scope

The change is limited to storage view anchor allocation and rendering:

- Remove empty top-level anchor spans from both regular and managed snapshot
  regions.
- Put resource fragment IDs on their Storage Inventory rows.
- Put mount-specific fragment IDs on their Mounted Storage rows.
- Put fragment IDs with no matching resource or mount on the first matching
  Storage Attention row.
- Preserve deterministic, unique HTML IDs when resource and mount identifiers
  collide or distinct identifiers sanitize to the same fragment.
- Preserve the existing HTMX snapshot refresh behavior and all storage data.

The broker contract, normalized snapshot model, collectors, health semantics,
and remote-mount mutations do not change.

## Anchor Allocation

Replace the current `anchorSet` lists with aligned assignments for resources,
mounts, and findings. Each assignment has the same length and order as the
corresponding snapshot slice, so templates can apply IDs without searching or
recomputing collision rules.

Allocation proceeds deterministically:

1. Allocate each resource's sanitized `storageAnchor(resource.ID)` to its
   inventory row. If the sanitized value is already used, append `-` until it
   is unique.
2. For each mount, use `storageAnchor(mount.ID)` when a finding targets that
   mount ID and no resource has already claimed the fragment. Otherwise use
   the existing `mount-` prefix before applying the same uniqueness rule.
3. For each finding, reuse the already allocated resource or mount fragment
   for its `ResourceID`. The matching visible resource or mount row remains the
   sole owner of that HTML ID.
4. If a finding targets no visible resource or mount, allocate its sanitized
   fragment to the first finding with that `ResourceID`. Later findings for the
   same ID receive no `id` attribute, preventing duplicate IDs.
5. Findings with an empty `ResourceID` receive no fragment ID.

This keeps Attention links for normal device findings pointed at inventory,
managed-mount findings pointed at the mount table, and invalid-manifest or
other orphan findings pointed at their visible Attention entry.

## Template Changes

`SnapshotContents` and `ManagedSnapshotRegion` will no longer emit anchor
spans. They will pass aligned anchor assignments to:

- `StorageAttention`, which applies orphan finding anchors to the relevant
  finding row.
- `MountTable` and `ManagedMountTable`, which apply mount anchors to table
  rows.
- `ResourceInventory`, which applies resource anchors to table rows.

Topology links continue to use the allocated resource fragments rather than
recomputing IDs independently. The section remains the sole 30-second HTMX
replacement target, so every refreshed fragment includes a coherent set of
links and targets.

No new CSS is required. Removing the empty direct grid children eliminates the
accumulated gap while preserving the existing card spacing.

## Error Handling And Compatibility

Anchor allocation is presentation-only and cannot make the storage snapshot
unavailable. Empty or unusual identifiers continue through the existing
sanitizer and deterministic collision handling. No compatibility shim is
needed because fragment identifiers are generated from the same storage IDs;
only their location in the document changes.

The HTML remains valid when multiple findings refer to one resource, when a
mount ID overlaps a resource ID, and when distinct IDs sanitize to the same
value.

## Testing

Rendering tests will cover:

- No empty top-level anchor spans precede the visible snapshot cards.
- Resource fragment IDs appear exactly once on inventory rows.
- Managed-mount findings target the corresponding mount row.
- Findings without a matching resource or mount target the first matching
  Attention row.
- Duplicate findings and sanitized collisions produce unique IDs.
- Topology links use the allocated resource fragments.
- Regular and managed pages use the same allocation behavior.
- Existing HTMX attributes remain present.
- Rendered HTML contains component output and no literal `@web.` syntax.

After editing `views.templ`, run `make generate`. Before handoff, run
`make build`, `make test`, `make fmt`, and `make lint`, using the matching
Docker targets if native dependencies are unavailable.

## Documentation

Update the storage behavior in `README.md` and `yeti/OVERVIEW.md` to state that
Attention and topology deep links land on visible inventory, mount, or finding
rows. `CLAUDE.md` is absent in this checkout, so there is no file to update.
