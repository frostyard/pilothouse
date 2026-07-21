package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLVMReportRejectsMalformedDuplicateAndOversizedData(t *testing.T) {
	_, err := parseLVMReport([]byte(`{"report":[{"pv":[{"pv_uuid":"one"},{"pv_uuid":"one"}]}]}`))
	assert.Error(t, err)
	_, err = parseLVMReport([]byte(`{"report":[{"pv":[{"pv_uuid":"` + strings.Repeat("x", maxFieldBytes+1) + `"}]}]}`))
	assert.Error(t, err)
	_, err = parseLVMReport([]byte(`{"report":[{"unknown":[]}]}`))
	assert.Error(t, err)
}

func TestLVMEnricherReportsGraphUtilizationAndPartialHealth(t *testing.T) {
	enricher := newLVMEnricher(LVMTools{PVS: "/usr/sbin/pvs", VGS: "/usr/sbin/vgs", LVS: "/usr/sbin/lvs"})
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		fields := lvmLVFields
		switch path {
		case "/usr/sbin/pvs":
			fields = lvmPVFields
		case "/usr/sbin/vgs":
			fields = lvmVGFields
		}
		assert.Equal(t, []string{"--reportformat", "json", "--units", "b", "--nosuffix", "-o", fields}, args)
		switch path {
		case "/usr/sbin/pvs":
			return []byte(`{"report":[{"pv":[{"pv_uuid":"pv-one","vg_uuid":"vg-one","pv_name":"/dev/sda2","pv_size":"1073741824","pv_free":"0","pv_attr":"a--"},{"pv_uuid":"pv-two","vg_uuid":"vg-one","pv_name":"/dev/sdb2","pv_size":"1073741824","pv_free":"0","pv_attr":"a-m"}]}]}`), nil
		case "/usr/sbin/vgs":
			return []byte(`{"report":[{"vg":[{"vg_uuid":"vg-one","vg_name":"data","vg_size":"2147483648","vg_free":"536870912","vg_attr":"wz--np"}]}]}`), nil
		default:
			return mustFixture(t, "lvm.json"), nil
		}
	}

	result, err := enricher.Collect(context.Background(), Inventory{Resources: []Resource{
		{ID: stableID("partition", "8:2"), Path: "/dev/sda2"},
		{ID: stableID("partition", "8:18"), Path: "/dev/sdb2"},
	}})

	require.NoError(t, err)
	vgID := stableID("lvm-vg", "vg-one")
	lvID := stableID("lvm-lv", "lv-one")
	assert.Contains(t, result.Relations, Relation{From: stableID("partition", "8:2"), To: vgID, Kind: "member-of"})
	assert.Contains(t, result.Relations, Relation{From: vgID, To: lvID, Kind: "contains"})
	assert.Contains(t, result.Resources, Resource{ID: lvID, Kind: "logical-volume", Name: "data/root", Path: "/dev/data/root", SizeBytes: 1610612736, Health: HealthHealthy, State: "available", Details: []Detail{{Label: "Data utilization", Value: "75.0%"}, {Label: "Metadata utilization", Value: "25.0%"}}})
	assert.Contains(t, result.Findings, Finding{ResourceID: vgID, Severity: HealthCritical, Title: "LVM volume group has missing devices", Detail: "1 physical volume is missing"})
}

func TestLVMEnricherAcceptsEmptyHostReports(t *testing.T) {
	enricher := newLVMEnricher(LVMTools{PVS: "pvs", VGS: "vgs", LVS: "lvs"})
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"report":[{}]}`), nil }

	result, err := enricher.Collect(context.Background(), Inventory{})

	require.NoError(t, err)
	assert.Empty(t, result.Resources)
}

func TestLVMResultSkipsUnknownVolumeGroupReferences(t *testing.T) {
	result, err := lvmResult(lvmReport{}, lvmReport{}, lvmReport{LVs: []lvmLV{{UUID: "lv-one", VGUUID: "missing", Name: "root", VGName: "data", Path: "/dev/data/root", Size: "1", Attr: "-wi-a-----"}}}, Inventory{})

	require.NoError(t, err)
	assert.Empty(t, result.Resources)
}
