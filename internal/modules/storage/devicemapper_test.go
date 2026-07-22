package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDMInfoRejectsMalformedAndSensitiveInput(t *testing.T) {
	_, err := parseDMInfo([]byte("crypt|CRYPT-LUKS1-x|253|0|1\n"))
	assert.Error(t, err)
	_, err = parseDMInfo([]byte("crypt|CRYPT-LUKS1-x|253|0|1|1|unexpected\n"))
	assert.Error(t, err)
	assert.NotContains(t, string(mustFixture(t, "dm-info.txt")), "aes-")
	assert.NotContains(t, string(mustFixture(t, "dm-info.txt")), "key")
}

func TestDeviceMapperEnricherReportsCryptMappings(t *testing.T) {
	enricher := newDeviceMapperEnricher("dmsetup")
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		assert.Equal(t, "dmsetup", path)
		assert.Equal(t, []string{"info", "--columns", "--noheadings", "--separator", "|", "-o", "name,uuid,major,minor,open,segments"}, args)
		return mustFixture(t, "dm-info.txt"), nil
	}

	result, err := enricher.Collect(context.Background(), Inventory{Resources: []Resource{
		{ID: stableID("crypt", "253:0"), Kind: "crypt", Path: "/dev/mapper/cryptroot"},
	}})

	require.NoError(t, err)
	cryptID := stableID("crypt", "253:0")
	assert.Contains(t, result.Resources, Resource{ID: cryptID, Kind: "crypt", Name: "cryptroot", Path: "/dev/mapper/cryptroot", Health: HealthHealthy, State: "active", Details: []Detail{{Label: "Encrypted mapping", Value: "Yes"}}})
	assert.Contains(t, result.Relations, Relation{From: stableID("encryption", "CRYPT-LUKS2-11111111-2222-3333-4444-555555555555"), To: cryptID, Kind: "maps-to"})
	assert.Len(t, result.Resources, 3, "mappings discovered by dmsetup are reported once")
}

func TestMultipathEnricherReportsPathHealth(t *testing.T) {
	enricher := newMultipathEnricher("multipathd")
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		assert.Equal(t, "multipathd", path)
		if args[1] == "maps" {
			assert.Equal(t, []string{"show", "maps", "raw", "format", "%n|%w|%d|%N|%t"}, args)
			return mustFixture(t, "multipath.txt"), nil
		}
		assert.Equal(t, []string{"show", "paths", "raw", "format", "%m|%d|%t|%o"}, args)
		return mustFixture(t, "multipath-paths.txt"), nil
	}

	result, err := enricher.Collect(context.Background(), Inventory{Resources: []Resource{{ID: stableID("mpath", "253:1"), Kind: "mapping", Path: "/dev/mapper/mpatha"}}})
	require.NoError(t, err)
	assert.Contains(t, result.Findings, Finding{ResourceID: stableID("mpath", "253:1"), Severity: HealthCritical, Title: "Multipath map has failed paths", Detail: "1 of 2 paths failed"})
}

func TestMultipathEnricherCorrelatesMpathCoreResource(t *testing.T) {
	result := multipathResult([]multipathMap{{Name: "mpatha", DeclaredPaths: 2}}, []multipathPath{{Map: "mpatha", DMState: "failed", CheckerState: "faulty"}}, Inventory{Resources: []Resource{{ID: "mpath:one", Kind: "mpath", Path: "/dev/mapper/mpatha"}}})

	assert.Contains(t, result.Resources, Resource{ID: "mpath:one", Kind: "mpath", Name: "mpatha", Path: "/dev/mapper/mpatha", Health: HealthCritical, Details: []Detail{{Label: "Multipath paths", Value: "1 of 2 observed"}}})
	assert.Contains(t, result.Findings, Finding{ResourceID: "mpath:one", Severity: HealthCritical, Title: "Multipath map has failed paths", Detail: "1 of 1 paths failed"})
}

func TestMultipathEnricherMarksCountMismatchDegraded(t *testing.T) {
	result := multipathResult([]multipathMap{{Name: "mpathb", Device: "dm-2", DeclaredPaths: 2}}, []multipathPath{{Map: "mpathb", DMState: "active", CheckerState: "ready"}}, Inventory{Resources: []Resource{{ID: stableID("mapping", "253:2"), Kind: "mapping", Path: "/dev/mapper/mpathb", Details: []Detail{{Label: "MAJ:MIN", Value: "253:2"}}}}})

	assert.Contains(t, result.Resources, Resource{ID: stableID("mapping", "253:2"), Kind: "mapping", Name: "mpathb", Path: "/dev/mapper/mpathb", Health: HealthWarning, Details: []Detail{{Label: "MAJ:MIN", Value: "253:2"}, {Label: "Multipath paths", Value: "1 of 2 observed"}}})
	assert.Contains(t, result.Findings, Finding{ResourceID: stableID("mapping", "253:2"), Severity: HealthWarning, Title: "Multipath map is degraded", Detail: "1 of 2 paths observed"})
}

func TestMultipathEnricherLeavesUnknownPathStateUnknown(t *testing.T) {
	result := multipathResult([]multipathMap{{Name: "mpathc", Device: "dm-3", DeclaredPaths: 1}}, []multipathPath{{Map: "mpathc", DMState: "ghost", CheckerState: "ready"}}, Inventory{Resources: []Resource{{ID: stableID("mapping", "253:3"), Kind: "mapping", Path: "/dev/mapper/mpathc", Details: []Detail{{Label: "MAJ:MIN", Value: "253:3"}}}}})

	assert.Contains(t, result.Resources, Resource{ID: stableID("mapping", "253:3"), Kind: "mapping", Name: "mpathc", Path: "/dev/mapper/mpathc", Health: HealthUnknown, Details: []Detail{{Label: "MAJ:MIN", Value: "253:3"}, {Label: "Multipath paths", Value: "1 of 1 observed"}}})
}
