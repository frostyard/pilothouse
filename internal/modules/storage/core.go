package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var lsblkArgs = []string{"--json", "--bytes", "--output", "NAME,KNAME,PATH,TYPE,MAJ:MIN,PKNAME,SIZE,FSTYPE,FSVER,LABEL,UUID,MOUNTPOINTS,MODEL,SERIAL,ROTA,RM,RO"}
var findmntArgs = []string{"--json", "--list", "--bytes", "--output", "TARGET,SOURCE,FSTYPE,OPTIONS,SIZE,USED,AVAIL,USE%,MAJ:MIN"}

type Adapter interface {
	Collect(context.Context) (AdapterResult, error)
	Core() bool
	Name() string
}

type AdapterResult struct {
	Findings  []Finding
	Mounts    []Mount
	Relations []Relation
	Resources []Resource
	Truncated bool
}

type blockAdapter struct {
	path   string
	runner commandRunner
}

func NewBlockAdapter(path string) Adapter {
	return newBlockAdapter(path)
}

func newBlockAdapter(path string) Adapter {
	return blockAdapter{path: path, runner: commandRunner{limit: maxAdapterBytes}}
}

func (a blockAdapter) Collect(ctx context.Context) (AdapterResult, error) {
	output, err := a.runner.Run(ctx, a.path, lsblkArgs...)
	if err != nil {
		return AdapterResult{}, err
	}
	return parseLSBLK(output)
}

func (blockAdapter) Core() bool   { return true }
func (blockAdapter) Name() string { return "block" }

type mountAdapter struct {
	path   string
	runner commandRunner
}

func NewMountAdapter(path string) Adapter {
	return newMountAdapter(path)
}

func newMountAdapter(path string) Adapter {
	return mountAdapter{path: path, runner: commandRunner{limit: maxAdapterBytes}}
}

func (a mountAdapter) Collect(ctx context.Context) (AdapterResult, error) {
	output, err := a.runner.Run(ctx, a.path, findmntArgs...)
	if err != nil {
		return AdapterResult{}, err
	}
	return parseFindmnt(output)
}

func (mountAdapter) Core() bool   { return true }
func (mountAdapter) Name() string { return "mount" }

type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Children    []lsblkDevice `json:"children"`
	FSVersion   *string       `json:"fsver"`
	FSType      *string       `json:"fstype"`
	KName       string        `json:"kname"`
	Label       *string       `json:"label"`
	MajorMinor  string        `json:"maj:min"`
	Model       *string       `json:"model"`
	Mountpoints []*string     `json:"mountpoints"`
	Name        string        `json:"name"`
	ParentName  *string       `json:"pkname"`
	Path        string        `json:"path"`
	ReadOnly    bool          `json:"ro"`
	Removable   bool          `json:"rm"`
	Rotational  bool          `json:"rota"`
	Serial      *string       `json:"serial"`
	Size        decimal       `json:"size"`
	Type        string        `json:"type"`
	UUID        *string       `json:"uuid"`
}

func parseLSBLK(input []byte) (AdapterResult, error) {
	var output lsblkOutput
	if err := decodeStrict(input, &output); err != nil {
		return AdapterResult{}, fmt.Errorf("decode lsblk: %w", err)
	}
	result := AdapterResult{}
	resources := make(map[string]Resource)
	resourceCount := 0
	var walk func(lsblkDevice, string) error
	walk = func(device lsblkDevice, parentID string) error {
		if err := validateBlockDevice(device); err != nil {
			return err
		}
		if resourceCount >= maxResources {
			return fmt.Errorf("lsblk resources exceed limit")
		}
		resourceCount++
		kind := blockKind(device.Type)
		id := stableID(kind, device.MajorMinor)
		resources[id] = Resource{Details: blockDetails(device), Health: HealthHealthy, ID: id, Kind: kind, Name: device.Name, Path: device.Path, SizeBytes: parseUint(string(device.Size)), State: blockState(device)}
		if parentID != "" {
			result.Relations = append(result.Relations, Relation{From: parentID, To: id, Kind: "contains"})
		}
		if device.FSType != nil && *device.FSType != "" {
			filesystemID := stableID("filesystem", device.MajorMinor)
			if resourceCount >= maxResources {
				return fmt.Errorf("lsblk resources exceed limit")
			}
			resourceCount++
			resources[filesystemID] = Resource{Details: filesystemDetails(device), Health: HealthHealthy, ID: filesystemID, Kind: "filesystem", Name: *device.FSType, SizeBytes: parseUint(string(device.Size)), State: "available"}
			result.Relations = append(result.Relations, Relation{From: id, To: filesystemID, Kind: "contains"})
		}
		for _, child := range device.Children {
			if err := walk(child, id); err != nil {
				return err
			}
		}
		return nil
	}
	for _, device := range output.BlockDevices {
		if err := walk(device, ""); err != nil {
			return AdapterResult{}, err
		}
	}
	for _, resource := range resources {
		result.Resources = append(result.Resources, resource)
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sortRelations(result.Relations)
	return result, nil
}

// sortRelations orders the From, To, and Kind tuple deterministically.
func sortRelations(relations []Relation) {
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].From != relations[j].From {
			return relations[i].From < relations[j].From
		}
		if relations[i].To != relations[j].To {
			return relations[i].To < relations[j].To
		}
		return relations[i].Kind < relations[j].Kind
	})
}

func validateBlockDevice(device lsblkDevice) error {
	if err := validateStrings(device.Name, device.KName, device.Path, device.Type, device.MajorMinor, string(device.Size)); err != nil {
		return err
	}
	if !validMajorMinor(device.MajorMinor) {
		return fmt.Errorf("invalid block MAJ:MIN")
	}
	if _, err := strictUint(string(device.Size)); err != nil {
		return fmt.Errorf("invalid block size: %w", err)
	}
	for _, value := range []*string{device.FSVersion, device.FSType, device.Label, device.Model, device.ParentName, device.Serial, device.UUID} {
		if value != nil && len(*value) > maxFieldBytes {
			return fmt.Errorf("field exceeds limit")
		}
	}
	for _, value := range device.Mountpoints {
		if value != nil && len(*value) > maxFieldBytes {
			return fmt.Errorf("field exceeds limit")
		}
	}
	return nil
}

func blockKind(value string) string {
	if value == "part" {
		return "partition"
	}
	return value
}

func blockState(device lsblkDevice) string {
	if device.ReadOnly {
		return "read-only"
	}
	return "available"
}

func blockDetails(device lsblkDevice) []Detail {
	var details []Detail
	for _, pair := range []struct {
		label string
		value *string
	}{{"Model", device.Model}, {"Serial", device.Serial}} {
		if pair.value != nil && *pair.value != "" {
			details = append(details, Detail{Label: pair.label, Value: *pair.value})
		}
	}
	return details
}

func filesystemDetails(device lsblkDevice) []Detail {
	var details []Detail
	for _, pair := range []struct {
		label string
		value *string
	}{{"UUID", device.UUID}, {"Label", device.Label}, {"Version", device.FSVersion}} {
		if pair.value != nil && *pair.value != "" {
			details = append(details, Detail{Label: pair.label, Value: *pair.value})
		}
	}
	return details
}

type findmntOutput struct {
	Filesystems []findmntFilesystem `json:"filesystems"`
}

type findmntFilesystem struct {
	Available  decimal `json:"avail"`
	Filesystem string  `json:"fstype"`
	MajorMinor *string `json:"maj:min"`
	Options    string  `json:"options"`
	Size       decimal `json:"size"`
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	Used       decimal `json:"used"`
	UsedPct    string  `json:"use%"`
}

type decimal string

func (d *decimal) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, (*string)(d))
	}
	*d = decimal(data)
	return nil
}

func parseFindmnt(input []byte) (AdapterResult, error) {
	var output findmntOutput
	if err := decodeStrict(input, &output); err != nil {
		return AdapterResult{}, fmt.Errorf("decode findmnt: %w", err)
	}
	if len(output.Filesystems) > maxMounts {
		return AdapterResult{}, fmt.Errorf("findmnt mounts exceed limit")
	}
	result := AdapterResult{Mounts: make([]Mount, 0, len(output.Filesystems))}
	for _, filesystem := range output.Filesystems {
		mount, err := mountFromFilesystem(filesystem)
		if err != nil {
			return AdapterResult{}, err
		}
		result.Mounts = append(result.Mounts, mount)
	}
	sort.Slice(result.Mounts, func(i, j int) bool { return result.Mounts[i].Target < result.Mounts[j].Target })
	return result, nil
}

func mountFromFilesystem(filesystem findmntFilesystem) (Mount, error) {
	if err := validateStrings(string(filesystem.Available), filesystem.Filesystem, filesystem.Options, string(filesystem.Size), filesystem.Source, filesystem.Target, string(filesystem.Used), filesystem.UsedPct); err != nil {
		return Mount{}, err
	}
	total, used, available, percent, err := mountCapacity(filesystem)
	if err != nil {
		return Mount{}, err
	}
	resourceID := ""
	if filesystem.MajorMinor != nil {
		if len(*filesystem.MajorMinor) > maxFieldBytes || !validMajorMinor(*filesystem.MajorMinor) {
			return Mount{}, fmt.Errorf("invalid mount MAJ:MIN")
		}
		resourceID = stableID("filesystem", *filesystem.MajorMinor)
	}
	readOnly := false
	options := safeOptions(filesystem.Options)
	for _, option := range options {
		if option == "ro" {
			readOnly = true
		}
	}
	identity := filesystem.Target
	if resourceID == "" {
		identity = filesystem.Filesystem + "\x00" + filesystem.Source + "\x00" + filesystem.Target
	}
	return Mount{AvailableBytes: available, Filesystem: filesystem.Filesystem, Health: HealthHealthy, ID: stableID("mount", identity), Options: options, ReadOnly: readOnly, ResourceID: resourceID, Source: filesystem.Source, State: "mounted", Target: filesystem.Target, TotalBytes: total, UsedBytes: used, UsedPercent: percent}, nil
}

func mountCapacity(filesystem findmntFilesystem) (uint64, uint64, uint64, float64, error) {
	values := []string{string(filesystem.Size), string(filesystem.Used), string(filesystem.Available)}
	if unavailableCapacity(filesystem.UsedPct) {
		for _, value := range values {
			if unavailableCapacity(value) || value == "0" {
				continue
			}
			return 0, 0, 0, 0, fmt.Errorf("inconsistent unknown mount capacity")
		}
		return 0, 0, 0, 0, nil
	}
	for _, value := range values {
		if unavailableCapacity(value) {
			return 0, 0, 0, 0, fmt.Errorf("inconsistent unknown mount capacity")
		}
	}
	total, err := strictUint(values[0])
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid mount size: %w", err)
	}
	used, err := strictUint(values[1])
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid mount used: %w", err)
	}
	available, err := strictUint(values[2])
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid mount available: %w", err)
	}
	percent, err := strictPercent(filesystem.UsedPct)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid mount use%%: %w", err)
	}
	return total, used, available, percent, nil
}

func unavailableCapacity(value string) bool { return value == "" || value == "-" || value == "null" }

func decodeStrict(input []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func validateStrings(values ...string) error {
	for _, value := range values {
		if len(value) > maxFieldBytes {
			return fmt.Errorf("field exceeds limit")
		}
	}
	return nil
}

func strictUint(value string) (uint64, error) {
	if value == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("not decimal")
		}
	}
	return strconv.ParseUint(value, 10, 64)
}

func parseUint(value string) uint64 { parsed, _ := strictUint(value); return parsed }

func strictPercent(value string) (float64, error) {
	if !strings.HasSuffix(value, "%") {
		return 0, fmt.Errorf("missing percent")
	}
	parsed, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
	if err != nil || parsed < 0 || parsed > 100 {
		return 0, fmt.Errorf("invalid")
	}
	return parsed, nil
}

func validMajorMinor(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	_, firstErr := strictUint(parts[0])
	_, secondErr := strictUint(parts[1])
	return firstErr == nil && secondErr == nil
}

func safeOptions(value string) []string {
	allowed := map[string]bool{"ro": true, "rw": true, "nosuid": true, "nodev": true, "noexec": true, "relatime": true, "bind": true}
	var options []string
	for _, option := range strings.Split(value, ",") {
		if allowed[option] {
			options = append(options, option)
		}
	}
	sort.Strings(options)
	return options
}
