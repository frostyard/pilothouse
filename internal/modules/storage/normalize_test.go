package storage

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRejectsCycles(t *testing.T) {
	_, err := normalize(time.Unix(1, 0), []collectedResult{{name: "block", core: true, result: AdapterResult{
		Resources: []Resource{{ID: "a"}, {ID: "b"}},
		Relations: []Relation{{From: "a", To: "b", Kind: "contains"}, {From: "b", To: "a", Kind: "contains"}},
	}}})
	assert.ErrorContains(t, err, "cycle")
}

func TestNormalizeCountsFilesystemCapacityOnce(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{name: "mount", core: true, result: AdapterResult{Mounts: []Mount{
		{ID: "root", ResourceID: "fs:one", TotalBytes: 100, UsedBytes: 60, AvailableBytes: 40, State: "mounted"},
		{ID: "bind", ResourceID: "fs:one", TotalBytes: 100, UsedBytes: 60, AvailableBytes: 40, State: "mounted"},
	}}}})
	require.NoError(t, err)
	assert.Equal(t, uint64(100), snapshot.Summary.UsableBytes)
	assert.Equal(t, uint64(60), snapshot.Summary.UsedBytes)
	assert.Equal(t, uint64(40), snapshot.Summary.FreeBytes)
}

func TestNormalizeSortsSnapshotDeterministically(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{
		{name: "zeta", result: AdapterResult{Resources: []Resource{{ID: "c", Kind: "disk", Name: "z"}, {ID: "b", Kind: "disk", Name: "b"}, {ID: "a", Kind: "disk", Name: "a"}}, Mounts: []Mount{{ID: "z", Target: "/z", Source: "z"}, {ID: "a", Target: "/a", Source: "a"}}}},
		{name: "alpha", result: AdapterResult{Relations: []Relation{{From: "b", To: "c", Kind: "z"}, {From: "a", To: "b", Kind: "z"}}, Findings: []Finding{{ResourceID: "a", Severity: HealthWarning}, {ResourceID: "b", Severity: HealthCritical}}}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, resourceIDs(snapshot.Resources))
	assert.Equal(t, []string{"a", "b"}, relationFromIDs(snapshot.Relations))
	assert.Equal(t, []string{"/a", "/z"}, mountTargets(snapshot.Mounts))
	assert.Equal(t, []string{"b", "a"}, findingResourceIDs(snapshot.Findings))
	assert.Equal(t, []string{"alpha", "zeta"}, backendNames(snapshot.Backends))
}

func TestNormalizeRejectsMissingRelationEndpoint(t *testing.T) {
	_, err := normalize(time.Unix(1, 0), []collectedResult{{core: true, result: AdapterResult{Resources: []Resource{{ID: "a"}}, Relations: []Relation{{From: "a", To: "missing"}}}}})
	assert.ErrorContains(t, err, "missing endpoint")
}

func TestNormalizeRejectsDepthOverLimit(t *testing.T) {
	resources := make([]Resource, maxGraphDepth+2)
	relations := make([]Relation, maxGraphDepth+1)
	for i := range resources {
		resources[i].ID = string(rune('a' + i))
		if i > 0 {
			relations[i-1] = Relation{From: resources[i-1].ID, To: resources[i].ID}
		}
	}
	_, err := normalize(time.Unix(1, 0), []collectedResult{{core: true, result: AdapterResult{Resources: resources, Relations: relations}}})
	assert.ErrorContains(t, err, "depth")
}

func TestNormalizeCreatesMountFindings(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{result: AdapterResult{Mounts: []Mount{
		{ID: "warning", ResourceID: "warning", UsedPercent: 80, State: "mounted"},
		{ID: "critical", ResourceID: "critical", UsedPercent: 90, State: "mounted"},
		{ID: "readonly", ResourceID: "readonly", ReadOnly: true, State: "mounted"},
	}}}})
	require.NoError(t, err)
	assert.Equal(t, []Health{HealthCritical, HealthWarning, HealthWarning}, findingSeverities(snapshot.Findings))
	assert.Contains(t, findingTitles(snapshot.Findings), "Mount is read-only")
}

func TestNormalizeTruncatesSerializedSnapshot(t *testing.T) {
	resources := make([]Resource, maxResources)
	for i := range resources {
		resources[i] = Resource{ID: string(rune(0x1000 + i)), Name: strings.Repeat("x", maxFieldBytes), Kind: "disk"}
	}
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{name: "block", result: AdapterResult{Resources: resources}}})
	require.NoError(t, err)
	assert.True(t, snapshot.Truncated)
	require.Len(t, snapshot.Backends, 1)
	assert.Equal(t, BackendTruncated, snapshot.Backends[0].Availability)
}

func TestNormalizeDropsRelationsToResourceLimitDrops(t *testing.T) {
	resources := make([]Resource, maxResources+1)
	for i := range resources {
		resources[i] = Resource{ID: fmt.Sprintf("resource-%04d", i)}
	}
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{name: "block", core: true, result: AdapterResult{
		Resources: resources,
		Relations: []Relation{{From: "resource-0000", To: "resource-4096", Kind: "contains"}},
	}}})
	require.NoError(t, err)
	assert.True(t, snapshot.Truncated)
	assert.Empty(t, snapshot.Relations)
}

func TestNormalizeClearsDroppedResourceOnMountReplacement(t *testing.T) {
	resources := make([]Resource, maxResources)
	for i := range resources {
		resources[i] = Resource{ID: fmt.Sprintf("resource-%04d", i)}
	}
	core := AdapterResult{Resources: resources, Mounts: []Mount{{ID: "mount", ResourceID: "resource-0000"}}}
	enriched := AdapterResult{Resources: []Resource{{ID: "dropped"}}, Mounts: []Mount{{ID: "mount", ResourceID: "dropped"}}}

	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{name: "core", core: true, result: core}, {name: "zfs", result: enriched}})

	require.NoError(t, err)
	require.Len(t, snapshot.Mounts, 1)
	assert.Empty(t, snapshot.Mounts[0].ResourceID)
}

func TestNormalizeDiscardsMalformedOptionalResult(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{
		{name: "block", core: true, result: AdapterResult{Resources: []Resource{{ID: "disk"}}}},
		{name: "health", result: AdapterResult{Relations: []Relation{{From: "disk", To: "missing", Kind: "contains"}}}},
	})
	require.NoError(t, err)
	assert.Equal(t, []Resource{{ID: "disk"}}, snapshot.Resources)
	assert.Empty(t, snapshot.Relations)
	require.Len(t, snapshot.Backends, 2)
	assert.Equal(t, BackendUnavailable, snapshot.Backends[1].Availability)
}

func TestNormalizeKeepsSerializedSnapshotReferencesValid(t *testing.T) {
	resources := make([]Resource, maxResources)
	for i := range resources {
		resources[i] = Resource{ID: fmt.Sprintf("resource-%04d", i), Name: strings.Repeat("x", maxFieldBytes), Kind: "disk"}
	}
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{name: "block", core: true, result: AdapterResult{
		Resources: resources,
		Relations: []Relation{{From: "resource-0000", To: "resource-4095", Kind: "contains"}},
		Mounts:    []Mount{{ID: "mount", ResourceID: "resource-4095"}},
	}}})
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Mounts)
	assert.Empty(t, snapshot.Mounts[0].ResourceID)
	assert.NoError(t, validateGraph(snapshot.Resources, snapshot.Relations))
}

func TestNormalizeRejectsConflictingDuplicateResources(t *testing.T) {
	_, err := normalize(time.Unix(1, 0), []collectedResult{{core: true, result: AdapterResult{Resources: []Resource{{ID: "disk", Name: "one"}}}}, {core: true, result: AdapterResult{Resources: []Resource{{ID: "disk", Name: "two"}}}}})
	assert.ErrorContains(t, err, "conflicting resource")
}

func TestNormalizeDeduplicatesRelations(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), []collectedResult{{core: true, result: AdapterResult{
		Resources: []Resource{{ID: "a"}, {ID: "b"}},
		Relations: []Relation{{From: "a", To: "b", Kind: "contains"}, {From: "a", To: "b", Kind: "contains"}},
	}}})
	require.NoError(t, err)
	assert.Len(t, snapshot.Relations, 1)
}

func TestNormalizeInitializesHealthySummary(t *testing.T) {
	snapshot, err := normalize(time.Unix(1, 0), nil)
	require.NoError(t, err)
	assert.Equal(t, HealthHealthy, snapshot.Summary.HighestHealth)
}

func resourceIDs(resources []Resource) []string {
	ids := make([]string, len(resources))
	for i := range resources {
		ids[i] = resources[i].ID
	}
	return ids
}

func relationFromIDs(relations []Relation) []string {
	ids := make([]string, len(relations))
	for i := range relations {
		ids[i] = relations[i].From
	}
	return ids
}

func mountTargets(mounts []Mount) []string {
	targets := make([]string, len(mounts))
	for i := range mounts {
		targets[i] = mounts[i].Target
	}
	return targets
}

func findingResourceIDs(findings []Finding) []string {
	ids := make([]string, len(findings))
	for i := range findings {
		ids[i] = findings[i].ResourceID
	}
	return ids
}

func findingSeverities(findings []Finding) []Health {
	severities := make([]Health, len(findings))
	for i := range findings {
		severities[i] = findings[i].Severity
	}
	return severities
}

func findingTitles(findings []Finding) []string {
	titles := make([]string, len(findings))
	for i := range findings {
		titles[i] = findings[i].Title
	}
	return titles
}

func backendNames(backends []BackendStatus) []string {
	names := make([]string, len(backends))
	for i := range backends {
		names[i] = backends[i].Name
	}
	return names
}
