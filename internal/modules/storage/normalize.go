package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"
)

type collectedResult struct {
	err    error
	name   string
	core   bool
	result AdapterResult
}

func normalize(collectedAt time.Time, results []collectedResult) (Snapshot, error) {
	snapshot := Snapshot{CollectedAt: collectedAt}
	resources := make(map[string]Resource)
	relations := make(map[string]Relation)
	capacity := make(map[string]bool)
	truncated := make(map[string]bool)

	for _, collected := range results {
		status := BackendAvailable
		if collected.err != nil {
			status = BackendUnavailable
			if collected.err == context.DeadlineExceeded {
				status = BackendTimedOut
			}
		}
		if collected.result.Truncated {
			truncated[collected.name] = true
		}
		snapshot.Backends = append(snapshot.Backends, BackendStatus{Availability: status, CollectedAt: collectedAt, Name: collected.name})

		for _, resource := range collected.result.Resources {
			if existing, ok := resources[resource.ID]; ok {
				if !reflect.DeepEqual(existing, resource) {
					return Snapshot{}, fmt.Errorf("conflicting resource %q", resource.ID)
				}
				continue
			}
			if len(resources) == maxResources {
				truncated[collected.name] = true
				continue
			}
			if len(resource.Details) > maxDetails {
				resource.Details = resource.Details[:maxDetails]
				truncated[collected.name] = true
			}
			resources[resource.ID] = resource
		}
		for _, relation := range collected.result.Relations {
			key := relation.From + "\x00" + relation.Kind + "\x00" + relation.To
			if _, ok := relations[key]; ok {
				continue
			}
			if len(relations) == maxRelations {
				truncated[collected.name] = true
				continue
			}
			relations[key] = relation
		}
		for _, mount := range collected.result.Mounts {
			if len(snapshot.Mounts) == maxMounts {
				truncated[collected.name] = true
				continue
			}
			if mount.UsedPercent >= 90 {
				mount.Health = HealthCritical
				appendFinding(&snapshot, &truncated, collected.name, Finding{ResourceID: mount.ResourceID, Severity: HealthCritical, Title: "Mount capacity is critical", Detail: mount.Target})
			} else if mount.UsedPercent >= 80 {
				mount.Health = HealthWarning
				appendFinding(&snapshot, &truncated, collected.name, Finding{ResourceID: mount.ResourceID, Severity: HealthWarning, Title: "Mount capacity is high", Detail: mount.Target})
			}
			if mount.ReadOnly && !mount.Managed {
				if healthRank(mount.Health) < healthRank(HealthWarning) {
					mount.Health = HealthWarning
				}
				appendFinding(&snapshot, &truncated, collected.name, Finding{ResourceID: mount.ResourceID, Severity: HealthWarning, Title: "Mount is read-only", Detail: mount.Target})
			}
			snapshot.Mounts = append(snapshot.Mounts, mount)
		}
		for _, finding := range collected.result.Findings {
			appendFinding(&snapshot, &truncated, collected.name, finding)
		}
	}

	for _, relation := range relations {
		if _, ok := resources[relation.From]; !ok {
			return Snapshot{}, fmt.Errorf("relation has missing endpoint %q", relation.From)
		}
		if _, ok := resources[relation.To]; !ok {
			return Snapshot{}, fmt.Errorf("relation has missing endpoint %q", relation.To)
		}
		snapshot.Relations = append(snapshot.Relations, relation)
	}
	for _, resource := range resources {
		snapshot.Resources = append(snapshot.Resources, resource)
	}
	if err := validateGraph(snapshot.Resources, snapshot.Relations); err != nil {
		return Snapshot{}, err
	}

	for i := range snapshot.Backends {
		if truncated[snapshot.Backends[i].Name] {
			snapshot.Backends[i].Availability = BackendTruncated
			snapshot.Truncated = true
		}
	}
	sortSnapshot(&snapshot)
	if err := enforceSnapshotLimit(&snapshot); err != nil {
		return Snapshot{}, err
	}
	recalculateSummary(&snapshot, capacity)
	return snapshot, nil
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

func recalculateSummary(snapshot *Snapshot, capacity map[string]bool) {
	summary := Summary{}
	for id := range capacity {
		delete(capacity, id)
	}
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
		if len(snapshot.Findings) > 0 {
			snapshot.Findings = snapshot.Findings[:len(snapshot.Findings)/2]
		} else if len(snapshot.Mounts) > 0 {
			snapshot.Mounts = snapshot.Mounts[:len(snapshot.Mounts)/2]
		} else if len(snapshot.Relations) > 0 {
			snapshot.Relations = snapshot.Relations[:len(snapshot.Relations)/2]
		} else if len(snapshot.Resources) > 0 {
			snapshot.Resources = snapshot.Resources[:len(snapshot.Resources)/2]
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
	return nil
}
