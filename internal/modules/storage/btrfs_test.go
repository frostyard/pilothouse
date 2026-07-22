package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBtrfsEnricherReportsFilesystemDevicesSubvolumesAndErrors(t *testing.T) {
	enricher := newBtrfsEnricher("btrfs")
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		require.Equal(t, "btrfs", path)
		switch strings.Join(args, " ") {
		case "filesystem usage -b --raw /data":
			return mustFixture(t, "btrfs-usage.txt"), nil
		case "device stats /data":
			return mustFixture(t, "btrfs-stats.txt"), nil
		case "subvolume list -o /data":
			return []byte("ID 256 gen 10 top level 5 path home\nID 257 gen 11 top level 256 path home/user"), nil
		default:
			t.Fatalf("unexpected btrfs arguments %q", args)
			return nil, nil
		}
	}
	inventory := Inventory{Mounts: []Mount{{ID: "mount-data", Target: "/data", Source: "/dev/sda", Filesystem: "btrfs", State: "mounted", TotalBytes: 1000, UsedBytes: 400, AvailableBytes: 600}}, Resources: []Resource{{ID: "device-data", Path: "/dev/sda", Details: []Detail{{Label: "UUID", Value: "fs-one"}}}}}

	result, err := enricher.Collect(context.Background(), inventory)

	require.NoError(t, err)
	fsID := stableID("btrfs-filesystem", "fs-one")
	assert.Contains(t, result.Resources, Resource{ID: fsID, Kind: "btrfs-filesystem", Name: "fs-one", SizeBytes: 1073741824, Health: HealthWarning, State: "mounted"})
	assert.Contains(t, result.Resources, Resource{ID: stableID("btrfs-device", "fs-one:/dev/sdb"), Kind: "btrfs-device", Name: "/dev/sdb", Path: "/dev/sdb", Health: HealthWarning, State: "available", Details: []Detail{{Label: "Btrfs device errors", Value: "2"}}})
	assert.Contains(t, result.Relations, Relation{From: fsID, To: stableID("btrfs-subvolume", "fs-one:256"), Kind: "contains"})
	require.Len(t, result.Mounts, 1)
	assert.Equal(t, fsID, result.Mounts[0].ResourceID)
	assert.Contains(t, result.Findings, Finding{ResourceID: fsID, Severity: HealthWarning, Title: "Btrfs device reports errors", Detail: "/dev/sdb has 2 errors"})
}

func TestBtrfsEnricherHandlesSubvolumeMountsOfOneFilesystem(t *testing.T) {
	enricher := newBtrfsEnricher("btrfs")
	var usageCalls int
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		require.Equal(t, "btrfs", path)
		switch strings.Join(args, " ") {
		case "filesystem usage -b --raw /":
			usageCalls++
			return []byte("Overall:\n    Device size:                 1073741824\n    Device missing:                       0\n\n   /dev/sda\t1073741824"), nil
		case "device stats /":
			return []byte("[/dev/sda].write_io_errs 0"), nil
		case "subvolume list -o /":
			return nil, nil
		default:
			t.Fatalf("unexpected btrfs arguments %q", args)
			return nil, nil
		}
	}
	inventory := Inventory{Mounts: []Mount{
		{ID: "mount-root", Target: "/", Source: "/dev/sda[/@]", Filesystem: "btrfs", State: "mounted"},
		{ID: "mount-home", Target: "/home", Source: "/dev/sda[/@home]", Filesystem: "btrfs", State: "mounted"},
	}, Resources: []Resource{{ID: "device-root", Path: "/dev/sda", Details: []Detail{{Label: "UUID", Value: "fs-one"}}}}}

	result, err := enricher.Collect(context.Background(), inventory)

	require.NoError(t, err)
	assert.Equal(t, 1, usageCalls)
	fsID := stableID("btrfs-filesystem", "fs-one")
	assert.Contains(t, result.Resources, Resource{ID: fsID, Kind: "btrfs-filesystem", Name: "fs-one", SizeBytes: 1073741824, Health: HealthHealthy, State: "mounted"})
	require.Len(t, result.Mounts, 2)
	for _, mount := range result.Mounts {
		assert.Equal(t, fsID, mount.ResourceID)
	}
}

func TestParseBtrfsSubvolumesAcceptsEmptyOutput(t *testing.T) {
	subvolumes, err := parseBtrfsSubvolumes(nil)

	require.NoError(t, err)
	assert.Empty(t, subvolumes)
}

func TestParseBtrfsRejectsDuplicateFilesystemAndOversizedOutput(t *testing.T) {
	_, err := btrfsResult([]btrfsFilesystem{{uuid: "same"}, {uuid: "same"}}, Inventory{})
	assert.Error(t, err)
	_, err = parseBtrfsUsage([]byte(strings.Repeat("x", maxAdapterBytes+1)), "fs-one")
	assert.Error(t, err)
}

func TestBtrfsResultMarksMissingDevicesCritical(t *testing.T) {
	result, err := btrfsResult([]btrfsFilesystem{{uuid: "fs-one", size: 1, missing: 1}}, Inventory{})

	require.NoError(t, err)
	assert.Equal(t, HealthCritical, result.Resources[0].Health)
	assert.Contains(t, result.Findings, Finding{ResourceID: stableID("btrfs-filesystem", "fs-one"), Severity: HealthCritical, Title: "Btrfs filesystem has missing devices", Detail: "1 devices missing"})
}

func TestBtrfsMountAttachmentReplacesCoreMountCapacity(t *testing.T) {
	core := AdapterResult{Mounts: []Mount{{ID: "mount-data", Target: "/data", Source: "/dev/sda", Filesystem: "btrfs", State: "mounted", ResourceID: "device-data", TotalBytes: 1000, UsedBytes: 400, AvailableBytes: 600}}, Resources: []Resource{{ID: "device-data", Health: HealthHealthy}}}
	enriched := AdapterResult{Mounts: []Mount{{ID: "mount-data", Target: "/data", Source: "/dev/sda", Filesystem: "btrfs", State: "mounted", ResourceID: "fs-data", TotalBytes: 1000, UsedBytes: 400, AvailableBytes: 600}}, Resources: []Resource{{ID: "fs-data", Health: HealthHealthy}}}

	snapshot, err := normalize(time.Time{}, []collectedResult{{name: "core", core: true, result: core}, {name: "btrfs", result: enriched}})

	require.NoError(t, err)
	assert.Len(t, snapshot.Mounts, 1)
	assert.Equal(t, "fs-data", snapshot.Mounts[0].ResourceID)
	assert.Equal(t, uint64(1000), snapshot.Summary.UsableBytes)
}
