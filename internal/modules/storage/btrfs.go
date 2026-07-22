package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const btrfsEnricherName = "btrfs"

type btrfsEnricher struct {
	path   string
	runner commandRunner
}
type btrfsFilesystem struct {
	uuid          string
	size, missing uint64
	devices       []string
	errors        map[string]uint64
	subvolumes    []btrfsSubvolume
}
type btrfsSubvolume struct{ id, parent, path string }

func NewBtrfsEnricher(path string) Enricher { return newBtrfsEnricher(path) }
func newBtrfsEnricher(path string) *btrfsEnricher {
	return &btrfsEnricher{path: path, runner: commandRunner{limit: maxAdapterBytes}}
}
func (*btrfsEnricher) Name() string { return btrfsEnricherName }

func (e *btrfsEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	filesystems := make([]btrfsFilesystem, 0)
	seen := map[string]bool{}
	for _, mount := range inventory.Mounts {
		if mount.Filesystem != "btrfs" || mount.State != "mounted" || mount.Target != filepath.Clean(mount.Target) || !strings.HasPrefix(mount.Target, "/") {
			continue
		}
		uuid := btrfsMountUUID(mount, inventory.Resources)
		if uuid == "" {
			continue
		}
		// Subvolume layouts mount one filesystem at several targets; enrich the
		// filesystem once and let btrfsResult attach every matching mount.
		if seen[uuid] {
			continue
		}
		usage, err := e.runner.Run(ctx, e.path, "filesystem", "usage", "-b", "--raw", mount.Target)
		if err != nil {
			return AdapterResult{}, fmt.Errorf("read Btrfs usage: %w", err)
		}
		fs, err := parseBtrfsUsage(usage, uuid)
		if err != nil {
			return AdapterResult{}, err
		}
		stats, err := e.runner.Run(ctx, e.path, "device", "stats", mount.Target)
		if err != nil {
			return AdapterResult{}, fmt.Errorf("read Btrfs stats: %w", err)
		}
		fs.errors, err = parseBtrfsStats(stats)
		if err != nil {
			return AdapterResult{}, err
		}
		subvolumes, err := e.runner.Run(ctx, e.path, "subvolume", "list", "-o", mount.Target)
		if err != nil {
			return AdapterResult{}, fmt.Errorf("read Btrfs subvolumes: %w", err)
		}
		fs.subvolumes, err = parseBtrfsSubvolumes(subvolumes)
		if err != nil {
			return AdapterResult{}, err
		}
		if device := btrfsSourceDevice(mount.Source); strings.HasPrefix(device, "/dev/") && !containsString(fs.devices, device) {
			return AdapterResult{}, fmt.Errorf("btrfs usage does not identify mount device")
		}
		fs.uuid, seen[uuid] = uuid, true
		filesystems = append(filesystems, fs)
	}
	return btrfsResult(filesystems, inventory)
}

func btrfsMountUUID(mount Mount, resources []Resource) string {
	if uuid, ok := strings.CutPrefix(mount.Source, "UUID="); ok && uuid != "" && validateStrings(uuid) == nil {
		return uuid
	}
	device := btrfsSourceDevice(mount.Source)
	for _, resource := range resources {
		if resource.Path != device {
			continue
		}
		for _, detail := range resource.Details {
			if detail.Label == "UUID" && detail.Value != "" && validateStrings(detail.Value) == nil {
				return detail.Value
			}
		}
	}
	return ""
}

// btrfsSourceDevice strips the "[/subvolume]" suffix mount tables append to
// subvolume mount sources.
func btrfsSourceDevice(source string) string {
	device, _, _ := strings.Cut(source, "[")
	return device
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func parseBtrfsUsage(input []byte, expected string) (btrfsFilesystem, error) {
	if len(input) > maxAdapterBytes {
		return btrfsFilesystem{}, errOutputTooLarge
	}
	fs := btrfsFilesystem{errors: map[string]uint64{}}
	for _, line := range strings.Split(string(input), "\n") {
		if len(line) > maxFieldBytes {
			return btrfsFilesystem{}, fmt.Errorf("btrfs usage line exceeds limit")
		}
		line = strings.TrimSpace(line)
		if uuid, ok := strings.CutPrefix(line, "UUID: "); ok {
			if uuid != expected {
				return btrfsFilesystem{}, fmt.Errorf("btrfs usage identifies unexpected filesystem")
			}
			fs.uuid = uuid
			continue
		}
		if value, ok := strings.CutPrefix(line, "Device size:"); ok {
			n, err := strictUint(strings.TrimSpace(value))
			if err != nil || n == 0 {
				return btrfsFilesystem{}, fmt.Errorf("invalid Btrfs device size")
			}
			fs.size = n
			continue
		}
		if value, ok := strings.CutPrefix(line, "Device missing:"); ok {
			n, err := strictUint(strings.TrimSpace(value))
			if err != nil {
				return btrfsFilesystem{}, fmt.Errorf("invalid Btrfs missing devices")
			}
			fs.missing = n
			continue
		}
		if strings.HasPrefix(line, "/dev/") {
			fields := strings.Fields(line)
			if len(fields) != 2 || validateStrings(fields...) != nil {
				return btrfsFilesystem{}, fmt.Errorf("invalid Btrfs device")
			}
			if _, err := strictUint(fields[1]); err != nil {
				return btrfsFilesystem{}, fmt.Errorf("invalid Btrfs device")
			}
			fs.devices = append(fs.devices, fields[0])
		}
	}
	if (fs.uuid != "" && fs.uuid != expected) || fs.size == 0 || len(fs.devices) == 0 {
		return btrfsFilesystem{}, fmt.Errorf("invalid Btrfs usage")
	}
	return fs, nil
}

func parseBtrfsStats(input []byte) (map[string]uint64, error) {
	errors := map[string]uint64{}
	for _, line := range strings.Split(strings.TrimSpace(string(input)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) > maxFieldBytes || validateStrings(fields...) != nil {
			return nil, fmt.Errorf("invalid Btrfs device stats")
		}
		device, _, ok := strings.Cut(strings.TrimPrefix(fields[0], "["), "].")
		if !ok || !strings.HasPrefix(device, "/dev/") {
			return nil, fmt.Errorf("invalid Btrfs device stats")
		}
		n, err := strictUint(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid Btrfs device stats")
		}
		errors[device] += n
	}
	return errors, nil
}

func parseBtrfsSubvolumes(input []byte) ([]btrfsSubvolume, error) {
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" {
		return nil, nil
	}
	var result []btrfsSubvolume
	seen := map[string]bool{}
	for _, line := range strings.Split(trimmed, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] != "ID" || fields[2] != "gen" || fields[4] != "top" || fields[5] != "level" || fields[7] != "path" || len(result) >= maxResources {
			return nil, fmt.Errorf("invalid Btrfs subvolume")
		}
		if _, err := strictUint(fields[1]); err != nil || seen[fields[1]] || validateStrings(fields[1], fields[6]) != nil {
			return nil, fmt.Errorf("invalid Btrfs subvolume")
		}
		path := strings.Join(fields[8:], " ")
		if path == "" || validateStrings(path) != nil {
			return nil, fmt.Errorf("invalid Btrfs subvolume")
		}
		seen[fields[1]] = true
		result = append(result, btrfsSubvolume{id: fields[1], parent: fields[6], path: path})
	}
	return result, nil
}

func btrfsResult(filesystems []btrfsFilesystem, inventory Inventory) (AdapterResult, error) {
	result := AdapterResult{}
	seen := map[string]bool{}
	for _, fs := range filesystems {
		if fs.uuid == "" || seen[fs.uuid] {
			return AdapterResult{}, fmt.Errorf("duplicate Btrfs filesystem UUID")
		}
		seen[fs.uuid] = true
		id, health := stableID("btrfs-filesystem", fs.uuid), HealthHealthy
		if fs.missing > 0 {
			health = HealthCritical
			result.Findings = append(result.Findings, Finding{ResourceID: id, Severity: health, Title: "Btrfs filesystem has missing devices", Detail: fmt.Sprintf("%d devices missing", fs.missing)})
		}
		for _, count := range fs.errors {
			if count > 0 && health != HealthCritical {
				health = HealthWarning
			}
		}
		result.Resources = append(result.Resources, Resource{ID: id, Kind: "btrfs-filesystem", Name: fs.uuid, SizeBytes: fs.size, Health: health, State: "mounted"})
		for _, device := range fs.devices {
			deviceID := stableID("btrfs-device", fs.uuid+":"+device)
			result.Resources = append(result.Resources, Resource{ID: deviceID, Kind: "btrfs-device", Name: device, Path: device, Health: health, State: "available", Details: []Detail{{Label: "Btrfs device errors", Value: fmt.Sprint(fs.errors[device])}}})
			result.Relations = append(result.Relations, Relation{From: deviceID, To: id, Kind: "member-of"})
			if count := fs.errors[device]; count > 0 {
				result.Findings = append(result.Findings, Finding{ResourceID: id, Severity: HealthWarning, Title: "Btrfs device reports errors", Detail: fmt.Sprintf("%s has %d errors", device, count)})
			}
		}
		for _, subvolume := range fs.subvolumes {
			subvolumeID := stableID("btrfs-subvolume", fs.uuid+":"+subvolume.id)
			result.Resources = append(result.Resources, Resource{ID: subvolumeID, Kind: "btrfs-subvolume", Name: subvolume.path, Health: HealthHealthy, State: "available"})
			result.Relations = append(result.Relations, Relation{From: id, To: subvolumeID, Kind: "contains"})
		}
		for _, mount := range inventory.Mounts {
			if mount.Filesystem == "btrfs" && mount.State == "mounted" && btrfsMountUUID(mount, inventory.Resources) == fs.uuid {
				mount.ResourceID = id
				result.Mounts = append(result.Mounts, mount)
			}
		}
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sortRelations(result.Relations)
	return result, nil
}
