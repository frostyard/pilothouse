package incus

import (
	"slices"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

// Detail is the per-instance presentation model behind
// broker.QueryIncusInstance.
//
// Its Config and Devices fields are built by allowlist, never by copying
// the instance's expanded configuration wholesale. An Incus instance's
// configuration routinely carries secrets — `user.user-data` holds
// cloud-init payloads with SSH keys and passwords, `environment.*` holds
// process environment, and `raw.*` holds passthrough runtime
// configuration — and docs/modules.md forbids exposing instance
// environment variables or secrets to the web process. Only the keys in
// configKeys/configPrefixes and the per-type device properties in
// deviceProperties cross the broker boundary; everything else is dropped,
// so a key added by a future Incus release is excluded until it is
// reviewed and added here.
type Detail struct {
	Architecture string        `json:"architecture"`
	Config       []ConfigEntry `json:"config"`
	CreatedAt    string        `json:"created_at,omitempty"`
	Description  string        `json:"description,omitempty"`
	Devices      []Device      `json:"devices"`
	Instance     Instance      `json:"instance"`
	LastUsedAt   string        `json:"last_used_at,omitempty"`
	Location     string        `json:"location,omitempty"`
	MemoryTotal  uint64        `json:"memory_total"`
	Networks     []Interface   `json:"networks"`
	Profiles     []string      `json:"profiles"`
	Project      string        `json:"project"`
	Snapshots    []Snapshot    `json:"snapshots"`
	Stateful     bool          `json:"stateful"`
}

type ConfigEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Device struct {
	Name       string        `json:"name"`
	Properties []ConfigEntry `json:"properties"`
	Type       string        `json:"type"`
}

type Interface struct {
	Addresses []string `json:"addresses,omitempty"`
	HWAddr    string   `json:"hwaddr,omitempty"`
	MTU       int      `json:"mtu"`
	Name      string   `json:"name"`
	State     string   `json:"state,omitempty"`
}

type Snapshot struct {
	CreatedAt string `json:"created_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Name      string `json:"name"`
	Stateful  bool   `json:"stateful"`
}

// configKeys is the exact set of single configuration keys that may cross
// the broker boundary. Every key here is instance shape or posture, never
// payload: no cloud-init data, no environment, no raw passthrough.
var configKeys = []string{
	"boot.autostart",
	"boot.autostart.delay",
	"boot.autostart.priority",
	"limits.cpu",
	"limits.disk.priority",
	"limits.memory",
	"limits.memory.swap",
	"limits.processes",
	"security.nesting",
	"security.privileged",
	"security.protection.delete",
	"volatile.base_image",
}

// configPrefixes covers the image.* family, which describes which image an
// instance was created from (os, release, architecture, variant, serial,
// description). Incus writes these itself at creation time from image
// metadata; they are not a user-controlled free-form namespace like
// `user.*`.
var configPrefixes = []string{"image."}

// deviceProperties is the per-device-type allowlist of properties. A device
// type absent from this map contributes its name and type only, so a device
// kind added by a future Incus release exposes no properties until it is
// reviewed and added here.
var deviceProperties = map[string][]string{
	"disk":       {"path", "pool", "readonly", "size", "source"},
	"gpu":        {"gputype", "pci"},
	"nic":        {"hwaddr", "name", "network", "nictype", "parent"},
	"proxy":      {"connect", "listen"},
	"unix-block": {"path", "source"},
	"unix-char":  {"path", "source"},
}

func allowedConfigKey(key string) bool {
	if slices.Contains(configKeys, key) {
		return true
	}
	return slices.ContainsFunc(configPrefixes, func(prefix string) bool {
		return strings.HasPrefix(key, prefix)
	})
}

// detail projects one API instance onto the detail model, applying the
// config and device allowlists.
func detail(item api.InstanceFull, project string) Detail {
	value := Detail{
		Architecture: item.Architecture, Config: allowedConfig(item.ExpandedConfig, item.Config),
		Description: item.Description, Devices: allowedDevices(item.ExpandedDevices, item.Devices),
		Instance: listInstance(item), Location: item.Location, Profiles: item.Profiles,
		Project: project, Snapshots: snapshots(item.Snapshots), Stateful: item.Stateful,
	}
	if value.Profiles == nil {
		value.Profiles = []string{}
	}
	if !item.CreatedAt.IsZero() {
		value.CreatedAt = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !item.LastUsedAt.IsZero() {
		value.LastUsedAt = item.LastUsedAt.UTC().Format(time.RFC3339)
	}
	if item.State != nil {
		value.Networks = interfaces(item.State)
		if item.State.Memory.Total > 0 {
			value.MemoryTotal = uint64(item.State.Memory.Total)
		}
	}
	return value
}

// allowedConfig merges the expanded and instance-local configuration —
// expanded wins, since it is what the instance actually runs with — and
// keeps only allowlisted keys.
func allowedConfig(expanded, local api.ConfigMap) []ConfigEntry {
	merged := map[string]string{}
	for key, value := range local {
		if allowedConfigKey(key) {
			merged[key] = value
		}
	}
	for key, value := range expanded {
		if allowedConfigKey(key) {
			merged[key] = value
		}
	}
	entries := make([]ConfigEntry, 0, len(merged))
	for key, value := range merged {
		entries = append(entries, ConfigEntry{Key: key, Value: value})
	}
	slices.SortFunc(entries, func(a, b ConfigEntry) int { return strings.Compare(a.Key, b.Key) })
	return entries
}

func allowedDevices(expanded, local map[string]map[string]string) []Device {
	merged := map[string]map[string]string{}
	for name, properties := range local {
		merged[name] = properties
	}
	for name, properties := range expanded {
		merged[name] = properties
	}
	devices := make([]Device, 0, len(merged))
	for name, properties := range merged {
		devices = append(devices, Device{Name: name, Properties: allowedProperties(properties), Type: properties["type"]})
	}
	slices.SortFunc(devices, func(a, b Device) int { return strings.Compare(a.Name, b.Name) })
	return devices
}

func allowedProperties(properties map[string]string) []ConfigEntry {
	allowed := deviceProperties[properties["type"]]
	entries := make([]ConfigEntry, 0, len(allowed))
	for _, key := range allowed {
		if value := properties[key]; value != "" {
			entries = append(entries, ConfigEntry{Key: key, Value: value})
		}
	}
	slices.SortFunc(entries, func(a, b ConfigEntry) int { return strings.Compare(a.Key, b.Key) })
	return entries
}

func interfaces(state *api.InstanceState) []Interface {
	values := make([]Interface, 0, len(state.Network))
	for name, network := range state.Network {
		value := Interface{HWAddr: network.Hwaddr, MTU: network.Mtu, Name: name, State: network.State}
		for _, address := range network.Addresses {
			if address.Address != "" {
				value.Addresses = append(value.Addresses, address.Address+"/"+address.Netmask)
			}
		}
		slices.Sort(value.Addresses)
		values = append(values, value)
	}
	slices.SortFunc(values, func(a, b Interface) int { return strings.Compare(a.Name, b.Name) })
	return values
}

func snapshots(raw []api.InstanceSnapshot) []Snapshot {
	values := make([]Snapshot, 0, len(raw))
	for _, item := range raw {
		value := Snapshot{Name: snapshotName(item.Name), Stateful: item.Stateful}
		if !item.CreatedAt.IsZero() {
			value.CreatedAt = item.CreatedAt.UTC().Format(time.RFC3339)
		}
		if !item.ExpiresAt.IsZero() {
			value.ExpiresAt = item.ExpiresAt.UTC().Format(time.RFC3339)
		}
		values = append(values, value)
	}
	slices.SortFunc(values, func(a, b Snapshot) int { return strings.Compare(a.Name, b.Name) })
	return values
}

// snapshotName trims the "instance/" prefix Incus uses for snapshot names
// so the rendered name matches what an operator types.
func snapshotName(name string) string {
	if _, snapshot, found := strings.Cut(name, "/"); found {
		return snapshot
	}
	return name
}
