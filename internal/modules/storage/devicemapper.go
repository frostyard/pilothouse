package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	deviceMapperEnricherName = "device-mapper"
	cryptUUIDPrefix          = "CRYPT-LUKS"
)

type deviceMapperEnricher struct {
	dmsetup string
	runner  commandRunner
}
type multipathEnricher struct {
	multipathd string
	runner     commandRunner
}
type dmInfo struct {
	Name, UUID, MajorMinor string
	Open, Segments         uint64
}
type multipathMap struct {
	Name, WWID, Device, State string
	DeclaredPaths             uint64
}
type multipathPath struct{ Map, Device, DMState, CheckerState string }

func NewDeviceMapperEnricher(dmsetupPath string) Enricher {
	return newDeviceMapperEnricher(dmsetupPath)
}
func newDeviceMapperEnricher(dmsetupPath string) *deviceMapperEnricher {
	return &deviceMapperEnricher{dmsetup: dmsetupPath, runner: commandRunner{limit: maxAdapterBytes}}
}
func (*deviceMapperEnricher) Name() string { return deviceMapperEnricherName }

func NewMultipathEnricher(multipathdPath string) Enricher {
	return newMultipathEnricher(multipathdPath)
}
func newMultipathEnricher(multipathdPath string) *multipathEnricher {
	return &multipathEnricher{multipathd: multipathdPath, runner: commandRunner{limit: maxAdapterBytes}}
}
func (*multipathEnricher) Name() string { return "multipath" }

func (e *deviceMapperEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	dmOutput, err := e.runner.Run(ctx, e.dmsetup, "info", "--columns", "--noheadings", "--separator", "|", "-o", "name,uuid,major,minor,open,segments")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read device-mapper info: %w", err)
	}
	infos, err := parseDMInfo(dmOutput)
	if err != nil {
		return AdapterResult{}, err
	}
	return deviceMapperResult(infos, inventory), nil
}

func (e *multipathEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	multipathOutput, err := e.runner.Run(ctx, e.multipathd, "show", "maps", "raw", "format", "%n|%w|%d|%N|%t")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read multipath maps: %w", err)
	}
	maps, err := parseMultipathMaps(multipathOutput)
	if err != nil {
		return AdapterResult{}, err
	}
	pathsOutput, err := e.runner.Run(ctx, e.multipathd, "show", "paths", "raw", "format", "%m|%d|%t|%o")
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read multipath paths: %w", err)
	}
	paths, err := parseMultipathPaths(pathsOutput)
	if err != nil {
		return AdapterResult{}, err
	}
	return multipathResult(maps, paths, inventory), nil
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
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || seen[identity] || len(result) >= maxResources {
			return nil, fmt.Errorf("invalid device-mapper info")
		}
		seen[identity] = true
		result = append(result, dmInfo{Name: fields[0], UUID: fields[1], MajorMinor: strconv.FormatUint(major, 10) + ":" + strconv.FormatUint(minor, 10), Open: open, Segments: segments})
	}
	return result, nil
}

func parseMultipathMaps(input []byte) ([]multipathMap, error) {
	var result []multipathMap
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(input)), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 5 {
			return nil, fmt.Errorf("invalid multipath map")
		}
		paths, err := strictUint(fields[3])
		if err != nil || fields[0] == "" || fields[1] == "" || fields[2] == "" || seen[fields[0]] || validateStrings(fields...) != nil || len(result) >= maxResources {
			return nil, fmt.Errorf("invalid multipath map")
		}
		seen[fields[0]] = true
		result = append(result, multipathMap{Name: fields[0], WWID: fields[1], Device: fields[2], DeclaredPaths: paths, State: fields[4]})
	}
	return result, nil
}

func parseMultipathPaths(input []byte) ([]multipathPath, error) {
	var result []multipathPath
	for _, line := range strings.Split(strings.TrimSpace(string(input)), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 4 || fields[0] == "" || fields[1] == "" || validateStrings(fields...) != nil || len(result) >= maxResources {
			return nil, fmt.Errorf("invalid multipath path")
		}
		result = append(result, multipathPath{Map: fields[0], Device: fields[1], DMState: fields[2], CheckerState: fields[3]})
	}
	return result, nil
}

func deviceMapperResult(infos []dmInfo, inventory Inventory) AdapterResult {
	result := AdapterResult{}
	coreByPath := map[string]Resource{}
	coreByMajorMinor := map[string]Resource{}
	for _, resource := range inventory.Resources {
		if resource.Path == filepath.Clean(resource.Path) && strings.HasPrefix(resource.Path, "/dev/mapper/") {
			coreByPath[resource.Path] = resource
		}
		for _, detail := range resource.Details {
			if detail.Label == "MAJ:MIN" && validMajorMinor(detail.Value) {
				coreByMajorMinor[detail.Value] = resource
			}
		}
	}
	for _, info := range infos {
		path := "/dev/mapper/" + info.Name
		resource, exists := coreByPath[path]
		if !exists {
			resource, exists = coreByMajorMinor[info.MajorMinor]
		}
		if !exists {
			resource = Resource{ID: stableID("mapping", info.MajorMinor), Kind: "mapping", Name: info.Name, Path: path}
		}
		id := resource.ID
		resource.Name, resource.Path, resource.State, resource.Health = info.Name, path, "active", HealthHealthy
		if info.Open == 0 {
			resource.State, resource.Health = "inactive", HealthUnknown
		}
		if strings.HasPrefix(info.UUID, cryptUUIDPrefix) {
			resource.Details = append(resource.Details, Detail{Label: "Encrypted", Value: "Yes"})
			encryptionID := stableID("encryption", info.UUID)
			result.Resources = append(result.Resources, Resource{ID: encryptionID, Kind: "encryption", Name: info.Name, Health: resource.Health, State: resource.State})
			result.Relations = append(result.Relations, Relation{From: encryptionID, To: id, Kind: "maps-to"})
		}
		if len(result.Resources) < maxResources {
			result.Resources = append(result.Resources, resource)
		}
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sortRelations(result.Relations)
	return result
}

func multipathResult(maps []multipathMap, paths []multipathPath, inventory Inventory) AdapterResult {
	result := AdapterResult{}
	resources := make(map[string]Resource, len(inventory.Resources))
	for _, resource := range inventory.Resources {
		if resource.Kind == "mapping" && resource.Path == filepath.Clean(resource.Path) && strings.HasPrefix(resource.Path, "/dev/mapper/") {
			resources[resource.Path] = resource
		}
	}
	for _, mapping := range maps {
		resource, ok := resources["/dev/mapper/"+mapping.Name]
		if !ok {
			continue
		}
		health, detail := multipathHealth(mapping, paths)
		resource.Name = mapping.Name
		resource.Health = higherHealth(resource.Health, health)
		resource.Details = append(resource.Details, Detail{Label: "Paths", Value: pathSummary(mapping, paths)})
		result.Resources = append(result.Resources, resource)
		switch health {
		case HealthCritical:
			result.Findings = append(result.Findings, Finding{ResourceID: resource.ID, Severity: health, Title: "Multipath map has failed paths", Detail: detail})
		case HealthWarning:
			result.Findings = append(result.Findings, Finding{ResourceID: resource.ID, Severity: health, Title: "Multipath map is degraded", Detail: detail})
		}
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	return result
}

func pathSummary(mapping multipathMap, paths []multipathPath) string {
	observed := 0
	for _, path := range paths {
		if path.Map == mapping.Name {
			observed++
		}
	}
	return fmt.Sprintf("%d of %d observed", observed, mapping.DeclaredPaths)
}

func multipathHealth(mapping multipathMap, paths []multipathPath) (Health, string) {
	healthy, failed, unknown := 0, 0, 0
	for _, path := range paths {
		if path.Map != mapping.Name {
			continue
		}
		switch {
		case path.DMState == "active" && path.CheckerState == "ready":
			healthy++
		case path.DMState == "failed" || path.CheckerState == "faulty" || path.CheckerState == "down":
			failed++
		default:
			unknown++
		}
	}
	observed := healthy + failed + unknown
	if failed > 0 {
		return HealthCritical, fmt.Sprintf("%d of %d paths failed", failed, observed)
	}
	if unknown > 0 {
		return HealthUnknown, "multipath path state is unknown"
	}
	if uint64(observed) != mapping.DeclaredPaths {
		return HealthWarning, fmt.Sprintf("%d of %d paths observed", observed, mapping.DeclaredPaths)
	}
	return HealthHealthy, ""
}
