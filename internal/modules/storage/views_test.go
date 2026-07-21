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
