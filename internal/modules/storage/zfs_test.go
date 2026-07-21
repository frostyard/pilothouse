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
	result, err := zfsResult([]zfsPool{{name: "tank", size: 100, alloc: 40, free: 60, health: "ONLINE"}}, nil, []zfsDataset{{name: "tank", kind: "filesystem", mountpoint: "/tank"}, {name: "tank/home", kind: "filesystem", mountpoint: "/home"}})
	require.NoError(t, err)
	require.Equal(t, uint64(100), result.Mounts[0].TotalBytes)

	snapshot, err := normalize(time.Time{}, []collectedResult{{name: "zfs", result: result}})

	require.NoError(t, err)
	require.Len(t, snapshot.Mounts, 2)
	assert.Equal(t, uint64(100), snapshot.Summary.UsableBytes)
	assert.Equal(t, uint64(40), snapshot.Summary.UsedBytes)
	assert.Equal(t, uint64(60), snapshot.Summary.FreeBytes)
}

func TestParseZFSRejectsMalformedAndUnknownPoolData(t *testing.T) {
	_, err := parseZFSPools([]byte("tank\t1\tbad\t0\t0%\tONLINE"))
	assert.Error(t, err)
	_, err = zfsResult([]zfsPool{{name: "tank", size: 1, alloc: 0, free: 1, cap: 0, health: "ONLINE"}}, nil, []zfsDataset{{name: "other/home", kind: "filesystem", used: 0, available: 1, refer: 0, mountpoint: "/home"}})
	assert.Error(t, err)
}
