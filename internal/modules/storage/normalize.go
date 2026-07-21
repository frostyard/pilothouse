package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"time"
)

type collectedResult struct {
	err    error
	name   string
	core   bool
	result AdapterResult
}

type aggregate struct {
	dropped   map[string]bool
	findings  []Finding
	mounts    []Mount
	relations map[string]Relation
	resources map[string]Resource
	truncated map[string]bool
}

func newAggregate() aggregate {
	return aggregate{dropped: make(map[string]bool), relations: make(map[string]Relation), resources: make(map[string]Resource), truncated: make(map[string]bool)}
}

func (a aggregate) clone() aggregate {
	a.dropped = maps.Clone(a.dropped)
	a.findings = slices.Clone(a.findings)
	a.mounts = slices.Clone(a.mounts)
	a.relations = maps.Clone(a.relations)
	a.resources = maps.Clone(a.resources)
	a.truncated = maps.Clone(a.truncated)
	return a
}

func normalize(collectedAt time.Time, results []collectedResult) (Snapshot, error) {
	statuses := make([]BackendStatus, len(results))
	state := newAggregate()
	for i, collected := range results {
		statuses[i] = BackendStatus{Availability: availabilityFor(collected.err), CollectedAt: collectedAt, Name: collected.name}
		if collected.core && collected.err == nil {
			if err := state.apply(collected.name, collected.result); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if err := state.validate(); err != nil {
		return Snapshot{}, err
	}
	for i, collected := range results {
		if collected.core || collected.err != nil {
			continue
		}
		candidate := state.clone()
		if err := candidate.apply(collected.name, collected.result); err != nil || candidate.validate() != nil {
			statuses[i].Availability = BackendUnavailable
			continue
		}
		state = candidate
	}

	snapshot := Snapshot{Backends: statuses, CollectedAt: collectedAt, Findings: slices.Clone(state.findings), Mounts: slices.Clone(state.mounts), Truncated: len(state.truncated) > 0}
	for _, relation := range state.relations {
		snapshot.Relations = append(snapshot.Relations, relation)
	}
	for _, resource := range state.resources {
		snapshot.Resources = append(snapshot.Resources, resource)
	}
	for i := range snapshot.Mounts {
		mount := &snapshot.Mounts[i]
		if mount.UsedPercent >= 90 {
			mount.Health = HealthCritical
			appendFinding(&snapshot, &state.truncated, "", Finding{ResourceID: mount.ResourceID, Severity: HealthCritical, Title: "Mount capacity is critical", Detail: mount.Target})
		} else if mount.UsedPercent >= 80 {
			mount.Health = HealthWarning
			appendFinding(&snapshot, &state.truncated, "", Finding{ResourceID: mount.ResourceID, Severity: HealthWarning, Title: "Mount capacity is high", Detail: mount.Target})
		}
		if mount.ReadOnly && !mount.Managed {
			if healthRank(mount.Health) < healthRank(HealthWarning) {
				mount.Health = HealthWarning
			}
			appendFinding(&snapshot, &state.truncated, "", Finding{ResourceID: mount.ResourceID, Severity: HealthWarning, Title: "Mount is read-only", Detail: mount.Target})
		}
	}
	for i := range snapshot.Backends {
		if state.truncated[snapshot.Backends[i].Name] {
			snapshot.Backends[i].Availability = BackendTruncated
			snapshot.Truncated = true
		}
	}
	sortSnapshot(&snapshot)
	if err := enforceSnapshotLimit(&snapshot); err != nil {
		return Snapshot{}, err
	}
	recalculateSummary(&snapshot)
	return snapshot, nil
}

func availabilityFor(err error) Availability {
	if err == nil {
		return BackendAvailable
	}
	if err == context.DeadlineExceeded {
		return BackendTimedOut
	}
	return BackendUnavailable
}

func (a *aggregate) apply(backend string, result AdapterResult) error {
	if result.Truncated {
		a.truncated[backend] = true
	}
	for _, resource := range result.Resources {
		if existing, ok := a.resources[resource.ID]; ok {
			if !reflect.DeepEqual(existing, resource) {
				return fmt.Errorf("conflicting resource %q", resource.ID)
			}
			continue
		}
		if len(a.resources) == maxResources {
			a.dropped[resource.ID] = true
			a.truncated[backend] = true
			continue
		}
		if len(resource.Details) > maxDetails {
			resource.Details = resource.Details[:maxDetails]
			a.truncated[backend] = true
		}
		a.resources[resource.ID] = resource
	}
	for _, relation := range result.Relations {
		if a.dropped[relation.From] || a.dropped[relation.To] {
			continue
		}
		key := relationKey(relation)
		if _, ok := a.relations[key]; ok {
			continue
		}
		if len(a.relations) == maxRelations {
			a.truncated[backend] = true
			continue
		}
		a.relations[key] = relation
	}
	for _, mount := range result.Mounts {
		if len(a.mounts) == maxMounts {
			a.truncated[backend] = true
			continue
		}
		if a.dropped[mount.ResourceID] {
			mount.ResourceID = ""
		}
		a.mounts = append(a.mounts, mount)
	}
	for _, finding := range result.Findings {
		if len(a.findings) == maxFindings {
			a.truncated[backend] = true
			continue
		}
		a.findings = append(a.findings, finding)
	}
	return nil
}

func (a aggregate) validate() error {
	for _, relation := range a.relations {
		if _, ok := a.resources[relation.From]; !ok {
			return fmt.Errorf("relation has missing endpoint %q", relation.From)
		}
		if _, ok := a.resources[relation.To]; !ok {
			return fmt.Errorf("relation has missing endpoint %q", relation.To)
		}
	}
	resources := make([]Resource, 0, len(a.resources))
	relations := make([]Relation, 0, len(a.relations))
	for _, resource := range a.resources {
		resources = append(resources, resource)
	}
	for _, relation := range a.relations {
		relations = append(relations, relation)
	}
	return validateGraph(resources, relations)
}

func relationKey(relation Relation) string {
	return relation.From + "\x00" + relation.Kind + "\x00" + relation.To
}

func appendFinding(snapshot *Snapshot, truncated *map[string]bool, backend string, finding Finding) {
	if len(snapshot.Findings) == maxFindings {
		(*truncated)[backend] = true
		return
	}
	snapshot.Findings = append(snapshot.Findings, finding)
}

func validateGraph(resources []Resource, relations []Relation) error {
	edges := make(map[string][]string, len(resources))
	for _, relation := range relations {
		edges[relation.From] = append(edges[relation.From], relation.To)
	}
	state := make(map[string]int, len(resources))
	depths := make(map[string]int, len(resources))
	var walk func(string) (int, error)
	walk = func(id string) (int, error) {
		if state[id] == 1 {
			return 0, fmt.Errorf("graph cycle detected")
		}
		if state[id] == 2 {
			return depths[id], nil
		}
		state[id] = 1
		depth := 0
		for _, child := range edges[id] {
			childDepth, err := walk(child)
			if err != nil {
				return 0, err
			}
			if childDepth+1 > depth {
				depth = childDepth + 1
			}
		}
		state[id] = 2
		depths[id] = depth
		return depth, nil
	}
	for _, resource := range resources {
		depth, err := walk(resource.ID)
		if err != nil {
			return err
		}
		if depth > maxGraphDepth {
			return fmt.Errorf("graph depth exceeds limit")
		}
	}
	return nil
}

func sortSnapshot(snapshot *Snapshot) {
	sort.Slice(snapshot.Resources, func(i, j int) bool {
		if snapshot.Resources[i].Kind != snapshot.Resources[j].Kind {
			return snapshot.Resources[i].Kind < snapshot.Resources[j].Kind
		}
		if snapshot.Resources[i].Name != snapshot.Resources[j].Name {
			return snapshot.Resources[i].Name < snapshot.Resources[j].Name
		}
		return snapshot.Resources[i].ID < snapshot.Resources[j].ID
	})
	sort.Slice(snapshot.Relations, func(i, j int) bool {
		if snapshot.Relations[i].From != snapshot.Relations[j].From {
			return snapshot.Relations[i].From < snapshot.Relations[j].From
		}
		if snapshot.Relations[i].Kind != snapshot.Relations[j].Kind {
			return snapshot.Relations[i].Kind < snapshot.Relations[j].Kind
		}
		return snapshot.Relations[i].To < snapshot.Relations[j].To
	})
	sort.Slice(snapshot.Mounts, func(i, j int) bool {
		if snapshot.Mounts[i].Target != snapshot.Mounts[j].Target {
			return snapshot.Mounts[i].Target < snapshot.Mounts[j].Target
		}
		if snapshot.Mounts[i].Source != snapshot.Mounts[j].Source {
			return snapshot.Mounts[i].Source < snapshot.Mounts[j].Source
		}
		return snapshot.Mounts[i].ID < snapshot.Mounts[j].ID
	})
	sort.Slice(snapshot.Findings, func(i, j int) bool {
		if healthRank(snapshot.Findings[i].Severity) != healthRank(snapshot.Findings[j].Severity) {
			return healthRank(snapshot.Findings[i].Severity) > healthRank(snapshot.Findings[j].Severity)
		}
		if snapshot.Findings[i].ResourceID != snapshot.Findings[j].ResourceID {
			return snapshot.Findings[i].ResourceID < snapshot.Findings[j].ResourceID
		}
		return snapshot.Findings[i].Title < snapshot.Findings[j].Title
	})
	sort.Slice(snapshot.Backends, func(i, j int) bool { return snapshot.Backends[i].Name < snapshot.Backends[j].Name })
}

func higherHealth(current, candidate Health) Health {
	if healthRank(candidate) > healthRank(current) {
		return candidate
	}
	return current
}

func recalculateSummary(snapshot *Snapshot) {
	summary := Summary{HighestHealth: HealthHealthy}
	capacity := make(map[string]bool)
	for _, resource := range snapshot.Resources {
		if healthRank(resource.Health) >= healthRank(HealthWarning) {
			summary.UnhealthyResources++
		}
		summary.HighestHealth = higherHealth(summary.HighestHealth, resource.Health)
	}
	for _, mount := range snapshot.Mounts {
		if mount.State == "mounted" {
			summary.ActiveMounts++
		}
		if mount.Filesystem != "overlay" && mount.ResourceID != "" && !capacity[mount.ResourceID] {
			capacity[mount.ResourceID] = true
			summary.UsableBytes += mount.TotalBytes
			summary.UsedBytes += mount.UsedBytes
			summary.FreeBytes += mount.AvailableBytes
		}
		summary.HighestHealth = higherHealth(summary.HighestHealth, mount.Health)
	}
	for _, finding := range snapshot.Findings {
		summary.HighestHealth = higherHealth(summary.HighestHealth, finding.Severity)
	}
	for _, backend := range snapshot.Backends {
		if backend.Availability != BackendAvailable {
			summary.UnavailableBackends++
		}
	}
	snapshot.Summary = summary
}

func enforceSnapshotLimit(snapshot *Snapshot) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("serialize snapshot: %w", err)
	}
	for len(encoded) > maxSnapshotBytes {
		snapshot.Truncated = true
		if len(snapshot.Resources) > 0 {
			removed := snapshot.Resources[len(snapshot.Resources)/2:]
			snapshot.Resources = snapshot.Resources[:len(snapshot.Resources)/2]
			sanitizeSnapshotReferences(snapshot, removed)
		} else if len(snapshot.Findings) > 0 {
			snapshot.Findings = snapshot.Findings[:len(snapshot.Findings)/2]
		} else if len(snapshot.Mounts) > 0 {
			snapshot.Mounts = snapshot.Mounts[:len(snapshot.Mounts)/2]
		} else if len(snapshot.Relations) > 0 {
			snapshot.Relations = snapshot.Relations[:len(snapshot.Relations)/2]
		} else {
			return fmt.Errorf("snapshot metadata exceeds limit")
		}
		for i := range snapshot.Backends {
			snapshot.Backends[i].Availability = BackendTruncated
		}
		encoded, err = json.Marshal(snapshot)
		if err != nil {
			return fmt.Errorf("serialize snapshot: %w", err)
		}
	}
	return validateGraph(snapshot.Resources, snapshot.Relations)
}

func sanitizeSnapshotReferences(snapshot *Snapshot, removed []Resource) {
	removedIDs := make(map[string]bool, len(removed))
	for _, resource := range removed {
		removedIDs[resource.ID] = true
	}
	relations := snapshot.Relations[:0]
	for _, relation := range snapshot.Relations {
		if !removedIDs[relation.From] && !removedIDs[relation.To] {
			relations = append(relations, relation)
		}
	}
	snapshot.Relations = relations
	for i := range snapshot.Mounts {
		if removedIDs[snapshot.Mounts[i].ResourceID] {
			snapshot.Mounts[i].ResourceID = ""
		}
	}
}
