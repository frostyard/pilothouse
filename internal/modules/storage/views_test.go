package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummaryCardRendersStorageSummary(t *testing.T) {
	snapshot := Snapshot{Summary: Summary{ActiveMounts: 3, UsableBytes: 10 * 1024 * 1024 * 1024, UsedBytes: 4 * 1024 * 1024 * 1024, HighestHealth: HealthWarning}}
	var output strings.Builder
	require.NoError(t, SummaryCard(snapshot).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, "3 active mounts")
	assert.Contains(t, html, "10.0 GiB usable")
	assert.Contains(t, html, "4.0 GiB used")
	assert.Contains(t, html, "Warning")
	assert.Contains(t, html, "m9 18 6-6-6-6")
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersUnavailableState(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(Snapshot{}, true).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Storage status is unavailable.")
	assert.NotContains(t, output.String(), "@web.")
}

func TestPageEscapesMountValues(t *testing.T) {
	snapshot := Snapshot{Summary: Summary{ActiveMounts: 1}, Mounts: []Mount{{Target: "/mnt/<unsafe>", Source: "<script>alert(1)</script>", Filesystem: "ext4", State: "mounted", UsedPercent: 20}}}
	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, "/mnt/&lt;unsafe&gt;")
	assert.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
	assert.NotContains(t, html, "<script>alert(1)</script>")
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersUniqueResourceAndFindingAnchors(t *testing.T) {
	snapshot := Snapshot{
		Resources: []Resource{{ID: "disk:abc"}, {ID: "volume:one"}, {ID: "mount:disk:abc"}},
		Findings:  []Finding{{ResourceID: "disk:abc"}, {ResourceID: "alert:only"}},
		Mounts:    []Mount{{ID: "disk:abc", Target: "/data"}},
	}
	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))

	html := output.String()
	assert.Equal(t, 1, strings.Count(html, `id="disk-abc"`))
	assert.Equal(t, 1, strings.Count(html, `id="volume-one"`))
	assert.Equal(t, 1, strings.Count(html, `id="alert-only"`))
	assert.Equal(t, 1, strings.Count(html, `id="mount-disk-abc"`))
	assert.Equal(t, 1, strings.Count(html, `id="mount-disk-abc-"`))
}

func TestRenderStorageOperations(t *testing.T) {
	snapshot := Snapshot{
		Summary:  Summary{ActiveMounts: 2, UsableBytes: 100 * 1024 * 1024 * 1024, UsedBytes: 40 * 1024 * 1024 * 1024, HighestHealth: HealthWarning, UnhealthyResources: 1},
		Findings: []Finding{{ResourceID: "disk:sda", Severity: HealthWarning, Title: "Disk attention", Detail: "SMART warning"}},
		Mounts: []Mount{
			{ID: "mount:local", ResourceID: "partition:sda1", Target: "/data", Source: "/dev/sda1", Filesystem: "ext4", State: "mounted", UsedPercent: 40},
			{ID: "mount:nfs", Target: "/srv/export", Source: "server:/export", Filesystem: "nfs4", State: "mounted", UsedPercent: 20},
		},
		Resources: []Resource{
			{ID: "disk:sda", Kind: "disk", Name: "sda", SizeBytes: 100 * 1024 * 1024 * 1024, Health: HealthWarning},
			{ID: "partition:sda1", Kind: "partition", Name: "sda1", Path: "/dev/sda1", SizeBytes: 100 * 1024 * 1024 * 1024, Health: HealthHealthy},
		},
		Relations: []Relation{{From: "disk:sda", To: "partition:sda1", Kind: "contains"}},
		Backends:  []BackendStatus{{Name: "smartctl", Availability: BackendUnavailable}},
	}

	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, `id="storage-snapshot"`)
	assert.Contains(t, html, `hx-get="/storage"`)
	assert.Contains(t, html, `hx-trigger="every 30s"`)
	assert.Contains(t, html, `hx-select="#storage-snapshot"`)
	assert.Contains(t, html, `hx-target="#storage-snapshot"`)
	assert.Contains(t, html, `hx-swap="outerHTML"`)
	assert.Contains(t, html, `Mounted storage`)
	assert.Contains(t, html, `Storage topology`)
	assert.Contains(t, html, `server:/export`)
	assert.Contains(t, html, `Backend unavailable`)
	assert.Contains(t, html, `Disk attention`)
	assert.Contains(t, html, `sda`)
	assert.Contains(t, html, `sda1`)
	assert.Contains(t, html, `class="storage-snapshot"`)
	assert.Contains(t, html, `class="storage-operations"`)
	assert.Contains(t, html, `storage-topology`)
	assert.Contains(t, html, `class="storage-tree"`)
	assert.Contains(t, html, `class="storage-node"`)
	assert.Contains(t, html, `class="storage-details"`)
	assert.NotContains(t, html, `@web.`)
}

func TestRenderStorageOperationsEmpty(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(Snapshot{}, false).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, `No mounted storage was reported.`)
	assert.Contains(t, html, `No storage resources were reported.`)
	assert.Contains(t, html, `No backend status was reported.`)
}

func TestRenderStorageOperationsFullyUnavailable(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(Snapshot{}, true).Render(context.Background(), &output))

	assert.Contains(t, output.String(), `Storage status is unavailable.`)
	assert.Contains(t, output.String(), `id="storage-snapshot"`)
}

func TestRenderStorageOperationsTruncated(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(Snapshot{Truncated: true}, false).Render(context.Background(), &output))

	assert.Contains(t, output.String(), `Storage inventory was truncated.`)
}

func TestRenderStorageOperationsEscapesLabels(t *testing.T) {
	snapshot := Snapshot{Resources: []Resource{{ID: "disk:unsafe", Kind: "disk", Name: `<script>alert(1)</script>`, Details: []Detail{{Label: `<label>`, Value: `<value>`}}}}}
	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, `&lt;script&gt;alert(1)&lt;/script&gt;`)
	assert.Contains(t, html, `&lt;label&gt;`)
	assert.Contains(t, html, `&lt;value&gt;`)
	assert.NotContains(t, html, `<script>alert(1)</script>`)
}

func TestRenderStorageOperationsReadOnlyBadge(t *testing.T) {
	snapshot := Snapshot{Mounts: []Mount{{ID: "mount:readonly", Target: "/archive", Source: "/dev/sdb1", Filesystem: "ext4", State: "mounted", ReadOnly: true}}}
	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))

	assert.Contains(t, output.String(), `Read-only`)
}

func TestRenderTopologyLinksFriendlyResourceNames(t *testing.T) {
	resources := []Resource{
		{ID: "disk:sda", Name: "Primary disk"},
		{ID: "partition:sda1", Name: "<root>"},
	}
	relations := []Relation{
		{From: "disk:sda", To: "partition:sda1", Kind: "contains"},
		{From: "partition:sda1", To: "missing", Kind: "mounts"},
	}
	var output strings.Builder
	require.NoError(t, Topology(resources, relations).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, `<a href="#disk-sda">Primary disk</a>`)
	assert.Contains(t, html, `<a href="#partition-sda1">&lt;root&gt;</a>`)
	assert.Contains(t, html, `Unknown resource`)
	assert.NotContains(t, html, `href="#missing"`)
	assert.NotContains(t, html, `>disk-sda<`)
	assert.NotContains(t, html, `>partition-sda1<`)
}

func TestUsagePercentClampsToRange(t *testing.T) {
	assert.Equal(t, 0.0, usagePercent(0, 0))
	assert.Equal(t, 0.0, usagePercent(0, 1))
	assert.Equal(t, 100.0, usagePercent(2, 1))
}
