package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const mdraidEnricherName = "mdraid"

var (
	mdArrayName   = regexp.MustCompile(`^md[0-9]+$`)
	mdMember      = regexp.MustCompile(`^([^\[\]\s]+)\[([0-9]+)\](?:\([A-Z]+\))?$`)
	mdMemberCount = regexp.MustCompile(`\[([0-9]+)/([0-9]+)\]`)
	mdRecovery    = regexp.MustCompile(`(?:recovery|resync|reshape|check)\s*=\s*([0-9]+(?:\.[0-9]+)?)%`)
)

type mdArray struct {
	name     string
	level    string
	expected int
	active   int
	members  []string
	recovery float64
	state    string
}

type mdraidEnricher struct {
	root     string
	mdadm    string
	readFile func(string) ([]byte, error)
	runner   commandRunner
}

func NewMDRAIDEnricher(root, mdadmPath string) Enricher { return newMDRAIDEnricher(root, mdadmPath) }

func newMDRAIDEnricher(root, mdadmPath string) *mdraidEnricher {
	return &mdraidEnricher{root: root, mdadm: mdadmPath, readFile: readMDStat, runner: commandRunner{limit: maxAdapterBytes}}
}

func (*mdraidEnricher) Name() string { return mdraidEnricherName }

func (e *mdraidEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	input, err := e.readFile(filepath.Join(e.root, "proc", "mdstat"))
	if err != nil {
		return AdapterResult{}, fmt.Errorf("read mdstat: %w", err)
	}
	if len(input) > maxAdapterBytes {
		return AdapterResult{}, errOutputTooLarge
	}
	arrays, err := parseMDStat(input)
	if err != nil {
		return AdapterResult{}, err
	}

	resources := make(map[string]Resource, len(inventory.Resources))
	for _, resource := range inventory.Resources {
		resources[resource.Path] = resource
	}
	result := AdapterResult{}
	for _, array := range arrays {
		path := "/dev/" + array.name
		detailOutput, err := e.runner.Run(ctx, e.mdadm, "--detail", "--export", path)
		if err != nil {
			return AdapterResult{}, fmt.Errorf("read MD detail for %s: %w", path, err)
		}
		detail, err := parseMDDetail(detailOutput, array.name)
		if err != nil {
			return AdapterResult{}, err
		}
		if array.level != "" && detail.level != array.level {
			return AdapterResult{}, fmt.Errorf("MD detail level does not match %s", path)
		}

		raidID := stableID("raid", array.name)
		if resource, ok := resources[path]; ok && (resource.Kind == "raid" || resource.Kind == "mapping") {
			raidID = resource.ID
		}
		expected := array.expected
		if expected == 0 {
			expected = detail.devices
		}
		health, state := HealthHealthy, array.state
		switch state {
		case "inactive":
			health = HealthUnknown
		case "auto-read-only":
			health = HealthWarning
		}
		if state != "inactive" && expected > 0 && array.active < expected {
			health, state = HealthCritical, "degraded"
			result.Findings = append(result.Findings, Finding{ResourceID: raidID, Severity: HealthCritical, Title: "RAID array is degraded", Detail: fmt.Sprintf("%d of %d members active", array.active, expected)})
		}
		details := []Detail{{Label: "Level", Value: detail.level}, {Label: "RAID members", Value: fmt.Sprintf("%d of %d active", array.active, expected)}}
		if array.recovery != 0 {
			details = append(details, Detail{Label: "Recovery progress", Value: strconv.FormatFloat(array.recovery, 'f', 1, 64) + "%"})
		}
		result.Resources = append(result.Resources, Resource{ID: raidID, Kind: "raid", Name: array.name, Path: path, Health: health, State: state, Details: details})
		for _, member := range detail.members {
			if resource, ok := resources[member]; ok {
				result.Relations = append(result.Relations, Relation{From: resource.ID, To: raidID, Kind: "member-of"})
			}
		}
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sortRelations(result.Relations)
	return result, nil
}

type mdDetail struct {
	devices int
	level   string
	members []string
}

func parseMDDetail(input []byte, name string) (mdDetail, error) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(input), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || len(key) > maxFieldBytes || len(value) > maxFieldBytes {
			return mdDetail{}, fmt.Errorf("invalid MD detail")
		}
		switch key {
		case "MD_LEVEL", "MD_DEVICES", "MD_DEVNAME":
			values[key] = value
		default:
			if strings.HasPrefix(key, "MD_DEVICE_") && strings.HasSuffix(key, "_DEV") && strings.HasPrefix(value, "/dev/") {
				values[key] = value
			}
		}
	}
	if values["MD_DEVNAME"] != name {
		return mdDetail{}, fmt.Errorf("MD detail does not match %s", name)
	}
	devices, err := strconv.Atoi(values["MD_DEVICES"])
	if values["MD_LEVEL"] == "" || err != nil || devices <= 0 {
		return mdDetail{}, fmt.Errorf("invalid MD detail")
	}
	members := make([]string, 0)
	for key, value := range values {
		if strings.HasPrefix(key, "MD_DEVICE_") && strings.HasSuffix(key, "_DEV") {
			members = append(members, value)
		}
	}
	sort.Strings(members)
	return mdDetail{devices: devices, level: values["MD_LEVEL"], members: members}, nil
}

func readMDStat(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxAdapterBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAdapterBytes {
		return nil, errOutputTooLarge
	}
	return data, nil
}

func parseMDStat(input []byte) ([]mdArray, error) {
	var arrays []mdArray
	seen := make(map[string]bool)
	var current *mdArray
	for _, line := range strings.Split(string(input), "\n") {
		if len(line) > maxFieldBytes {
			return nil, fmt.Errorf("mdstat line exceeds limit")
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == ":" {
			current = nil
			if !mdArrayName.MatchString(fields[0]) || seen[fields[0]] || len(fields) < 3 {
				continue
			}
			if len(arrays) >= maxResources {
				return nil, fmt.Errorf("MD arrays exceed limit")
			}
			array := mdArray{name: fields[0]}
			memberStart := 0
			switch fields[2] {
			case "active":
				array.state = "available"
				memberStart = 3
				if memberStart < len(fields) && fields[memberStart] == "(auto-read-only)" {
					array.state = "auto-read-only"
					memberStart++
				}
				if memberStart >= len(fields) {
					continue
				}
				array.level = fields[memberStart]
				memberStart++
			case "inactive":
				array.state = "inactive"
				memberStart = 3
			default:
				continue
			}
			valid := true
			for _, field := range fields[memberStart:] {
				if !strings.Contains(field, "[") {
					continue
				}
				match := mdMember.FindStringSubmatch(field)
				if match == nil {
					valid = false
					break
				}
				array.members = append(array.members, match[1])
			}
			if !valid || len(array.members) == 0 {
				continue
			}
			arrays, seen[array.name] = append(arrays, array), true
			current = &arrays[len(arrays)-1]
			continue
		}
		if current == nil {
			continue
		}
		if match := mdMemberCount.FindStringSubmatch(line); match != nil {
			expected, _ := strconv.Atoi(match[1])
			active, _ := strconv.Atoi(match[2])
			current.expected, current.active = expected, active
		}
		if match := mdRecovery.FindStringSubmatch(line); match != nil {
			recovery, _ := strconv.ParseFloat(match[1], 64)
			current.recovery = recovery
		}
	}
	for _, array := range arrays {
		if (array.state != "inactive" && (array.level == "" || array.expected == 0)) || (array.expected > 0 && array.active > array.expected) {
			return nil, fmt.Errorf("invalid MD array %s", array.name)
		}
	}
	return arrays, nil
}
