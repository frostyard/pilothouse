package storage

import (
	"context"
	"strings"
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

func TestDeviceMapperEnricherReportsCryptAndMultipathHealth(t *testing.T) {
	enricher := newDeviceMapperEnricher("dmsetup", "multipathd")
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		switch path {
		case "dmsetup":
			assert.Equal(t, []string{"info", "--columns", "--noheadings", "--separator", "|", "-o", "name,uuid,major,minor,open,segments"}, args)
			return mustFixture(t, "dm-info.txt"), nil
		case "multipathd":
			if args[1] == "maps" {
				assert.Equal(t, []string{"show", "maps", "raw", "format", "%n|%w|%d|%N|%t"}, args)
				return mustFixture(t, "multipath.txt"), nil
			}
			assert.Equal(t, []string{"show", "paths", "raw", "format", "%m|%d|%t|%o"}, args)
			return mustFixture(t, "multipath-paths.txt"), nil
		default:
			t.Fatalf("unexpected command %s", path)
			return nil, nil
		}
	}

	result, err := enricher.Collect(context.Background(), Inventory{Resources: []Resource{
		{ID: stableID("crypt", "253:0"), Kind: "crypt", Path: "/dev/mapper/cryptroot"},
		{ID: stableID("mpath", "253:1"), Kind: "mpath", Path: "/dev/mapper/mpatha"},
	}})

	require.NoError(t, err)
	cryptID := stableID("crypt", "253:0")
	assert.Contains(t, result.Resources, Resource{ID: cryptID, Kind: "crypt", Name: "cryptroot", Path: "/dev/mapper/cryptroot", Health: HealthHealthy, State: "active", Details: []Detail{{Label: "Encrypted", Value: "Yes"}}})
	assert.Contains(t, result.Relations, Relation{From: stableID("encryption", "CRYPT-LUKS2-11111111-2222-3333-4444-555555555555"), To: cryptID, Kind: "maps-to"})
	assert.Len(t, result.Resources, 3, "core mappings must not be duplicated")
	assert.Contains(t, result.Findings, Finding{ResourceID: stableID("mpath", "253:1"), Severity: HealthCritical, Title: "Multipath map has failed paths", Detail: "1 of 2 paths failed"})
}

func TestDeviceMapperEnricherMarksInactiveAndCountMismatchMultipathDegraded(t *testing.T) {
	assert.True(t, strings.HasPrefix("CRYPT-LUKS2-", cryptUUIDPrefix))
	result := deviceMapperResult([]dmInfo{{Name: "mpathb", MajorMinor: "253:2"}}, []multipathMap{{Name: "mpathb", DeclaredPaths: 2}}, []multipathPath{{Map: "mpathb", DMState: "active", CheckerState: "ready"}}, Inventory{})

	assert.Contains(t, result.Resources, Resource{ID: stableID("mapping", "253:2"), Kind: "mapping", Name: "mpathb", Path: "/dev/mapper/mpathb", Health: HealthWarning, State: "inactive"})
	assert.Contains(t, result.Findings, Finding{ResourceID: stableID("mapping", "253:2"), Severity: HealthWarning, Title: "Multipath map is degraded", Detail: "1 of 2 paths observed"})
}

func TestDeviceMapperEnricherLeavesUnknownPathStateUnknown(t *testing.T) {
	result := deviceMapperResult([]dmInfo{{Name: "mpathc", MajorMinor: "253:3", Open: 1}}, []multipathMap{{Name: "mpathc", DeclaredPaths: 1}}, []multipathPath{{Map: "mpathc", DMState: "ghost", CheckerState: "ready"}}, Inventory{})

	assert.Contains(t, result.Resources, Resource{ID: stableID("mapping", "253:3"), Kind: "mapping", Name: "mpathc", Path: "/dev/mapper/mpathc", Health: HealthUnknown, State: "active"})
}
