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

func TestRenderAdvancedStorageDetails(t *testing.T) {
	smart, err := parseSMART(mustFixture(t, "smart-nvme.json"), "/dev/nvme0n1")
	require.NoError(t, err)
	mdraid := newMDRAIDEnricher("/fixture", "mdadm")
	mdraid.readFile = func(string) ([]byte, error) { return mustFixture(t, "mdstat-degraded.txt"), nil }
	mdraid.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		return mustFixture(t, "mdadm-detail.txt"), nil
	}
	raid, err := mdraid.Collect(context.Background(), Inventory{})
	require.NoError(t, err)
	deviceMapper := deviceMapperResult([]dmInfo{{Name: "crypt", UUID: "CRYPT-LUKS2-test", MajorMinor: "253:0", Open: 1}}, Inventory{})
	multipath := multipathResult([]multipathMap{{Name: "mpatha", DeclaredPaths: 1}}, []multipathPath{{Map: "mpatha", DMState: "active", CheckerState: "ready"}}, Inventory{Resources: []Resource{{ID: "mpath", Kind: "mpath", Path: "/dev/mapper/mpatha"}}})
	zfs, err := zfsResult([]zfsPool{{name: "tank", size: 1, health: "ONLINE"}}, map[string]zfsStatus{"tank": {state: "ONLINE"}}, nil, Inventory{})
	require.NoError(t, err)
	btrfs, err := btrfsResult([]btrfsFilesystem{{uuid: "fs", size: 1, devices: []string{"/dev/sda"}, errors: map[string]uint64{"/dev/sda": 2}}}, Inventory{})
	require.NoError(t, err)
	lvm, err := lvmResult(lvmReport{}, lvmReport{VGs: []lvmVG{{UUID: "vg", Name: "data", Size: "1", Free: "0"}}}, lvmReport{LVs: []lvmLV{{UUID: "lv", VGUUID: "vg", Name: "root", VGName: "data", Path: "/dev/data/root", Size: "1", Attr: "----a", Data: "20"}}}, Inventory{})
	require.NoError(t, err)
	stale := AdapterResult{Resources: []Resource{{ID: "stale"}}}
	markStale(&stale)
	resources := append([]Resource{smart}, raid.Resources...)
	resources = append(resources, deviceMapper.Resources...)
	resources = append(resources, multipath.Resources...)
	resources = append(resources, zfs.Resources...)
	resources = append(resources, btrfs.Resources...)
	resources = append(resources, lvm.Resources...)
	resources = append(resources, stale.Resources...)
	snapshot := Snapshot{Resources: resources, Backends: []BackendStatus{{Name: "smart", Availability: BackendUnsupported}}}
	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))

	html := output.String()
	for _, label := range []string{"Temperature", "Percentage used", "RAID members", "Recovery progress", "LVM data usage", "Encrypted mapping", "Multipath paths", "ZFS pool health", "Btrfs device errors", "Health data: Stale", "Backend unsupported"} {
		assert.Contains(t, html, label)
	}
	assert.NotContains(t, html, "@web.")
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

func TestRemoteMountFormRendersAllowlistedFieldsAndEscapesValues(t *testing.T) {
	var output strings.Builder
	require.NoError(t, RemoteMountForm("smb-credentials", "csrf-token").Render(context.Background(), &output))

	html := output.String()
	for _, value := range []string{`action="/storage/mounts"`, `name="csrf"`, `value="csrf-token"`, `name="server"`, `name="share"`, `name="username"`, `name="password"`, `name="target"`, `name="version"`, `name="read_only"`, `value="2.1"`, `value="3.0"`, `value="3.1.1"`} {
		assert.Contains(t, html, value)
	}
	assert.NotContains(t, html, `name="options"`)
	assert.NotContains(t, html, `credential-path`)
	assert.NotContains(t, html, `name="unit"`)
	assert.NotContains(t, html, `@web.`)
}

func TestRemoteMountFormsRenderExactProtocolFields(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol string
		present  []string
		absent   []string
	}{
		{"nfs", "nfs", []string{`name="host"`, `name="export"`, `value="3"`, `value="4"`, `value="4.1"`, `value="4.2"`}, []string{`name="server"`, `name="username"`, `name="password"`}},
		{"smb guest", "smb-guest", []string{`name="server"`, `name="share"`, `value="2.1"`, `value="3.0"`, `value="3.1.1"`, `protocol=smb-credentials`}, []string{`name="host"`, `name="export"`, `name="username"`, `name="password"`}},
		{"smb credentials", "smb-credentials", []string{`name="server"`, `name="share"`, `name="username"`, `name="password"`, `protocol=smb-guest`}, []string{`name="host"`, `name="export"`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			require.NoError(t, RemoteMountForm(test.protocol, "csrf-token").Render(context.Background(), &output))
			for _, value := range test.present {
				assert.Contains(t, output.String(), value)
			}
			for _, value := range test.absent {
				assert.NotContains(t, output.String(), value)
			}
			for _, value := range []string{`name="options"`, `credential-path`, `name="unit"`, `name="executable"`, `@web.`} {
				assert.NotContains(t, output.String(), value)
			}
		})
	}
}

func TestManagedPageShowsControlsOnlyForAdminManagedMounts(t *testing.T) {
	snapshot := Snapshot{Mounts: []Mount{{ID: "remote:0123456789abcdef0123456789abcdef", Managed: true, Target: "/managed"}, {ID: "local:unmanaged", Target: "/local"}}}
	for _, test := range []struct {
		name  string
		admin bool
		want  bool
	}{{"administrator", true, true}, {"non-administrator", false, false}} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			require.NoError(t, ManagedPage(snapshot, false, "csrf-token", test.admin).Render(context.Background(), &output))
			for _, control := range []string{"Add remote mount", `>Mount</button>`, `>Unmount</button>`, `>Delete</button>`} {
				if test.want {
					assert.Contains(t, output.String(), control)
				} else {
					assert.NotContains(t, output.String(), control)
				}
			}
			if test.want {
				assert.Contains(t, output.String(), `/storage/mounts/0123456789abcdef0123456789abcdef/delete`)
			}
			assert.NotContains(t, output.String(), `local:unmanaged/delete`)
		})
	}
}

func TestManagedPageRendersLifecycleControlsForMountState(t *testing.T) {
	id := "remote:0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		state   string
		mount   bool
		unmount bool
	}{
		{"mounted", false, true},
		{"inactive", true, false},
		{"needs-attention", false, false},
	} {
		t.Run(test.state, func(t *testing.T) {
			var output strings.Builder
			require.NoError(t, ManagedPage(Snapshot{Mounts: []Mount{{ID: id, Managed: true, State: test.state}}}, false, "csrf", true).Render(context.Background(), &output))
			assert.Equal(t, test.mount, strings.Contains(output.String(), `>Mount</button>`))
			assert.Equal(t, test.unmount, strings.Contains(output.String(), `>Unmount</button>`))
			assert.Contains(t, output.String(), `>Delete</button>`)
		})
	}
}

func TestManagedMountPathFailsClosedForMalformedIDs(t *testing.T) {
	assert.Equal(t, "0123456789abcdef0123456789abcdef", managedMountID("remote:0123456789abcdef0123456789abcdef"))
	assert.Empty(t, managedMountID("malformed"))
}

func TestRemoteMountFormEscapesCSRFToken(t *testing.T) {
	var output strings.Builder
	require.NoError(t, RemoteMountForm("nfs", `"><script>alert(1)</script>`).Render(context.Background(), &output))
	assert.Contains(t, output.String(), `value="&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`)
	assert.NotContains(t, output.String(), `<script>alert(1)</script>`)
}
