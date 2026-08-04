package incus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStateReportsManagedAndUnmanagedNetworks proves both kinds appear. An
// Incus host's network list is mostly interfaces it merely observes, and
// hiding them would misrepresent the host.
func TestStateReportsManagedAndUnmanagedNetworks(t *testing.T) {
	state, err := NewSystemManager(stateClient()).State(context.Background(), "production")
	require.NoError(t, err)

	assert.Equal(t, []Network{
		{Managed: false, Name: "docker0", Type: "bridge"},
		{IPv4: "10.209.192.1/24", IPv6: "none", Managed: true, Name: "incusbr0", Status: "Created", Type: "bridge", UsedBy: 2},
	}, state.Networks)
}

func TestStateReportsProfiles(t *testing.T) {
	state, err := NewSystemManager(stateClient()).State(context.Background(), "production")
	require.NoError(t, err)
	assert.Equal(t, []Profile{
		{Description: "Default Incus profile", Devices: 2, Name: "default", UsedBy: 1},
	}, state.Profiles)
}

// TestNetworkAndProfileReadsDegradeIndependently proves an unreadable
// network or profile list costs its own section plus a warning, never the
// page — matching how storage already degrades.
func TestNetworkAndProfileReadsDegradeIndependently(t *testing.T) {
	client := stateClient()
	client.networksError = errors.New("connection failed")
	state, err := NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err)
	assert.Equal(t, []string{"Networks are unavailable."}, state.Warnings)
	assert.Empty(t, state.Networks)
	assert.Len(t, state.Profiles, 1, "profiles are unaffected")
	assert.Len(t, state.Instances, 2, "unrelated inventory is untouched")

	client = stateClient()
	client.profilesError = errors.New("connection failed")
	state, err = NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err)
	assert.Equal(t, []string{"Profiles are unavailable."}, state.Warnings)
	assert.Empty(t, state.Profiles)
	assert.Len(t, state.Networks, 2, "networks are unaffected")
}

func TestNetworkDetailReportsStateAndLeases(t *testing.T) {
	detail, err := NewSystemManager(stateClient()).NetworkDetail(context.Background(), "production", "incusbr0")
	require.NoError(t, err)

	assert.Equal(t, "incusbr0", detail.Name)
	assert.True(t, detail.Managed)
	assert.Equal(t, "00:16:3e:11:22:33", detail.HWAddr)
	assert.Equal(t, 1500, detail.MTU)
	assert.Equal(t, "up", detail.State)
	assert.Equal(t, []string{"10.209.192.1/24"}, detail.Addresses)
	require.NotNil(t, detail.Counters)
	assert.Equal(t, int64(1048576), detail.Counters.BytesReceived)

	assert.True(t, detail.LeasesAvailable)
	assert.Equal(t, []NetworkLease{
		{Address: "10.209.192.235", Hostname: "web-test", HWAddr: "00:16:3e:17:6f:6c", Type: "dynamic"},
	}, detail.Leases)
	assert.Equal(t, []string{"/1.0/instances/api", "/1.0/profiles/default"}, detail.UsedBy)
}

// TestUnmanagedNetworkReportsNoLeases proves the distinction between "no
// leases" and "leases cannot be read": Incus tracks leases only for
// networks it manages, and an unmanaged interface must not be made to look
// like a managed one with an empty lease table.
func TestUnmanagedNetworkReportsNoLeases(t *testing.T) {
	client := stateClient()
	detail, err := NewSystemManager(client).NetworkDetail(context.Background(), "production", "docker0")
	require.NoError(t, err)

	assert.False(t, detail.Managed)
	assert.False(t, detail.LeasesAvailable)
	assert.Empty(t, detail.Leases)
	for _, action := range client.actions {
		assert.False(t, strings.HasPrefix(action, "network leases "),
			"an unmanaged network must not be asked for leases, got %q", action)
	}
}

// TestManagedNetworkWithUnreadableLeasesDegrades proves a managed network
// whose lease read fails reports unavailable rather than failing the page.
func TestManagedNetworkWithUnreadableLeasesDegrades(t *testing.T) {
	client := stateClient()
	client.leasesError = errors.New("connection failed")
	detail, err := NewSystemManager(client).NetworkDetail(context.Background(), "production", "incusbr0")
	require.NoError(t, err)
	assert.False(t, detail.LeasesAvailable)
	assert.Empty(t, detail.Leases)
	assert.Equal(t, "up", detail.State, "the rest of the detail survives")
}

// TestNetworkDetailExcludesSecretConfiguration is the security guarantee for
// networks. Incus network configuration carries `bgp.peers.<name>.password`
// -- a real BGP session password -- alongside the `user.*` and `raw.*`
// namespaces, and none of it may cross the broker boundary.
func TestNetworkDetailExcludesSecretConfiguration(t *testing.T) {
	detail, err := NewSystemManager(stateClient()).NetworkDetail(context.Background(), "production", "incusbr0")
	require.NoError(t, err)

	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	serialized := string(encoded)

	for _, forbidden := range []string{
		"bgp.peers.upstream.password", "s3cr3t-bgp-password",
		"bgp.peers.upstream.address", "192.0.2.1",
		"user.note", "operator scratch space",
		"raw.dnsmasq", "auth-zone=example.test",
	} {
		assert.NotContains(t, serialized, forbidden, "%q must not cross the broker boundary", forbidden)
	}

	// The allowlisted keys are present, so the exclusions above are
	// proving a filter rather than an empty model.
	assert.Equal(t, []ConfigEntry{
		{Key: "dns.domain", Value: "incus"},
		{Key: "ipv4.address", Value: "10.209.192.1/24"},
		{Key: "ipv4.nat", Value: "true"},
		{Key: "ipv6.address", Value: "none"},
	}, detail.Config)
}

// TestNetworkListDoesNotBypassTheAllowlist proves the list model's IPv4/IPv6
// columns read through the same filter as the detail model, so the cheaper
// summary cannot become a way around it.
func TestNetworkListDoesNotBypassTheAllowlist(t *testing.T) {
	client := stateClient()
	state, err := NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err)

	encoded, err := json.Marshal(state.Networks)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "s3cr3t-bgp-password")
	assert.NotContains(t, string(encoded), "operator scratch space")

	assert.False(t, allowedNetworkKey("bgp.peers.upstream.password"))
	assert.False(t, allowedNetworkKey("user.note"))
	assert.False(t, allowedNetworkKey("raw.dnsmasq"))
	assert.False(t, allowedNetworkKey("ovn.key"))
	assert.False(t, allowedNetworkKey("tunnel.mesh.key"))
	assert.True(t, allowedNetworkKey("ipv4.address"))
}

func TestNetworkDetailValidatesNameAndProject(t *testing.T) {
	manager := NewSystemManager(stateClient())

	_, err := manager.NetworkDetail(context.Background(), "production", "../instances/api")
	assert.EqualError(t, err, "invalid network name")

	_, err = manager.NetworkDetail(context.Background(), "missing", "incusbr0")
	assert.EqualError(t, err, "project is not available")
}
