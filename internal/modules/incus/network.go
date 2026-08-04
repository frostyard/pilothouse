package incus

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
)

// Network is the list-level model for one Incus network. Both managed and
// unmanaged networks are reported: an Incus host's network list is mostly
// unmanaged interfaces it merely observes (physical NICs, loopback, a
// foreign bridge such as docker0), and hiding them would misrepresent what
// the host actually has.
type Network struct {
	IPv4    string `json:"ipv4,omitempty"`
	IPv6    string `json:"ipv6,omitempty"`
	Managed bool   `json:"managed"`
	Name    string `json:"name"`
	Status  string `json:"status,omitempty"`
	Type    string `json:"type"`
	UsedBy  int    `json:"used_by"`
}

// NetworkDetail is the per-network model behind broker.QueryIncusNetwork.
//
// Config is allowlisted for the same reason instance configuration is: an
// Incus network's configuration carries secrets. `bgp.peers.<name>.password`
// is a BGP session password, and the `ovn.*` and `tunnel.*` families carry
// credentials and keys. None of those namespaces is allowlisted, so none of
// them crosses the broker boundary.
type NetworkDetail struct {
	Addresses   []string       `json:"addresses,omitempty"`
	Config      []ConfigEntry  `json:"config"`
	Counters    *TrafficCount  `json:"counters,omitempty"`
	Description string         `json:"description,omitempty"`
	HWAddr      string         `json:"hwaddr,omitempty"`
	Leases      []NetworkLease `json:"leases"`
	// LeasesAvailable distinguishes "no leases" from "leases cannot be
	// read", which is the normal case for an unmanaged network: Incus
	// only tracks leases for networks it manages.
	LeasesAvailable bool     `json:"leases_available"`
	Managed         bool     `json:"managed"`
	MTU             int      `json:"mtu"`
	Name            string   `json:"name"`
	Project         string   `json:"project"`
	State           string   `json:"state,omitempty"`
	Status          string   `json:"status,omitempty"`
	Type            string   `json:"type"`
	UsedBy          []string `json:"used_by"`
}

type NetworkLease struct {
	Address  string `json:"address"`
	Hostname string `json:"hostname,omitempty"`
	HWAddr   string `json:"hwaddr,omitempty"`
	Type     string `json:"type,omitempty"`
}

type TrafficCount struct {
	BytesReceived   int64 `json:"bytes_received"`
	BytesSent       int64 `json:"bytes_sent"`
	PacketsReceived int64 `json:"packets_received"`
	PacketsSent     int64 `json:"packets_sent"`
}

// networkConfigKeys is the exact set of network configuration keys that may
// cross the broker boundary: addressing, NAT, DHCP and DNS shape. Secret
// namespaces are excluded by omission — `bgp.*` (peer passwords), `ovn.*`,
// `tunnel.*` (tunnel keys), `user.*` and `raw.*` are all absent, so a key
// added by a future Incus release is withheld until reviewed.
var networkConfigKeys = []string{
	"bridge.driver",
	"bridge.external_interfaces",
	"bridge.mode",
	"bridge.mtu",
	"dns.domain",
	"dns.mode",
	"dns.search",
	"ipv4.address",
	"ipv4.dhcp",
	"ipv4.dhcp.ranges",
	"ipv4.firewall",
	"ipv4.nat",
	"ipv4.routes",
	"ipv6.address",
	"ipv6.dhcp",
	"ipv6.dhcp.stateful",
	"ipv6.firewall",
	"ipv6.nat",
	"ipv6.routes",
	"mtu",
	"parent",
	"vlan",
}

func allowedNetworkKey(key string) bool {
	return slices.Contains(networkConfigKeys, key)
}

func networks(raw []api.Network) []Network {
	values := make([]Network, 0, len(raw))
	for _, item := range raw {
		values = append(values, Network{
			IPv4:    allowedNetworkValue(item.Config, "ipv4.address"),
			IPv6:    allowedNetworkValue(item.Config, "ipv6.address"),
			Managed: item.Managed, Name: item.Name, Status: item.Status,
			Type: item.Type, UsedBy: len(item.UsedBy),
		})
	}
	slices.SortFunc(values, func(a, b Network) int { return strings.Compare(a.Name, b.Name) })
	return values
}

// allowedNetworkValue reads one key only if it is allowlisted, so the list
// model cannot become a bypass around the detail model's filter.
func allowedNetworkValue(config api.ConfigMap, key string) string {
	if !allowedNetworkKey(key) {
		return ""
	}
	return config[key]
}

// NetworkDetail returns one network's allowlisted configuration, live
// interface state and DHCP leases. A network Incus does not manage has no
// leases to read; that is reported as unavailable rather than as an error,
// since an unmanaged interface is a perfectly ordinary thing to inspect.
func (m *SystemManager) NetworkDetail(ctx context.Context, requestedProject, name string) (NetworkDetail, error) {
	if !validResourceName(name) {
		return NetworkDetail{}, errors.New("invalid network name")
	}
	project, _, err := m.project(ctx, requestedProject)
	if err != nil {
		return NetworkDetail{}, err
	}
	item, err := m.client.Network(ctx, project, name)
	if err != nil {
		return NetworkDetail{}, err
	}
	if item == nil {
		return NetworkDetail{}, errors.New("network no longer exists")
	}
	detail := NetworkDetail{
		Config: allowedNetworkConfig(item.Config), Description: item.Description,
		Leases: []NetworkLease{}, Managed: item.Managed, Name: item.Name,
		Project: project, Status: item.Status, Type: item.Type, UsedBy: item.UsedBy,
	}
	if detail.UsedBy == nil {
		detail.UsedBy = []string{}
	}
	if state, err := m.client.NetworkState(ctx, project, name); err == nil && state != nil {
		detail.HWAddr, detail.MTU, detail.State = state.Hwaddr, state.Mtu, state.State
		for _, address := range state.Addresses {
			if address.Address != "" {
				detail.Addresses = append(detail.Addresses, address.Address+"/"+address.Netmask)
			}
		}
		slices.Sort(detail.Addresses)
		if state.Counters != nil {
			detail.Counters = &TrafficCount{
				BytesReceived: state.Counters.BytesReceived, BytesSent: state.Counters.BytesSent,
				PacketsReceived: state.Counters.PacketsReceived, PacketsSent: state.Counters.PacketsSent,
			}
		}
	}
	if item.Managed {
		if leases, err := m.client.NetworkLeases(ctx, project, name); err == nil {
			detail.LeasesAvailable = true
			detail.Leases = networkLeases(leases)
		}
	}
	return detail, nil
}

func allowedNetworkConfig(config api.ConfigMap) []ConfigEntry {
	entries := make([]ConfigEntry, 0, len(config))
	for key, value := range config {
		if allowedNetworkKey(key) {
			entries = append(entries, ConfigEntry{Key: key, Value: value})
		}
	}
	slices.SortFunc(entries, func(a, b ConfigEntry) int { return strings.Compare(a.Key, b.Key) })
	return entries
}

func networkLeases(raw []api.NetworkLease) []NetworkLease {
	values := make([]NetworkLease, 0, len(raw))
	for _, item := range raw {
		values = append(values, NetworkLease{
			Address: item.Address, Hostname: item.Hostname, HWAddr: item.Hwaddr, Type: item.Type,
		})
	}
	slices.SortFunc(values, func(a, b NetworkLease) int { return strings.Compare(a.Address, b.Address) })
	return values
}
