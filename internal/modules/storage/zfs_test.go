package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZFSEnricherReportsPoolsDatasetsAndHealth(t *testing.T) {
	enricher := newZFSEnricher(ZFSTools{ZPool: "zpool", ZFS: "zfs"})
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		switch {
		case path == "zpool" && strings.Join(args, " ") == "list -Hp -o name,size,alloc,free,cap,health":
			return mustFixture(t, "zpool-list.txt"), nil
		case path == "zpool" && strings.Join(args, " ") == "status -P":
			return mustFixture(t, "zpool-status.txt"), nil
		case path == "zfs" && strings.Join(args, " ") == "list -Hp -o name,type,used,available,refer,mountpoint":
			return mustFixture(t, "zfs-list.txt"), nil
		default:
			t.Fatalf("unexpected command %s %q", path, args)
			return nil, nil
		}
	}

	result, err := enricher.Collect(context.Background(), Inventory{})

	require.NoError(t, err)
	poolID := stableID("zfs-pool", "tank")
	datasetID := stableID("zfs-dataset", "tank/home")
	assert.Contains(t, result.Relations, Relation{From: poolID, To: datasetID, Kind: "contains"})
	assert.Contains(t, result.Resources, Resource{ID: poolID, Kind: "zfs-pool", Name: "tank", SizeBytes: 1073741824, Health: HealthCritical, State: "degraded", Details: []Detail{{Label: "Allocated", Value: "536870912"}, {Label: "Free", Value: "536870912"}, {Label: "Capacity", Value: "50%"}, {Label: "Read errors", Value: "1"}, {Label: "Write errors", Value: "0"}, {Label: "Checksum errors", Value: "2"}}})
	assert.Contains(t, result.Findings, Finding{ResourceID: poolID, Severity: HealthCritical, Title: "ZFS pool is degraded", Detail: "one or more devices are unavailable"})
	assert.Len(t, result.Mounts, 3)
	assert.Equal(t, poolID, result.Mounts[0].ResourceID)
}

func TestZFSPoolOwnsAggregateCapacity(t *testing.T) {
	result, err := zfsResult([]zfsPool{{name: "tank", size: 100, alloc: 40, free: 60, health: "ONLINE"}}, nil, []zfsDataset{{name: "tank", kind: "filesystem", mountpoint: "/tank"}, {name: "tank/home", kind: "filesystem", mountpoint: "/home"}}, Inventory{})
	require.NoError(t, err)
	require.Equal(t, uint64(100), result.Mounts[0].TotalBytes)

	snapshot, err := normalize(time.Time{}, []collectedResult{{name: "zfs", result: result}})

	require.NoError(t, err)
	require.Len(t, snapshot.Mounts, 2)
	assert.Equal(t, uint64(100), snapshot.Summary.UsableBytes)
	assert.Equal(t, uint64(40), snapshot.Summary.UsedBytes)
	assert.Equal(t, uint64(60), snapshot.Summary.FreeBytes)
}

func TestParseZFSStatusCountsOnlyAbsolutePathLeaves(t *testing.T) {
	statuses, err := parseZFSStatus(mustFixture(t, "zpool-status-multivdev.txt"))

	require.NoError(t, err)
	assert.Equal(t, zfsStatus{state: "DEGRADED", read: 3, write: 5, checksum: 7}, statuses["tank"])
}

func TestParseZFSPoolsAcceptsBareAndPercentCapacity(t *testing.T) {
	pools, err := parseZFSPools([]byte("tank\t100\t40\t60\t40\tONLINE\nbackup\t100\t40\t60\t40%\tONLINE"))

	require.NoError(t, err)
	assert.Equal(t, []uint64{40, 40}, []uint64{pools[0].cap, pools[1].cap})
}

func TestZFSEnricherReplacesCoreMountAndUsesPoolCapacity(t *testing.T) {
	enricher := newZFSEnricher(ZFSTools{ZPool: "zpool", ZFS: "zfs"})
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		switch path {
		case "zpool":
			if args[0] == "list" {
				return []byte("tank\t100\t95\t5\t95\tONLINE"), nil
			}
			return []byte("pool: tank\n state: ONLINE\nconfig:\n\n\tNAME STATE READ WRITE CKSUM\n\ttank ONLINE 0 0 0\n\t/dev/sda ONLINE 0 0 0"), nil
		case "zfs":
			return []byte("tank/home\tfilesystem\t95\t5\t95\t/home"), nil
		}
		t.Fatalf("unexpected command %s %q", path, args)
		return nil, nil
	}
	core := AdapterResult{Resources: []Resource{{ID: "core-device"}}, Mounts: []Mount{{ID: "core-home", Target: "/home", Source: "tank/home", Filesystem: "zfs", ResourceID: "core-device", State: "mounted", TotalBytes: 1}}}
	inventory := Inventory{Mounts: core.Mounts}

	result, err := enricher.Collect(context.Background(), inventory)
	require.NoError(t, err)
	require.Len(t, result.Mounts, 1)
	assert.Equal(t, "core-home", result.Mounts[0].ID)
	assert.Equal(t, float64(95), result.Mounts[0].UsedPercent)

	snapshot, err := normalize(time.Time{}, []collectedResult{{name: "core", core: true, result: core}, {name: "zfs", result: result}})
	require.NoError(t, err)
	require.Len(t, snapshot.Mounts, 1)
	assert.Equal(t, stableID("zfs-pool", "tank"), snapshot.Mounts[0].ResourceID)
	assert.Equal(t, uint64(100), snapshot.Summary.UsableBytes)
	assert.Contains(t, findingTitles(snapshot.Findings), "Mount capacity is critical")
}

func TestParseZFSRejectsMalformedAndUnknownPoolData(t *testing.T) {
	_, err := parseZFSPools([]byte("tank\t1\tbad\t0\t0%\tONLINE"))
	assert.Error(t, err)
	_, err = zfsResult([]zfsPool{{name: "tank", size: 1, alloc: 0, free: 1, cap: 0, health: "ONLINE"}}, nil, []zfsDataset{{name: "other/home", kind: "filesystem", used: 0, available: 1, refer: 0, mountpoint: "/home"}}, Inventory{})
	assert.Error(t, err)
}
