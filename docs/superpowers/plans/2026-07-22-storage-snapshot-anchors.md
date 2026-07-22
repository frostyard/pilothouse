# Storage Snapshot Anchors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the empty area above `/storage` while making storage deep links land on their matching visible inventory, mount, or Attention rows.

**Architecture:** Allocate all storage fragment IDs once from a complete snapshot, preserving deterministic collision handling. Pass aligned IDs into the templ components that own the visible rows, and make topology links consume the same resource-ID-to-fragment map so no empty grid children or independently recomputed targets remain.

**Tech Stack:** Go 1.26, templ, HTMX, vanilla CSS, testify, Markdown documentation.

## Global Constraints

- Keep the change inside storage presentation code; do not change the broker contract, normalized snapshot model, collectors, health semantics, or remote-mount mutations.
- Remove empty top-level anchor spans from regular and managed snapshot regions.
- Keep `#storage-snapshot` as the sole 30-second HTMX replacement target with its existing attributes.
- Put resource IDs on Storage Inventory rows, mount-only IDs on Mounted Storage rows, and unmatched IDs on the first matching Storage Attention row.
- Keep every rendered HTML `id` unique when IDs overlap or sanitize to the same value.
- Findings with an empty `ResourceID` do not receive an anchor.
- Run `make generate` after editing `internal/modules/storage/views.templ`; never edit `views_templ.go` manually.
- Every changed templ component invocation must have rendering coverage that checks component output and rejects literal `@web.` syntax.
- Review and update `README.md`, the absent `CLAUDE.md`, and `yeti/OVERVIEW.md` as required by `AGENTS.md`.
- Create one focused commit after each task passes its review gate.

## File Structure

- Modify `internal/modules/storage/views.templ`: allocate snapshot anchors, attach them to visible rows, and make topology consume allocated resource targets.
- Modify `internal/modules/storage/views_test.go`: reproduce issue #46 and cover semantic targets, orphan findings, duplicates, collisions, managed rendering, HTMX, and templ composition.
- Modify `internal/modules/storage/module.go`: use allocated finding targets for aggregate Attention paths.
- Modify `internal/modules/storage/module_test.go`: verify colliding and empty finding targets.
- Modify `README.md`: document useful storage Attention/topology deep links.
- Modify `yeti/OVERVIEW.md`: record anchor ownership and collision behavior for future agents.

---

### Task 1: Semantic Storage Snapshot Anchors

**Files:**
- Modify: `internal/modules/storage/views.templ:48-64,97-117,135-169,171-247,319-389`
- Test: `internal/modules/storage/views_test.go:45-60,62-102,187-206`
- Modify: `internal/modules/storage/module.go:28-42`
- Test: `internal/modules/storage/module_test.go:84-119`

**Interfaces:**
- Consumes: `Snapshot`, `Resource.ID`, `Mount.ID`, `Finding.ResourceID`, and `storageAnchor(string) string`.
- Produces: `snapshotAnchorSet(snapshot Snapshot) anchorSet`, where `anchorSet.findings`, `anchorSet.findingTargets`, `anchorSet.mounts`, and `anchorSet.resources` align with their snapshot slices and `anchorSet.resourceByID` maps resource IDs to their allocated fragments.
- Produces: `StorageAttention(findings []Finding, truncated bool, anchors []string)`, `MountTable(mounts []Mount, anchors []string)`, `ManagedMountTable(mounts []Mount, anchors []string, csrf string, admin bool)`, `Topology(resources []Resource, relations []Relation, anchors map[string]string)`, and `ResourceInventory(resources []Resource, anchors []string)`.

- [ ] **Step 1: Add a failing regression test for visible anchor ownership**

Replace `TestPageRendersUniqueResourceAndFindingAnchors` with tests that cover regular and managed rendering. Use this regular-page test:

```go
func TestPagePlacesSnapshotAnchorsOnVisibleRows(t *testing.T) {
	snapshot := Snapshot{
		Resources: []Resource{
			{ID: "disk:abc", Name: "Primary"},
			{ID: "disk/abc", Name: "Collision"},
		},
		Mounts: []Mount{{ID: "remote:mount", Target: "/managed"}},
		Findings: []Finding{
			{ResourceID: "disk:abc", Title: "Device warning"},
			{ResourceID: "disk:abc", Title: "Second device warning"},
			{ResourceID: "remote:mount", Title: "Mount warning"},
			{ResourceID: "orphan:manifest", Title: "Manifest warning"},
			{ResourceID: "orphan:manifest", Title: "Second manifest warning"},
			{Title: "Global warning"},
		},
	}

	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))
	html := output.String()

	assert.NotContains(t, html, `<span id=`)
	assert.Contains(t, html, `<tr id="disk-abc">`)
	assert.Contains(t, html, `<tr id="disk-abc-">`)
	assert.Contains(t, html, `<tr id="remote-mount">`)
	assert.Contains(t, html, `<div id="orphan-manifest" class="mini-row storage-details">`)
	assert.Equal(t, 1, strings.Count(html, `id="disk-abc"`))
	assert.Equal(t, 1, strings.Count(html, `id="disk-abc-"`))
	assert.Equal(t, 1, strings.Count(html, `id="remote-mount"`))
	assert.Equal(t, 1, strings.Count(html, `id="orphan-manifest"`))
	assert.NotContains(t, html, `id=""`)
	assert.NotContains(t, html, `@web.`)
}
```

Add managed-page coverage to prove both snapshot regions use the same allocator:

```go
func TestManagedPagePlacesFindingAnchorOnManagedMountRow(t *testing.T) {
	snapshot := Snapshot{
		Mounts:   []Mount{{ID: "remote:0123456789abcdef0123456789abcdef", Managed: true, Target: "/managed"}},
		Findings: []Finding{{ResourceID: "remote:0123456789abcdef0123456789abcdef", Title: "Managed mount warning"}},
	}

	var output strings.Builder
	require.NoError(t, ManagedPage(snapshot, false, "csrf", true).Render(context.Background(), &output))
	html := output.String()

	assert.Contains(t, html, `<tr id="remote-0123456789abcdef0123456789abcdef">`)
	assert.Equal(t, 1, strings.Count(html, `id="remote-0123456789abcdef0123456789abcdef"`))
	assert.NotContains(t, html, `<span id=`)
	assert.NotContains(t, html, `@web.`)
}
```

- [ ] **Step 2: Add a failing topology collision test**

Replace `TestRenderTopologyLinksFriendlyResourceNames` with a version that passes explicit allocated targets and proves links do not recompute colliding fragments:

```go
func TestRenderTopologyUsesAllocatedResourceAnchors(t *testing.T) {
	resources := []Resource{
		{ID: "disk:abc", Name: "Primary disk"},
		{ID: "disk/abc", Name: "<collision>"},
	}
	relations := []Relation{
		{From: "disk:abc", To: "disk/abc", Kind: "contains"},
		{From: "disk/abc", To: "missing", Kind: "mounts"},
	}
	anchors := map[string]string{"disk:abc": "disk-abc", "disk/abc": "disk-abc-"}

	var output strings.Builder
	require.NoError(t, Topology(resources, relations, anchors).Render(context.Background(), &output))
	html := output.String()

	assert.Contains(t, html, `<a href="#disk-abc">Primary disk</a>`)
	assert.Contains(t, html, `<a href="#disk-abc-">&lt;collision&gt;</a>`)
	assert.Contains(t, html, `Unknown resource`)
	assert.NotContains(t, html, `href="#missing"`)
	assert.NotContains(t, html, `@web.`)
}
```

Extend `internal/modules/storage/module_test.go` with aggregate Attention coverage for the same collision and an empty resource ID:

```go
func TestHealthUsesAllocatedFindingAnchors(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{
		Resources: []Resource{{ID: "disk:abc"}, {ID: "disk/abc"}},
		Findings:  []Finding{{ResourceID: "disk/abc", Severity: HealthWarning}, {Severity: HealthWarning}},
	}}

	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 2)
	assert.Equal(t, "/storage#disk-abc-", findings[0].Path)
	assert.Equal(t, "/storage", findings[1].Path)
}
```

- [ ] **Step 3: Run the focused tests and verify the red state**

Run:

```bash
go test ./internal/modules/storage -run 'Test(PagePlacesSnapshotAnchors|ManagedPagePlacesFindingAnchor|RenderTopologyUsesAllocated|HealthUsesAllocatedFindingAnchors)'
```

Expected: build failures because `Topology` still takes two arguments, followed by assertion failures against the current empty `<span>` anchors once the signature is updated.

- [ ] **Step 4: Replace list-only allocation with aligned semantic assignments**

Replace `snapshotAnchors`, the current `anchorSet`, `snapshotAnchorSet`, and `mountAnchors` with:

```go
type anchorSet struct {
	findings     []string
	findingTargets []string
	mounts       []string
	resources    []string
	resourceByID map[string]string
}

func snapshotAnchorSet(snapshot Snapshot) anchorSet {
	anchors := anchorSet{
		findings:     make([]string, len(snapshot.Findings)),
		findingTargets: make([]string, len(snapshot.Findings)),
		mounts:       make([]string, len(snapshot.Mounts)),
		resources:    make([]string, len(snapshot.Resources)),
		resourceByID: make(map[string]string, len(snapshot.Resources)),
	}
	used := make(map[string]struct{}, len(snapshot.Resources)+len(snapshot.Mounts)+len(snapshot.Findings))
	unique := func(base string) string {
		anchor := base
		for {
			if _, exists := used[anchor]; !exists {
				used[anchor] = struct{}{}
				return anchor
			}
			anchor += "-"
		}
	}

	for index, resource := range snapshot.Resources {
		anchor := unique(storageAnchor(resource.ID))
		anchors.resources[index] = anchor
		anchors.resourceByID[resource.ID] = anchor
	}

	targetedMounts := make(map[string]bool, len(snapshot.Findings))
	for _, finding := range snapshot.Findings {
		targetedMounts[finding.ResourceID] = true
	}
	mountByID := make(map[string]string, len(snapshot.Mounts))
	for index, mount := range snapshot.Mounts {
		base := "mount-" + storageAnchor(mount.ID)
		if targetedMounts[mount.ID] {
			if _, resourceOwnsTarget := anchors.resourceByID[mount.ID]; !resourceOwnsTarget {
				base = storageAnchor(mount.ID)
			}
		}
		anchor := unique(base)
		anchors.mounts[index] = anchor
		if _, exists := mountByID[mount.ID]; !exists {
			mountByID[mount.ID] = anchor
		}
	}

	orphanByID := make(map[string]string)
	for index, finding := range snapshot.Findings {
		if finding.ResourceID == "" {
			continue
		}
		if anchor, exists := anchors.resourceByID[finding.ResourceID]; exists {
			anchors.findingTargets[index] = anchor
			continue
		}
		if anchor, exists := mountByID[finding.ResourceID]; exists {
			anchors.findingTargets[index] = anchor
			continue
		}
		if anchor, exists := orphanByID[finding.ResourceID]; exists {
			anchors.findingTargets[index] = anchor
			continue
		}
		anchor := unique(storageAnchor(finding.ResourceID))
		orphanByID[finding.ResourceID] = anchor
		anchors.findings[index] = anchor
		anchors.findingTargets[index] = anchor
	}
	return anchors
}
```

This retains the existing `mount-` fragment for mounts that are not direct finding targets, while allowing `/storage#remote-...` to land on a managed mount row.

- [ ] **Step 5: Attach allocated IDs to visible rows**

In both `ManagedSnapshotRegion` and `SnapshotContents`, delete the loop that renders `<span id={ anchor }></span>`. Pass the aligned assignments:

```templ
@StorageMetrics(snapshot.Summary)
@StorageAttention(snapshot.Findings, snapshot.Truncated, anchors.findings)
@MountTable(snapshot.Mounts, anchors.mounts)
@Topology(snapshot.Resources, snapshot.Relations, anchors.resourceByID)
@ResourceInventory(snapshot.Resources, anchors.resources)
@BackendStates(snapshot.Backends)
```

Use `ManagedMountTable` instead of `MountTable` in `ManagedSnapshotRegion`, preserving its `csrf` and `admin` arguments.

Change the Attention loop to call a small component that omits the attribute when there is no owned anchor:

```templ
for index, finding := range findings {
	@StorageFinding(finding, anchors[index])
}
```

```templ
templ StorageFinding(finding Finding, anchor string) {
	if anchor == "" {
		<div class="mini-row storage-details"><div><strong>{ finding.Title }</strong><small>{ finding.Detail }</small></div><span class={ "badge", string(finding.Severity) }>{ healthLabel(finding.Severity) }</span></div>
	} else {
		<div id={ anchor } class="mini-row storage-details"><div><strong>{ finding.Title }</strong><small>{ finding.Detail }</small></div><span class={ "badge", string(finding.Severity) }>{ healthLabel(finding.Severity) }</span></div>
	}
}
```

Apply mount IDs directly to the existing mount `<tr>` elements with `id={ anchors[index] }`. Change `ResourceInventory` to iterate with an index and apply `id={ anchors[index] }` to each existing resource `<tr>`.

- [ ] **Step 6: Make topology use the allocated resource map**

Change the topology functions and endpoint lookup to these signatures:

```templ
templ Topology(resources []Resource, relations []Relation, anchors map[string]string) {
	<article class="card table-card storage-topology">
		<div class="table-toolbar"><h2>Storage topology</h2><span>{ fmt.Sprintf("%d relations", len(relations)) }</span></div>
		if len(relations) == 0 {
			<div class="empty-state">No storage relationships were reported.</div>
		} else {
			@TopologyRelations(resourceIndex(resources), relations, anchors)
		}
	</article>
}
```

```templ
templ TopologyRelations(resources map[string]Resource, relations []Relation, anchors map[string]string) {
	<div class="storage-tree">
		for _, relation := range relations {
			<div class="storage-node"><strong>
				@TopologyEndpoint(endpointFor(resources, anchors, relation.From))
				→
				@TopologyEndpoint(endpointFor(resources, anchors, relation.To))
			</strong><small>{ relation.Kind }</small></div>
		}
	</div>
}
```

```go
func endpointFor(resources map[string]Resource, anchors map[string]string, id string) topologyEndpoint {
	resource, found := resources[id]
	if !found {
		return topologyEndpoint{}
	}
	return topologyEndpoint{anchor: anchors[id], found: true, name: resource.Name}
}
```

- [ ] **Step 7: Use allocated targets in aggregate Attention paths**

Change `Module.Health` so its paths use the same assignments as the rendered page and omit an empty fragment:

```go
func (*Module) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	snapshot, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	anchors := snapshotAnchorSet(snapshot)
	findings := make([]platform.Finding, 0, len(snapshot.Findings))
	for index, finding := range snapshot.Findings {
		path := "/storage"
		if anchors.findingTargets[index] != "" {
			path += "#" + anchors.findingTargets[index]
		}
		findings = append(findings, platform.Finding{
			Detail: finding.Detail, ID: "storage." + finding.ResourceID,
			Path: path, Severity: storageSeverity(finding.Severity),
			Source: "Storage", Title: finding.Title,
		})
	}
	return findings, nil
}
```

- [ ] **Step 8: Generate templates and run the focused tests**

Run:

```bash
make generate && go test ./internal/modules/storage -run 'Test(PagePlacesSnapshotAnchors|ManagedPagePlacesFindingAnchor|RenderTopologyUsesAllocated|HealthUsesAllocatedFindingAnchors|RenderStorageOperations)'
```

Expected: templ generation succeeds and all selected tests pass, including existing HTMX, escaping, and component-output assertions.

- [ ] **Step 9: Run the complete storage package tests**

Run:

```bash
go test ./internal/modules/storage
```

Expected: PASS.

- [ ] **Step 10: Review and commit the task diff**

Run:

```bash
git diff --check && git diff -- internal/modules/storage/views.templ internal/modules/storage/views_test.go internal/modules/storage/module.go internal/modules/storage/module_test.go
```

Expected: no whitespace errors; only the anchor allocation, component signatures, semantic IDs, generated templ output, and tests change.

Commit the reviewed implementation:

```bash
git add internal/modules/storage/views.templ internal/modules/storage/views_test.go internal/modules/storage/module.go internal/modules/storage/module_test.go
git commit -m "fix: place storage anchors on visible rows"
```

### Task 2: Documentation And Project Verification

**Files:**
- Modify: `README.md:7-30`
- Modify: `yeti/OVERVIEW.md:121-142`
- Verify absent: `CLAUDE.md`
- Test: all files changed in Task 1 and Task 2

**Interfaces:**
- Consumes: the semantic anchor behavior implemented in Task 1.
- Produces: human- and AI-facing documentation plus full repository verification evidence.

- [ ] **Step 1: Update human-facing storage behavior**

Add this bullet under `README.md`'s `What works` list after the live Attention bullet:

```markdown
- Storage Attention and topology deep links that land on the matching inventory, mount, or finding row
```

- [ ] **Step 2: Update AI-facing architecture context**

Add this paragraph after the templ composition rule in `yeti/OVERVIEW.md`:

```markdown
- **Storage snapshot anchors.** Storage allocates fragment IDs once per
  snapshot and puts them on visible inventory, mount, or Attention rows.
  Topology links consume the same resource-to-fragment map. Do not restore
  empty anchor spans as direct children of `.storage-snapshot`: it is a CSS
  grid, so each span creates an empty grid row and accumulates visible gaps.
```

- [ ] **Step 3: Confirm the repository has no `CLAUDE.md` to update**

Run:

```bash
test ! -e CLAUDE.md
```

Expected: exit 0. If the file appears due to concurrent work, read it and add the same storage anchor rule in its relevant frontend or module section.

- [ ] **Step 4: Run formatting and generation**

Run:

```bash
make generate && make fmt
```

Expected: both commands exit 0 and generated templ output is current.

- [ ] **Step 5: Run the required build and test suites**

Run:

```bash
make build && make test
```

Expected: both commands exit 0. If native PAM or systemd dependencies are unavailable, run `make docker-build && make docker-test` instead and require both targets to pass.

- [ ] **Step 6: Run the required linter**

Run:

```bash
make lint
```

Expected: exit 0. If native dependencies are unavailable, run `make docker-lint` and require it to pass.

- [ ] **Step 7: Inspect the final worktree**

Run:

```bash
git status --short && git diff --check && git diff --stat
```

Expected: only `README.md` and `yeti/OVERVIEW.md` remain uncommitted after Task 1; no whitespace errors or unrelated files appear. Generated templ files remain ignored and must not be hand-edited.

- [ ] **Step 8: Commit the documentation**

```bash
git add README.md yeti/OVERVIEW.md
git commit -m "docs: explain storage snapshot anchors"
```
