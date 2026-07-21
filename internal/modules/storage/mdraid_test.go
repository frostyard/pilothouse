package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMDStatHealthyArray(t *testing.T) {
	arrays, err := parseMDStat(mustFixture(t, "mdstat-healthy.txt"))

	require.NoError(t, err)
	require.Equal(t, []mdArray{{name: "md0", level: "raid1", expected: 2, active: 2, members: []string{"sda1", "sdb1"}, state: "available"}}, arrays)
}

func TestParseMDStatDegradedArrayIncludesRecovery(t *testing.T) {
	arrays, err := parseMDStat(mustFixture(t, "mdstat-degraded.txt"))

	require.NoError(t, err)
	require.Len(t, arrays, 1)
	assert.Equal(t, 2, arrays[0].expected)
	assert.Equal(t, 1, arrays[0].active)
	assert.Equal(t, 5.0, arrays[0].recovery)
}

func TestParseMDStatRetainsValidMixedStateArrays(t *testing.T) {
	arrays, err := parseMDStat(mustFixture(t, "mdstat-mixed.txt"))

	require.NoError(t, err)
	require.Len(t, arrays, 3)
	assert.Equal(t, "available", arrays[0].state)
	assert.Equal(t, "inactive", arrays[1].state)
	assert.Equal(t, "auto-read-only", arrays[2].state)
}

func TestParseMDStatRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"oversized line", []byte(strings.Repeat("a", maxFieldBytes+1) + "\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseMDStat(test.input)
			assert.Error(t, err)
		})
	}
}

func TestParseMDStatSkipsMalformedAndDuplicateArrays(t *testing.T) {
	arrays, err := parseMDStat([]byte("md0 : active raid1 sda1[0]\n      1 blocks [1/1] [U]\nmd1 : active raid1 bad[not-a-number]\n      1 blocks [1/1] [U]\nmd0 : active raid1 sdb1[0]\n      1 blocks [1/1] [U]\n"))

	require.NoError(t, err)
	assert.Equal(t, []mdArray{{name: "md0", level: "raid1", expected: 1, active: 1, members: []string{"sda1"}, state: "available"}}, arrays)
}

func TestParseMDStatRejectsTooManyArrays(t *testing.T) {
	var input strings.Builder
	for index := range maxResources + 1 {
		input.WriteString("md")
		input.WriteString(strconv.Itoa(index))
		input.WriteString(" : active raid1 sda1[0]\n      1 blocks [1/1] [U]\n")
	}

	_, err := parseMDStat([]byte(input.String()))

	assert.Error(t, err)
}

func TestMDRAIDEnricherReportsTopologyAndHealth(t *testing.T) {
	enricher := newMDRAIDEnricher("/fixture", "/usr/sbin/mdadm")
	enricher.readFile = func(path string) ([]byte, error) {
		assert.Equal(t, "/fixture/proc/mdstat", path)
		return mustFixture(t, "mdstat-degraded.txt"), nil
	}
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		assert.Equal(t, "/usr/sbin/mdadm", path)
		assert.Equal(t, []string{"--detail", "--export", "/dev/md0"}, args)
		return mustFixture(t, "mdadm-detail.txt"), nil
	}

	result, err := enricher.Collect(context.Background(), Inventory{Resources: []Resource{
		{ID: stableID("disk", "8:1"), Kind: "disk", Path: "/dev/sda1"},
		{ID: stableID("disk", "8:2"), Kind: "disk", Path: "/dev/sdb1"},
	}})

	require.NoError(t, err)
	raidID := stableID("raid", "md0")
	assert.Contains(t, result.Relations, Relation{From: stableID("disk", "8:1"), To: raidID, Kind: "member-of"})
	assert.Contains(t, result.Relations, Relation{From: stableID("disk", "8:2"), To: raidID, Kind: "member-of"})
	assert.Contains(t, result.Findings, Finding{ResourceID: raidID, Severity: HealthCritical, Title: "RAID array is degraded", Detail: "1 of 2 members active"})
	assert.Contains(t, result.Resources, Resource{ID: raidID, Kind: "raid", Name: "md0", Path: "/dev/md0", Health: HealthCritical, State: "degraded", Details: []Detail{{Label: "Level", Value: "raid1"}, {Label: "Members", Value: "1 of 2 active"}, {Label: "Recovery", Value: "5.0%"}}})
}

func TestMDRAIDEnricherRejectsMismatchedDetailArray(t *testing.T) {
	enricher := newMDRAIDEnricher("/fixture", "/usr/sbin/mdadm")
	enricher.readFile = func(string) ([]byte, error) { return mustFixture(t, "mdstat-healthy.txt"), nil }
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("MD_DEVNAME=md1\nMD_LEVEL=raid1\nMD_DEVICES=2"), nil
	}

	_, err := enricher.Collect(context.Background(), Inventory{})

	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrBackendUnsupported))
}

func TestMDRAIDEnricherUsesDetailMembersForRelations(t *testing.T) {
	enricher := newMDRAIDEnricher("/fixture", "/usr/sbin/mdadm")
	enricher.readFile = func(string) ([]byte, error) { return mustFixture(t, "mdstat-healthy.txt"), nil }
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("MD_DEVNAME=md0\nMD_LEVEL=raid1\nMD_DEVICES=2\nMD_DEVICE_sdz1_DEV=/dev/sdz1"), nil
	}

	result, err := enricher.Collect(context.Background(), Inventory{Resources: []Resource{{ID: stableID("disk", "8:1"), Kind: "disk", Path: "/dev/sda1"}}})

	require.NoError(t, err)
	assert.Empty(t, result.Relations)
}

func TestMDRAIDEnricherReusesCoreArrayIdentity(t *testing.T) {
	enricher := newMDRAIDEnricher("/fixture", "/usr/sbin/mdadm")
	enricher.readFile = func(string) ([]byte, error) { return mustFixture(t, "mdstat-degraded.txt"), nil }
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		return mustFixture(t, "mdadm-detail.txt"), nil
	}
	raidID := stableID("mapping", "9:0")

	result, err := enricher.Collect(context.Background(), Inventory{Resources: []Resource{
		{ID: stableID("disk", "8:1"), Kind: "disk", Path: "/dev/sda1"},
		{ID: raidID, Kind: "mapping", Path: "/dev/md0"},
	}})

	require.NoError(t, err)
	assert.Equal(t, raidID, result.Resources[0].ID)
	assert.Contains(t, result.Relations, Relation{From: stableID("disk", "8:1"), To: raidID, Kind: "member-of"})
	assert.Contains(t, result.Findings, Finding{ResourceID: raidID, Severity: HealthCritical, Title: "RAID array is degraded", Detail: "1 of 2 members active"})
}

func TestMDRAIDEnricherMarksInactiveAndAutoReadOnlyArraysUnhealthy(t *testing.T) {
	enricher := newMDRAIDEnricher("/fixture", "/usr/sbin/mdadm")
	enricher.readFile = func(string) ([]byte, error) { return mustFixture(t, "mdstat-mixed.txt"), nil }
	enricher.runner.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		name := strings.TrimPrefix(args[2], "/dev/")
		return []byte("MD_DEVNAME=" + name + "\nMD_LEVEL=raid1\nMD_DEVICES=2"), nil
	}

	result, err := enricher.Collect(context.Background(), Inventory{})

	require.NoError(t, err)
	assert.Contains(t, result.Resources, Resource{ID: stableID("raid", "md1"), Kind: "raid", Name: "md1", Path: "/dev/md1", Health: HealthUnknown, State: "inactive", Details: []Detail{{Label: "Level", Value: "raid1"}, {Label: "Members", Value: "0 of 2 active"}}})
	assert.Contains(t, result.Resources, Resource{ID: stableID("raid", "md2"), Kind: "raid", Name: "md2", Path: "/dev/md2", Health: HealthWarning, State: "auto-read-only", Details: []Detail{{Label: "Level", Value: "raid1"}, {Label: "Members", Value: "2 of 2 active"}}})
}

func TestMDRAIDEnricherBoundsMDStatRead(t *testing.T) {
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	require.NoError(t, os.Mkdir(proc, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proc, "mdstat"), []byte(strings.Repeat("x", maxAdapterBytes+1)), 0o644))

	_, err := NewMDRAIDEnricher(root, "/usr/sbin/mdadm").Collect(context.Background(), Inventory{})

	assert.ErrorIs(t, err, errOutputTooLarge)
}
