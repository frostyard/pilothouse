package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const zfsEnricherName = "zfs"

type ZFSTools struct{ ZPool, ZFS string }

type zfsEnricher struct {
	tools  ZFSTools
	runner commandRunner
}

type zfsPool struct {
	name                   string
	size, alloc, free, cap uint64
	health                 string
}
type zfsDataset struct {
	name, kind, mountpoint string
	used, available, refer uint64
}
type zfsStatus struct {
	state                 string
	read, write, checksum uint64
}

func NewZFSEnricher(paths ZFSTools) Enricher { return newZFSEnricher(paths) }
func newZFSEnricher(paths ZFSTools) *zfsEnricher {
	return &zfsEnricher{tools: paths, runner: commandRunner{limit: maxAdapterBytes}}
}
func (*zfsEnricher) Name() string { return zfsEnricherName }

func (e *zfsEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	pools, err := e.runner.Run(ctx, e.tools.ZPool, "list", "-Hp", "-o", "name,size,alloc,free,cap,health")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read ZFS pools: %w", err)
	}
	statuses, err := e.runner.Run(ctx, e.tools.ZPool, "status", "-P")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read ZFS status: %w", err)
	}
	datasets, err := e.runner.Run(ctx, e.tools.ZFS, "list", "-Hp", "-o", "name,type,used,available,refer,mountpoint")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read ZFS datasets: %w", err)
	}
	parsedPools, err := parseZFSPools(pools)
	if err != nil {
		return AdapterResult{}, err
	}
	parsedStatus, err := parseZFSStatus(statuses)
	if err != nil {
		return AdapterResult{}, err
	}
	parsedDatasets, err := parseZFSDatasets(datasets)
	if err != nil {
		return AdapterResult{}, err
	}
	return zfsResult(parsedPools, parsedStatus, parsedDatasets, inventory)
}

func parseZFSPools(input []byte) ([]zfsPool, error) {
	var pools []zfsPool
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(input)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 6 || validateStrings(fields...) != nil || fields[0] == "" || seen[fields[0]] || len(pools) >= maxResources {
			return nil, fmt.Errorf("invalid ZFS pool")
		}
		size, e1 := strictUint(fields[1])
		alloc, e2 := strictUint(fields[2])
		free, e3 := strictUint(fields[3])
		capText := strings.TrimSuffix(fields[4], "%")
		cap, e4 := strictUint(capText)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || cap > 100 || size == 0 || alloc > size || free > size {
			return nil, fmt.Errorf("invalid ZFS pool")
		}
		seen[fields[0]] = true
		pools = append(pools, zfsPool{name: fields[0], size: size, alloc: alloc, free: free, cap: cap, health: strings.ToUpper(fields[5])})
	}
	return pools, nil
}

func parseZFSDatasets(input []byte) ([]zfsDataset, error) {
	var datasets []zfsDataset
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(input)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 6 || validateStrings(fields...) != nil || fields[0] == "" || seen[fields[0]] || len(datasets) >= maxResources {
			return nil, fmt.Errorf("invalid ZFS dataset")
		}
		used, e1 := strictUint(fields[2])
		available, e2 := strictUint(fields[3])
		refer, e3 := strictUint(fields[4])
		if e1 != nil || e2 != nil || e3 != nil || (fields[1] != "filesystem" && fields[1] != "volume") {
			return nil, fmt.Errorf("invalid ZFS dataset")
		}
		seen[fields[0]] = true
		datasets = append(datasets, zfsDataset{name: fields[0], kind: fields[1], used: used, available: available, refer: refer, mountpoint: fields[5]})
	}
	return datasets, nil
}

func parseZFSStatus(input []byte) (map[string]zfsStatus, error) {
	states := map[string]zfsStatus{}
	var pool string
	inConfig := false
	for _, line := range strings.Split(string(input), "\n") {
		if len(line) > maxFieldBytes {
			return nil, fmt.Errorf("ZFS status line exceeds limit")
		}
		trimmed := strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(trimmed, "pool: "); ok {
			if name == "" || validateStrings(name) != nil || states[name].state != "" {
				return nil, fmt.Errorf("invalid ZFS status")
			}
			pool = name
			inConfig = false
			continue
		}
		if state, ok := strings.CutPrefix(trimmed, "state: "); ok && pool != "" {
			if state == "" || validateStrings(state) != nil {
				return nil, fmt.Errorf("invalid ZFS status")
			}
			status := states[pool]
			status.state = strings.ToUpper(state)
			states[pool] = status
			continue
		}
		if trimmed == "config:" {
			inConfig = true
			continue
		}
		if !inConfig || pool == "" || trimmed == "" || strings.HasPrefix(trimmed, "NAME ") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "/") || validateStrings(fields[:5]...) != nil {
			continue
		}
		read, e1 := strictUint(fields[2])
		write, e2 := strictUint(fields[3])
		checksum, e3 := strictUint(fields[4])
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, fmt.Errorf("invalid ZFS status errors")
		}
		status := states[pool]
		status.read += read
		status.write += write
		status.checksum += checksum
		states[pool] = status
	}
	return states, nil
}

func zfsResult(pools []zfsPool, states map[string]zfsStatus, datasets []zfsDataset, inventory Inventory) (AdapterResult, error) {
	known := make(map[string]zfsPool, len(pools))
	result := AdapterResult{}
	for _, pool := range pools {
		known[pool.name] = pool
		state := pool.health
		status := states[pool.name]
		if status.state != "" {
			state = status.state
		}
		health := HealthHealthy
		if state == "DEGRADED" || state == "FAULTED" || state == "UNAVAIL" {
			health = HealthCritical
		}
		id := stableID("zfs-pool", pool.name)
		details := []Detail{{Label: "Allocated", Value: fmt.Sprint(pool.alloc)}, {Label: "Free", Value: fmt.Sprint(pool.free)}, {Label: "Capacity", Value: fmt.Sprintf("%d%%", pool.cap)}, {Label: "Read errors", Value: fmt.Sprint(status.read)}, {Label: "Write errors", Value: fmt.Sprint(status.write)}, {Label: "Checksum errors", Value: fmt.Sprint(status.checksum)}}
		result.Resources = append(result.Resources, Resource{ID: id, Kind: "zfs-pool", Name: pool.name, SizeBytes: pool.size, Health: health, State: strings.ToLower(state), Details: details})
		if health == HealthCritical {
			result.Findings = append(result.Findings, Finding{ResourceID: id, Severity: health, Title: "ZFS pool is degraded", Detail: "one or more devices are unavailable"})
		}
	}
	for pool := range states {
		if _, ok := known[pool]; !ok {
			return AdapterResult{}, fmt.Errorf("ZFS status references unknown pool %q", pool)
		}
	}
	for _, dataset := range datasets {
		pool := strings.SplitN(dataset.name, "/", 2)[0]
		if _, ok := known[pool]; !ok {
			return AdapterResult{}, fmt.Errorf("ZFS dataset references unknown pool %q", pool)
		}
		id := stableID("zfs-dataset", dataset.name)
		result.Resources = append(result.Resources, Resource{ID: id, Kind: "zfs-" + dataset.kind, Name: dataset.name, SizeBytes: dataset.refer, Health: HealthHealthy, State: "available"})
		result.Relations = append(result.Relations, Relation{From: stableID("zfs-pool", pool), To: id, Kind: "contains"})
		if dataset.kind == "filesystem" && strings.HasPrefix(dataset.mountpoint, "/") {
			// Pool ownership prevents mounted datasets from adding capacity separately.
			mount := Mount{ID: stableID("zfs-mount", dataset.mountpoint), Target: dataset.mountpoint, Source: dataset.name, Filesystem: "zfs", ResourceID: stableID("zfs-pool", pool), State: "mounted"}
			pool := known[pool]
			mount.TotalBytes, mount.UsedBytes, mount.AvailableBytes = pool.size, pool.alloc, pool.free
			mount.UsedPercent = zfsUsedPercent(pool.alloc, pool.size)
			for _, coreMount := range inventory.Mounts {
				if coreMount.Target == mount.Target && coreMount.Source == mount.Source && coreMount.Filesystem == mount.Filesystem {
					mount.ID = coreMount.ID
					break
				}
			}
			result.Mounts = append(result.Mounts, mount)
		}
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sortRelations(result.Relations)
	return result, nil
}

func zfsUsedPercent(alloc, size uint64) float64 {
	if size == 0 {
		return 0
	}
	percent := float64(alloc) * 100 / float64(size)
	if percent > 100 {
		return 100
	}
	return percent
}
