package incus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const imageFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeClient struct {
	actions       []string
	bucketErrors  map[string]error
	buckets       map[string][]api.StorageBucket
	consoleError  error
	consoleLog    string
	images        []api.Image
	instanceErr   error
	instances     []api.InstanceFull
	logfile       string
	logfileError  error
	leases        []api.NetworkLease
	leasesError   error
	networkState  *api.NetworkState
	networks      []api.Network
	networksError error
	pools         []api.StoragePool
	profiles      []api.Profile
	profilesError error
	projects      []api.Project
	snapshotError error
	version       string
	volumeErrors  map[string]error
	volumes       map[string][]api.StorageVolume
}

func (client *fakeClient) ConsoleLog(_ context.Context, project, name string) (io.ReadCloser, error) {
	client.actions = append(client.actions, "console "+project+" "+name)
	if client.consoleError != nil {
		return nil, client.consoleError
	}
	return io.NopCloser(strings.NewReader(client.consoleLog)), nil
}

func (client *fakeClient) CreateSnapshot(_ context.Context, project, instance, name string) error {
	client.actions = append(client.actions, "snapshot create "+project+" "+instance+" "+name)
	return client.snapshotError
}

func (client *fakeClient) DeleteSnapshot(_ context.Context, project, instance, name string) error {
	client.actions = append(client.actions, "snapshot delete "+project+" "+instance+" "+name)
	return client.snapshotError
}

func (client *fakeClient) RestoreSnapshot(_ context.Context, project, instance, name string) error {
	client.actions = append(client.actions, "snapshot restore "+project+" "+instance+" "+name)
	return client.snapshotError
}

func (client *fakeClient) Instance(_ context.Context, project, name string) (*api.InstanceFull, error) {
	client.actions = append(client.actions, "instance "+project+" "+name)
	if client.instanceErr != nil {
		return nil, client.instanceErr
	}
	for _, item := range client.instances {
		if item.Name == name {
			return &item, nil
		}
	}
	return nil, errors.New("not found")
}

func (client *fakeClient) Logfile(_ context.Context, project, name, filename string) (io.ReadCloser, error) {
	client.actions = append(client.actions, "logfile "+project+" "+name+" "+filename)
	if client.logfileError != nil {
		return nil, client.logfileError
	}
	return io.NopCloser(strings.NewReader(client.logfile)), nil
}

func (client *fakeClient) Images(_ context.Context, project string) ([]api.Image, error) {
	client.actions = append(client.actions, "images "+project)
	return client.images, nil
}

func (client *fakeClient) Instances(_ context.Context, project string) ([]api.InstanceFull, error) {
	client.actions = append(client.actions, "instances "+project)
	return client.instances, nil
}

func (client *fakeClient) Networks(_ context.Context, project string) ([]api.Network, error) {
	client.actions = append(client.actions, "networks "+project)
	return client.networks, client.networksError
}

func (client *fakeClient) Network(_ context.Context, project, name string) (*api.Network, error) {
	client.actions = append(client.actions, "network "+project+" "+name)
	for _, item := range client.networks {
		if item.Name == name {
			return &item, nil
		}
	}
	return nil, errors.New("not found")
}

func (client *fakeClient) NetworkState(_ context.Context, project, name string) (*api.NetworkState, error) {
	client.actions = append(client.actions, "network state "+project+" "+name)
	if client.networkState == nil {
		return nil, errors.New("no state")
	}
	return client.networkState, nil
}

func (client *fakeClient) NetworkLeases(_ context.Context, project, name string) ([]api.NetworkLease, error) {
	client.actions = append(client.actions, "network leases "+project+" "+name)
	if client.leasesError != nil {
		return nil, client.leasesError
	}
	return client.leases, nil
}

func (client *fakeClient) Profiles(_ context.Context, project string) ([]api.Profile, error) {
	client.actions = append(client.actions, "profiles "+project)
	return client.profiles, client.profilesError
}

func (client *fakeClient) Profile(_ context.Context, project, name string) (*api.Profile, error) {
	client.actions = append(client.actions, "profile "+project+" "+name)
	for _, item := range client.profiles {
		if item.Name == name {
			return &item, nil
		}
	}
	return nil, errors.New("not found")
}

func (client *fakeClient) Projects(context.Context) ([]api.Project, error) {
	return client.projects, nil
}

func (client *fakeClient) Remove(_ context.Context, project, name string) error {
	client.actions = append(client.actions, "remove "+project+" "+name)
	return nil
}

func (client *fakeClient) RemoveImage(_ context.Context, project, fingerprint string) error {
	client.actions = append(client.actions, "remove image "+project+" "+fingerprint)
	return nil
}

func (client *fakeClient) Restart(_ context.Context, project, name string, timeout int) error {
	client.actions = append(client.actions, fmt.Sprintf("restart %d %s %s", timeout, project, name))
	return nil
}

func (client *fakeClient) Server(context.Context) (*api.Server, error) {
	return &api.Server{Environment: api.ServerEnvironment{ServerVersion: client.version}}, nil
}

func (client *fakeClient) Start(_ context.Context, project, name string) error {
	client.actions = append(client.actions, "start "+project+" "+name)
	return nil
}

func (client *fakeClient) StorageBuckets(_ context.Context, project, pool string) ([]api.StorageBucket, error) {
	client.actions = append(client.actions, "buckets "+project+" "+pool)
	return client.buckets[pool], client.bucketErrors[pool]
}

func (client *fakeClient) StoragePools(context.Context) ([]api.StoragePool, error) {
	return client.pools, nil
}

func (client *fakeClient) StorageVolumes(_ context.Context, project, pool string) ([]api.StorageVolume, error) {
	client.actions = append(client.actions, "volumes "+project+" "+pool)
	return client.volumes[pool], client.volumeErrors[pool]
}

func (client *fakeClient) Stop(_ context.Context, project, name string, timeout int, force bool) error {
	client.actions = append(client.actions, fmt.Sprintf("stop %d force=%t %s %s", timeout, force, project, name))
	return nil
}

func TestSystemManagerBuildsCanonicalState(t *testing.T) {
	client := stateClient()
	state, err := NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err)
	assert.Equal(t, "6.11", state.Version)
	assert.Equal(t, "production", state.Project)
	assert.Equal(t, []Project{{Name: "default"}, {Name: "production"}}, state.Projects)
	require.Len(t, state.Instances, 2)
	assert.Equal(t, "api", state.Instances[0].Name)
	assert.True(t, state.Instances[0].Running)
	assert.Equal(t, "Ubuntu 24.04", state.Instances[0].Image)
	assert.Equal(t, "Virtual machine", state.Instances[1].Type)

	// The running instance carries live state: only globally-scoped
	// addresses, sorted; memory, CPU and process counters; the recorded
	// start time; and its snapshot count.
	running := state.Instances[0]
	assert.Equal(t, []string{"10.0.0.5", "fd42::5"}, running.Addresses,
		"loopback and link-local addresses must not be reported")
	assert.Equal(t, uint64(268435456), running.Memory)
	assert.Equal(t, int64(12_000_000_000), running.CPUTime)
	assert.Equal(t, int64(42), running.Processes)
	assert.Equal(t, "2026-08-01T12:00:00Z", running.StartedAt)
	assert.Equal(t, 2, running.Snapshots)

	// The stopped instance has no runtime state at all, so every live
	// field stays zero rather than being reported as a measured zero.
	stopped := state.Instances[1]
	assert.Empty(t, stopped.Addresses)
	assert.Zero(t, stopped.Memory)
	assert.Zero(t, stopped.Processes)
	assert.Empty(t, stopped.StartedAt)
	require.Len(t, state.Images, 1)
	assert.Equal(t, "ubuntu/24.04", state.Images[0].Name)
	assert.Equal(t, 2, state.Images[0].Instances)
	assert.Equal(t, uint64(1048576), state.Images[0].Size)
	assert.Equal(t, []StoragePool{
		{Driver: "zfs", Name: "fast", Status: "created", UsedBy: 1},
		{Driver: "dir", Name: "slow", Status: "Created", UsedBy: 2},
	}, state.Pools)
	assert.Equal(t, []StorageVolume{
		{ContentType: "filesystem", Name: "data", Pool: "fast", Type: "custom", UsedBy: 1},
		{ContentType: "block", Name: "backup", Pool: "slow", Type: "custom", UsedBy: 0},
	}, state.Volumes)
	assert.Equal(t, []StorageBucket{{Name: "assets", Pool: "fast", S3URL: "https://s3.example/assets"}}, state.Buckets)
}

// TestStorageDegradesPerPool proves a pool whose volume or bucket read
// fails costs only that pool's rows plus a warning — every other pool's
// inventory, and the rest of the page's data, survives. A driver that
// simply has no bucket support stays silent, since that is a capability
// gap rather than a degraded read.
func TestStorageDegradesPerPool(t *testing.T) {
	client := stateClient()
	client.bucketErrors["slow"] = errors.New("driver does not support storage buckets")
	state, err := NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err)
	assert.Len(t, state.Buckets, 1)
	assert.Empty(t, state.Warnings, "an unsupported bucket driver is not a degraded read")

	client = stateClient()
	client.bucketErrors["slow"] = errors.New("connection failed")
	state, err = NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err, "one pool's failed bucket read must not fail the whole query")
	assert.Equal(t, []string{"Storage buckets for pool slow are unavailable."}, state.Warnings)
	assert.Len(t, state.Buckets, 1, "the healthy pool's buckets survive")
	assert.Len(t, state.Volumes, 2, "volumes are unaffected by a bucket failure")

	client = stateClient()
	client.volumeErrors["fast"] = errors.New("connection failed")
	state, err = NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err)
	assert.Equal(t, []string{"Storage volumes for pool fast are unavailable."}, state.Warnings)
	assert.Equal(t, []StorageVolume{{ContentType: "block", Name: "backup", Pool: "slow", Type: "custom"}}, state.Volumes,
		"the other pool's volumes are still listed")
	assert.Len(t, state.Pools, 2, "both pools are still reported")
	assert.Len(t, state.Instances, 2, "unrelated inventory is untouched")
}

func TestInstanceActionsValidateStateAndName(t *testing.T) {
	client := stateClient()
	manager := NewSystemManager(client)

	require.NoError(t, manager.Stop(context.Background(), "production", "api"))
	assert.Equal(t, "stop 30 force=false production api", client.actions[len(client.actions)-1])
	require.NoError(t, manager.Start(context.Background(), "production", "worker-vm"))
	assert.Equal(t, "start production worker-vm", client.actions[len(client.actions)-1])
	require.NoError(t, manager.Restart(context.Background(), "production", "api"))
	assert.Equal(t, "restart 30 production api", client.actions[len(client.actions)-1])
	require.NoError(t, manager.Remove(context.Background(), "production", "worker-vm"))
	assert.Equal(t, "remove production worker-vm", client.actions[len(client.actions)-1])

	err := manager.Remove(context.Background(), "production", "api")
	assert.EqualError(t, err, "stop the instance before removing it")
	err = manager.Start(context.Background(), "production", "../default/api")
	assert.EqualError(t, err, "invalid instance name")
	err = manager.Start(context.Background(), "missing", "worker-vm")
	assert.EqualError(t, err, "project is not available")
}

func TestRemoveImageValidatesUsageAndIdentifiers(t *testing.T) {
	used := stateClient()
	err := NewSystemManager(used).RemoveImage(context.Background(), "production", imageFingerprint)
	assert.EqualError(t, err, "remove instances using this image before deleting it")
	assert.NotContains(t, used.actions, "remove image production "+imageFingerprint)

	unused := stateClient()
	unused.instances = nil
	require.NoError(t, NewSystemManager(unused).RemoveImage(context.Background(), "production", imageFingerprint))
	assert.Contains(t, unused.actions, "remove image production "+imageFingerprint)

	err = NewSystemManager(unused).RemoveImage(context.Background(), "production", "")
	assert.EqualError(t, err, "project and image fingerprint are required")
}

func TestEmptyServerUsesInstalledVersionFallback(t *testing.T) {
	state, err := NewSystemManager(&fakeClient{projects: []api.Project{{Name: "default"}}}).State(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "installed", state.Version)
	assert.Equal(t, "default", state.Project)
	assert.Empty(t, state.Instances)
	assert.Empty(t, state.Images)
}

func TestValidInstanceName(t *testing.T) {
	for _, name := range []string{"api", "worker-01", "a"} {
		assert.True(t, validInstanceName(name), name)
	}
	for _, name := range []string{"", "-api", "api-", "API", "api/default", "api.local", "../api"} {
		assert.False(t, validInstanceName(name), name)
	}
}

// secretConfig carries, alongside the allowlisted keys, one key from each
// namespace the detail model must never expose: cloud-init payload,
// process environment, raw passthrough, and a non-base_image volatile key.
var secretConfig = api.ConfigMap{
	"volatile.base_image":   imageFingerprint,
	"image.description":     "Ubuntu 24.04",
	"image.os":              "Ubuntu",
	"limits.memory":         "2GiB",
	"security.nesting":      "true",
	"user.user-data":        "#cloud-config\nssh_authorized_keys:\n  - ssh-ed25519 AAAAsecret",
	"environment.SECRET":    "hunter2",
	"raw.lxc":               "lxc.apparmor.profile=unconfined",
	"volatile.eth0.hwaddr":  "00:16:3e:aa:bb:cc",
	"cloud-init.user-data":  "#cloud-config\npassword: hunter2",
	"user.vendor-data":      "vendor secret",
	"volatile.last_state.p": "RUNNING",
}

// managedNetworkConfig mixes the addressing keys a network page should
// report with a BGP peer password, which is a real Incus network config key
// and must never cross the broker boundary.
var managedNetworkConfig = api.ConfigMap{
	"ipv4.address":                "10.209.192.1/24",
	"ipv4.nat":                    "true",
	"ipv6.address":                "none",
	"dns.domain":                  "incus",
	"bgp.peers.upstream.password": "s3cr3t-bgp-password",
	"bgp.peers.upstream.address":  "192.0.2.1",
	"user.note":                   "operator scratch space",
	"raw.dnsmasq":                 "auth-zone=example.test",
}

// runningState is the live state of the running fixture instance. Its
// address list deliberately mixes one global address per family with a
// loopback and a link-local address, so a test can prove only globals are
// surfaced without matching on interface names.
func runningState() *api.InstanceState {
	return &api.InstanceState{
		CPU:       api.InstanceStateCPU{Usage: 12_000_000_000},
		Memory:    api.InstanceStateMemory{Usage: 268435456, Total: 2147483648},
		Processes: 42,
		StartedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Status:    "Running",
		Network: map[string]api.InstanceStateNetwork{
			"eth0": {
				Hwaddr: "00:16:3e:aa:bb:cc", Mtu: 1500, State: "up",
				Addresses: []api.InstanceStateNetworkAddress{
					{Family: "inet", Address: "10.0.0.5", Netmask: "24", Scope: "global"},
					{Family: "inet6", Address: "fd42::5", Netmask: "64", Scope: "global"},
					{Family: "inet6", Address: "fe80::1", Netmask: "64", Scope: "link"},
				},
			},
			"lo": {
				Mtu: 65536, State: "up",
				Addresses: []api.InstanceStateNetworkAddress{
					{Family: "inet", Address: "127.0.0.1", Netmask: "8", Scope: "local"},
				},
			},
		},
	}
}

func stateClient() *fakeClient {
	return &fakeClient{
		version:    "6.11",
		projects:   []api.Project{{Name: "production"}, {Name: "default"}},
		consoleLog: "console boot line\nconsole ready\n",
		logfile:    "lxc supervisor line\n",
		instances: []api.InstanceFull{
			{Instance: api.Instance{
				Name: "worker-vm", Status: "Stopped", StatusCode: api.Stopped, Type: "virtual-machine",
				ExpandedConfig: secretConfig,
			}},
			{
				Instance: api.Instance{
					Name: "api", Status: "Running", StatusCode: api.Running, Type: "container",
					ExpandedConfig: secretConfig,
					InstancePut: api.InstancePut{
						Architecture: "x86_64", Profiles: []string{"default"},
						Devices: api.DevicesMap{
							"eth0": {"type": "nic", "network": "incusbr0", "nictype": "bridged", "hwaddr": "00:16:3e:aa:bb:cc"},
							"root": {"type": "disk", "path": "/", "pool": "fast", "source": "/var/lib/incus/storage-pools/fast"},
							"gpu0": {"type": "gpu", "gputype": "physical", "pci": "0000:01:00.0", "uid": "1000"},
						},
					},
					CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
				},
				State: runningState(),
				Snapshots: []api.InstanceSnapshot{
					{Name: "api/nightly", InstanceSnapshotPut: api.InstanceSnapshotPut{}, CreatedAt: time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC), Stateful: false},
					{Name: "api/before-upgrade", CreatedAt: time.Date(2026, 7, 20, 3, 0, 0, 0, time.UTC), Stateful: true},
				},
			},
		},
		images: []api.Image{{Fingerprint: imageFingerprint, Size: 1048576, Type: "container", Aliases: []api.ImageAlias{{Name: "ubuntu/24.04"}}}},
		pools: []api.StoragePool{
			{Name: "slow", Driver: "dir", Status: "Created", UsedBy: []string{"a", "b"}},
			{Name: "fast", Driver: "zfs", UsedBy: []string{"a"}},
		},
		volumes: map[string][]api.StorageVolume{
			"fast": {{Name: "data", Type: "custom", ContentType: "filesystem", UsedBy: []string{"a"}}, {Name: "instance-root", Type: "container"}},
			"slow": {{Name: "backup", Type: "custom", ContentType: "block"}},
		},
		buckets: map[string][]api.StorageBucket{
			"fast": {{Name: "assets", S3URL: "https://s3.example/assets"}},
		},
		networks: []api.Network{
			{
				Name: "incusbr0", Type: "bridge", Managed: true, Status: "Created",
				UsedBy:     []string{"/1.0/instances/api", "/1.0/profiles/default"},
				NetworkPut: api.NetworkPut{Config: managedNetworkConfig},
			},
			{Name: "docker0", Type: "bridge", Managed: false},
		},
		networkState: &api.NetworkState{
			Hwaddr: "00:16:3e:11:22:33", Mtu: 1500, State: "up",
			Addresses: []api.NetworkStateAddress{
				{Family: "inet", Address: "10.209.192.1", Netmask: "24", Scope: "global"},
			},
			Counters: &api.NetworkStateCounters{
				BytesReceived: 1048576, BytesSent: 2097152, PacketsReceived: 700, PacketsSent: 1,
			},
		},
		leases: []api.NetworkLease{
			{Address: "10.209.192.235", Hostname: "web-test", Hwaddr: "00:16:3e:17:6f:6c", Type: "dynamic"},
		},
		profiles: []api.Profile{{
			Name: "default", UsedBy: []string{"/1.0/instances/api"},
			ProfilePut: api.ProfilePut{
				Description: "Default Incus profile",
				Config:      secretConfig,
				Devices: api.DevicesMap{
					"eth0": {"type": "nic", "network": "incusbr0", "name": "eth0"},
					"root": {"type": "disk", "path": "/", "pool": "default"},
				},
			},
		}},
		bucketErrors: map[string]error{},
		volumeErrors: map[string]error{},
	}
}

// TestUnknownCountersAreNotReportedAsMeasurements covers what a real
// virtual machine without the guest agent returns: Incus uses -1 as its
// "cannot determine" sentinel for the process count, and reports a zero CPU
// usage. Neither is a measurement, so neither may reach the page as one.
func TestUnknownCountersAreNotReportedAsMeasurements(t *testing.T) {
	client := stateClient()
	state := runningState()
	state.Processes = -1
	state.CPU.Usage = 0
	client.instances[1].State = state

	result, err := NewSystemManager(client).State(context.Background(), "production")
	require.NoError(t, err)

	instance := result.Instances[0]
	require.Equal(t, "api", instance.Name)
	assert.Zero(t, instance.Processes, "an unknown process count must not surface as -1")
	assert.Zero(t, instance.CPUTime)
	assert.Equal(t, "—", processLabel(instance))
	assert.Equal(t, "no CPU time reported", cpuLabel(instance))

	// Memory is still a real reading and must survive.
	assert.Equal(t, uint64(268435456), instance.Memory)
}

// TestInterfaceWithNoAddressesIsStillReported covers the same host: a VM
// whose interface exists but reports no addresses yet must still appear in
// the detail page's interface table rather than vanishing.
func TestInterfaceWithNoAddressesIsStillReported(t *testing.T) {
	client := stateClient()
	state := runningState()
	state.Network = map[string]api.InstanceStateNetwork{
		"eth0": {Hwaddr: "00:16:3e:aa:bb:cc", Mtu: 1500, State: "up"},
	}
	client.instances[1].State = state

	value, err := NewSystemManager(client).Detail(context.Background(), "production", "api")
	require.NoError(t, err)
	require.Len(t, value.Networks, 1)
	assert.Equal(t, "eth0", value.Networks[0].Name)
	assert.Empty(t, value.Networks[0].Addresses)
	assert.Equal(t, "—", joinValues(value.Networks[0].Addresses))
	assert.Empty(t, value.Instance.Addresses)
}
