package storage

import (
	"context"
	"fmt"
	"sort"
	"strconv"
)

const lvmEnricherName = "lvm"

const (
	lvmPVFields = "pv_uuid,vg_uuid,pv_name,pv_size,pv_free,pv_attr"
	lvmVGFields = "vg_uuid,vg_name,vg_size,vg_free,vg_attr"
	lvmLVFields = "lv_uuid,vg_uuid,lv_name,vg_name,lv_path,lv_size,lv_attr,data_percent,metadata_percent"
)

type LVMTools struct{ PVS, VGS, LVS string }

type lvmEnricher struct {
	tools  LVMTools
	runner commandRunner
}

func NewLVMEnricher(paths LVMTools) Enricher { return newLVMEnricher(paths) }

func newLVMEnricher(paths LVMTools) *lvmEnricher {
	return &lvmEnricher{tools: paths, runner: commandRunner{limit: maxAdapterBytes}}
}

func (*lvmEnricher) Name() string { return lvmEnricherName }

func (e *lvmEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	pvs, err := e.report(ctx, e.tools.PVS, lvmPVFields)
	if err != nil {
		return AdapterResult{}, err
	}
	vgs, err := e.report(ctx, e.tools.VGS, lvmVGFields)
	if err != nil {
		return AdapterResult{}, err
	}
	lvs, err := e.report(ctx, e.tools.LVS, lvmLVFields)
	if err != nil {
		return AdapterResult{}, err
	}
	return lvmResult(pvs, vgs, lvs, inventory)
}

func (e *lvmEnricher) report(ctx context.Context, path, fields string) (lvmReport, error) {
	output, err := e.runner.Run(ctx, path, "--reportformat", "json", "--units", "b", "--nosuffix", "-o", fields)
	if err != nil {
		return lvmReport{}, fmt.Errorf("read LVM report: %w", err)
	}
	return parseLVMReport(output)
}

type lvmReport struct {
	PVs []lvmPV
	VGs []lvmVG
	LVs []lvmLV
}
type lvmDocument struct {
	Report []struct {
		PVs []lvmPV `json:"pv"`
		VGs []lvmVG `json:"vg"`
		LVs []lvmLV `json:"lv"`
	} `json:"report"`
}
type lvmPV struct {
	UUID   string `json:"pv_uuid"`
	VGUUID string `json:"vg_uuid"`
	Name   string `json:"pv_name"`
	Size   string `json:"pv_size"`
	Free   string `json:"pv_free"`
	Attr   string `json:"pv_attr"`
}
type lvmVG struct {
	UUID string `json:"vg_uuid"`
	Name string `json:"vg_name"`
	Size string `json:"vg_size"`
	Free string `json:"vg_free"`
	Attr string `json:"vg_attr"`
}
type lvmLV struct {
	UUID     string `json:"lv_uuid"`
	VGUUID   string `json:"vg_uuid"`
	Name     string `json:"lv_name"`
	VGName   string `json:"vg_name"`
	Path     string `json:"lv_path"`
	Size     string `json:"lv_size"`
	Attr     string `json:"lv_attr"`
	Data     string `json:"data_percent"`
	Metadata string `json:"metadata_percent"`
}

func parseLVMReport(input []byte) (lvmReport, error) {
	var document lvmDocument
	if err := decodeStrict(input, &document); err != nil {
		return lvmReport{}, fmt.Errorf("decode LVM report: %w", err)
	}
	if len(document.Report) != 1 {
		return lvmReport{}, fmt.Errorf("invalid LVM report")
	}
	report := lvmReport{PVs: document.Report[0].PVs, VGs: document.Report[0].VGs, LVs: document.Report[0].LVs}
	if (len(report.PVs) > 0 && (len(report.VGs) > 0 || len(report.LVs) > 0)) || (len(report.VGs) > 0 && len(report.LVs) > 0) || len(report.PVs)+len(report.VGs)+len(report.LVs) > maxResources {
		return lvmReport{}, fmt.Errorf("invalid LVM report")
	}
	seen := map[string]bool{}
	for _, pv := range report.PVs {
		if err := validLVMFields(pv.UUID, pv.VGUUID, pv.Name, pv.Size, pv.Free, pv.Attr); err != nil || pv.UUID == "" || !validLVMSize(pv.Size) || !validLVMSize(pv.Free) || seen[pv.UUID] {
			return lvmReport{}, fmt.Errorf("invalid LVM physical volume")
		}
		seen[pv.UUID] = true
	}
	for _, vg := range report.VGs {
		if err := validLVMFields(vg.UUID, vg.Name, vg.Size, vg.Free, vg.Attr); err != nil || vg.UUID == "" || !validLVMSize(vg.Size) || !validLVMSize(vg.Free) || seen[vg.UUID] {
			return lvmReport{}, fmt.Errorf("invalid LVM volume group")
		}
		seen[vg.UUID] = true
	}
	for _, lv := range report.LVs {
		if err := validLVMFields(lv.UUID, lv.VGUUID, lv.Name, lv.VGName, lv.Path, lv.Size, lv.Attr, lv.Data, lv.Metadata); err != nil || lv.UUID == "" || lv.VGUUID == "" || lv.Path == "" || !validLVMSize(lv.Size) || !validLVMPercent(lv.Data) || !validLVMPercent(lv.Metadata) || seen[lv.UUID] {
			return lvmReport{}, fmt.Errorf("invalid LVM logical volume")
		}
		seen[lv.UUID] = true
	}
	return report, nil
}

func validLVMFields(values ...string) error { return validateStrings(values...) }
func validLVMSize(value string) bool        { _, err := strictUint(value); return err == nil }
func validLVMPercent(value string) bool {
	if value == "" {
		return true
	}
	n, err := strconv.ParseFloat(value, 64)
	return err == nil && n >= 0 && n <= 100
}

func lvmResult(pvs, vgs, lvs lvmReport, inventory Inventory) (AdapterResult, error) {
	if len(pvs.PVs)+len(vgs.VGs)+len(lvs.LVs) > maxResources {
		return AdapterResult{}, fmt.Errorf("LVM resources exceed limit")
	}
	vgByUUID := map[string]lvmVG{}
	for _, vg := range vgs.VGs {
		vgByUUID[vg.UUID] = vg
	}
	resourcesByPath := map[string]Resource{}
	for _, resource := range inventory.Resources {
		resourcesByPath[resource.Path] = resource
	}
	result := AdapterResult{}
	missing := map[string]int{}
	for _, pv := range pvs.PVs {
		if _, ok := vgByUUID[pv.VGUUID]; !ok && pv.VGUUID != "" {
			continue
		}
		id := stableID("lvm-pv", pv.UUID)
		result.Resources = append(result.Resources, Resource{ID: id, Kind: "physical-volume", Name: pv.Name, Path: pv.Name, SizeBytes: parseUint(pv.Size), Health: HealthHealthy, State: "available"})
		if core, ok := resourcesByPath[pv.Name]; ok {
			result.Relations = append(result.Relations, Relation{From: core.ID, To: stableID("lvm-vg", pv.VGUUID), Kind: "member-of"})
		}
		if lvmAttrAt(pv.Attr, 2) == 'm' {
			missing[pv.VGUUID]++
		}
	}
	for _, vg := range vgs.VGs {
		id := stableID("lvm-vg", vg.UUID)
		health := HealthHealthy
		if missing[vg.UUID] > 0 {
			health = HealthCritical
			result.Findings = append(result.Findings, Finding{ResourceID: id, Severity: HealthCritical, Title: "LVM volume group has missing devices", Detail: fmt.Sprintf("%d physical volume is missing", missing[vg.UUID])})
		} else if lvmAttrAt(vg.Attr, 3) == 'p' {
			health = HealthCritical
			result.Findings = append(result.Findings, Finding{ResourceID: id, Severity: HealthCritical, Title: "LVM volume group is partial", Detail: "one or more physical volumes are unavailable"})
		}
		result.Resources = append(result.Resources, Resource{ID: id, Kind: "volume-group", Name: vg.Name, SizeBytes: parseUint(vg.Size), Health: health, State: "available", Details: []Detail{{Label: "Free", Value: vg.Free}}})
	}
	for _, lv := range lvs.LVs {
		if _, ok := vgByUUID[lv.VGUUID]; !ok {
			continue
		}
		details := lvmLVDetails(lv)
		state := "inactive"
		health := HealthUnknown
		if lvmAttrAt(lv.Attr, 4) == 'a' {
			state, health = "available", HealthHealthy
		}
		id := stableID("lvm-lv", lv.UUID)
		result.Resources = append(result.Resources, Resource{ID: id, Kind: "logical-volume", Name: lv.VGName + "/" + lv.Name, Path: lv.Path, SizeBytes: parseUint(lv.Size), Health: health, State: state, Details: details})
		result.Relations = append(result.Relations, Relation{From: stableID("lvm-vg", lv.VGUUID), To: id, Kind: "contains"})
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sortRelations(result.Relations)
	return result, nil
}

func lvmAttrAt(value string, index int) byte {
	if len(value) <= index {
		return 0
	}
	return value[index]
}

func lvmLVDetails(lv lvmLV) []Detail {
	details := []Detail{}
	if lv.Data != "" {
		details = append(details, Detail{Label: "Data utilization", Value: lv.Data + "%"})
	}
	if lv.Metadata != "" {
		details = append(details, Detail{Label: "Metadata utilization", Value: lv.Metadata + "%"})
	}
	return details
}
