package storage

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	deviceMapperEnricherName = "device-mapper"
	cryptUUIDPrefix          = "CRYPT-LUKS"
)

type deviceMapperEnricher struct {
	dmsetup, multipathd string
	runner              commandRunner
}
type dmInfo struct {
	Name, UUID, MajorMinor string
	Open, Segments         uint64
}
type multipathMap struct {
	Name, WWID string
	Paths      uint64
	State      string
}

func NewDeviceMapperEnricher(dmsetupPath, multipathdPath string) Enricher {
	return newDeviceMapperEnricher(dmsetupPath, multipathdPath)
}
func newDeviceMapperEnricher(dmsetupPath, multipathdPath string) *deviceMapperEnricher {
	return &deviceMapperEnricher{dmsetup: dmsetupPath, multipathd: multipathdPath, runner: commandRunner{limit: maxAdapterBytes}}
}
func (*deviceMapperEnricher) Name() string { return deviceMapperEnricherName }

func (e *deviceMapperEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	dmOutput, err := e.runner.Run(ctx, e.dmsetup, "info", "--columns", "--noheadings", "--separator", "|", "-o", "name,uuid,major,minor,open,segments")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read device-mapper info: %w", err)
	}
	infos, err := parseDMInfo(dmOutput)
	if err != nil {
		return AdapterResult{}, err
	}
	multipathOutput, err := e.runner.Run(ctx, e.multipathd, "show", "maps", "raw", "format", "%n|%w|%d|%t")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read multipath maps: %w", err)
	}
	maps, err := parseMultipath(multipathOutput)
	if err != nil {
		return AdapterResult{}, err
	}
	return deviceMapperResult(infos, maps, inventory), nil
}

func parseDMInfo(input []byte) ([]dmInfo, error) {
	var result []dmInfo
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(input)), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 6 {
			return nil, fmt.Errorf("invalid device-mapper info")
		}
		if err := validateStrings(fields...); err != nil || fields[0] == "" || fields[1] == "" {
			return nil, fmt.Errorf("invalid device-mapper info")
		}
		major, e1 := strictUint(fields[2])
		minor, e2 := strictUint(fields[3])
		open, e3 := strictUint(fields[4])
		segments, e4 := strictUint(fields[5])
		identity := fields[2] + ":" + fields[3]
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || seen[identity] {
			return nil, fmt.Errorf("invalid device-mapper info")
		}
		seen[identity] = true
		result = append(result, dmInfo{Name: fields[0], UUID: fields[1], MajorMinor: strconv.FormatUint(major, 10) + ":" + strconv.FormatUint(minor, 10), Open: open, Segments: segments})
	}
	return result, nil
}

func parseMultipath(input []byte) ([]multipathMap, error) {
	var result []multipathMap
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(input)), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 4 {
			return nil, fmt.Errorf("invalid multipath map")
		}
		paths, err := strictUint(fields[2])
		if err != nil || paths == 0 || fields[0] == "" || fields[1] == "" || (fields[3] != "active" && fields[3] != "failed") || seen[fields[0]] || validateStrings(fields...) != nil {
			return nil, fmt.Errorf("invalid multipath map")
		}
		seen[fields[0]] = true
		result = append(result, multipathMap{Name: fields[0], WWID: fields[1], Paths: paths, State: fields[3]})
	}
	return result, nil
}

func deviceMapperResult(infos []dmInfo, maps []multipathMap, inventory Inventory) AdapterResult {
	result := AdapterResult{}
	core := map[string]Resource{}
	mapByName := map[string]multipathMap{}
	for _, resource := range inventory.Resources {
		core[resource.ID] = resource
	}
	for _, m := range maps {
		mapByName[m.Name] = m
	}
	for _, info := range infos {
		id := stableID("mapping", info.MajorMinor)
		resource, exists := core[id]
		if !exists {
			resource = Resource{ID: id, Kind: "mapping", Name: info.Name, Path: "/dev/mapper/" + info.Name}
		}
		resource.Name, resource.Path, resource.State, resource.Health = info.Name, "/dev/mapper/"+info.Name, "active", HealthHealthy
		if info.Open == 0 {
			resource.State, resource.Health = "inactive", HealthUnknown
		}
		if strings.HasPrefix(info.UUID, cryptUUIDPrefix) {
			resource.Details = append(resource.Details, Detail{Label: "Encrypted", Value: "Yes"})
			encryptionID := stableID("encryption", info.UUID)
			result.Resources = append(result.Resources, Resource{ID: encryptionID, Kind: "encryption", Name: info.Name, Health: resource.Health, State: resource.State})
			result.Relations = append(result.Relations, Relation{From: encryptionID, To: id, Kind: "maps-to"})
		}
		if multipath, ok := mapByName[info.Name]; ok && multipath.State == "failed" {
			resource.Health = HealthCritical
			result.Findings = append(result.Findings, Finding{ResourceID: id, Severity: HealthCritical, Title: "Multipath map has failed paths", Detail: fmt.Sprintf("1 of %d paths failed", multipath.Paths)})
		} else if ok && multipath.Paths == 1 {
			resource.Health = higherHealth(resource.Health, HealthWarning)
			result.Findings = append(result.Findings, Finding{ResourceID: id, Severity: HealthWarning, Title: "Multipath map is degraded", Detail: "1 active path"})
		}
		result.Resources = append(result.Resources, resource)
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sortRelations(result.Relations)
	return result
}
