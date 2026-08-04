package incus

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummaryRendersIconComponent(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Summary(State{}).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "<svg")
	assert.NotContains(t, output.String(), "@web.Icon")
}

func TestPageRendersSelectedProject(t *testing.T) {
	state := State{Project: "production", Projects: []Project{{Name: "default"}, {Name: "production"}}, Instances: []Instance{{Name: "api"}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	assert.Contains(t, output.String(), `value="production" selected`)
	assert.Contains(t, output.String(), `name="project" value="production"`)
}

func TestPageRendersStorageCards(t *testing.T) {
	state := State{
		Project: "production",
		Pools:   []StoragePool{{Name: "fast", Driver: "zfs", Status: "Created", UsedBy: 2}},
		Volumes: []StorageVolume{{Name: "data", Pool: "fast", ContentType: "filesystem", UsedBy: 1}},
		Buckets: []StorageBucket{{Name: "assets", Pool: "fast", S3URL: "https://s3.example/assets"}},
	}
	var output strings.Builder
	require.NoError(t, Page(state, "token", false).Render(context.Background(), &output))
	for _, value := range []string{"Storage pools", "Storage volumes", "Storage buckets", "fast", "data", "assets", "https://s3.example/assets"} {
		assert.Contains(t, output.String(), value)
	}
}

func TestPageRendersStorageEmptyStates(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(State{Project: "default"}, "token", false).Render(context.Background(), &output))
	for _, value := range []string{"No storage pools were found.", "No custom storage volumes were found in the default project.", "No storage buckets were found."} {
		assert.Contains(t, output.String(), value)
	}
}

func TestImagesRenderActionsAndDisabledUsage(t *testing.T) {
	state := State{Project: "production", Images: []Image{{Fingerprint: "free", Name: "free"}, {Fingerprint: "used", Name: "used", Instances: 2}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "Actions")
	assert.Contains(t, html, `/incus/images/free/remove`)
	assert.Contains(t, html, `name="project" value="production"`)
	assert.Contains(t, html, `title="Delete image"`)
	assert.Contains(t, html, `title="In use by 2 instance(s)"`)
	assert.Contains(t, html, "disabled")
	assert.Contains(t, html, `<svg`)
}

// detailFixture is populated enough that every assertion about a
// conditionally-rendered element is proving the condition rather than
// passing vacuously on an empty collection.
func detailFixture() Detail {
	return Detail{
		Architecture: "x86_64",
		Config:       []ConfigEntry{{Key: "limits.memory", Value: "2GiB"}},
		Devices:      []Device{{Name: "eth0", Type: "nic", Properties: []ConfigEntry{{Key: "network", Value: "incusbr0"}}}},
		Instance: Instance{
			Addresses: []string{"10.0.0.5"}, Memory: 268435456, Name: "api", Processes: 42,
			Running: true, Snapshots: 1, StartedAt: "2026-08-01T12:00:00Z", Status: "Running", Type: "Container",
		},
		MemoryTotal: 2147483648,
		Networks:    []Interface{{Addresses: []string{"10.0.0.5/24"}, HWAddr: "00:16:3e:aa:bb:cc", MTU: 1500, Name: "eth0", State: "up"}},
		Profiles:    []string{"default"},
		Project:     "production",
		Snapshots:   []Snapshot{{CreatedAt: "2026-07-30T03:00:00Z", Name: "nightly"}},
	}
}

func TestPageRendersLiveInstanceColumns(t *testing.T) {
	state := State{
		Project: "production",
		Instances: []Instance{
			{Addresses: []string{"10.0.0.5", "fd42::5"}, Memory: 268435456, Name: "api", Running: true, Status: "Running", Type: "Container"},
			{Name: "worker-vm", Status: "Stopped", Type: "Virtual machine"},
		},
	}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()

	assert.Contains(t, html, "10.0.0.5, fd42::5")
	assert.Contains(t, html, "256.0 MiB")
	assert.Contains(t, html, `href="/incus/instances/api?project=production"`)
	// The stopped instance reports no live values, rendered as dashes in
	// their own cells rather than as measured zeroes.
	assert.Contains(t, html, "<td>—</td>")
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersStorageWarnings(t *testing.T) {
	state := State{Project: "production", Warnings: []string{"Storage volumes for pool fast are unavailable."}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", false).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Partial inventory")
	assert.Contains(t, output.String(), "Storage volumes for pool fast are unavailable.")

	var clean strings.Builder
	require.NoError(t, Page(State{Project: "production"}, "token", false).Render(context.Background(), &clean))
	assert.NotContains(t, clean.String(), "Partial inventory")
}

func TestDetailPageRendersInstanceDepth(t *testing.T) {
	var output strings.Builder
	require.NoError(t, DetailPage(detailFixture(), "token", true).Render(context.Background(), &output))
	html := output.String()

	for _, value := range []string{
		"api", "Network", "Snapshots", "Devices", "Configuration", "Profiles",
		"eth0", "00:16:3e:aa:bb:cc", "10.0.0.5/24", "nightly", "network=incusbr0",
		"limits.memory", "2GiB", "default", "x86_64", "256.0 MiB", "of 2.0 GiB",
		"started 2026-08-01T12:00:00Z",
	} {
		assert.Contains(t, html, value)
	}
	assert.Contains(t, html, `href="/incus/instances/api/logs?project=production&amp;source=console"`)
	assert.Contains(t, html, `href="/incus?project=production"`)
	assert.NotContains(t, html, "@web.")
}

// TestDetailPageHidesLifecycleForNonAdmin proves the lifecycle controls are
// gated on the admin flag with a fixture that would otherwise render them,
// so the assertion is not vacuous.
func TestDetailPageHidesLifecycleForNonAdmin(t *testing.T) {
	var admin strings.Builder
	require.NoError(t, DetailPage(detailFixture(), "token", true).Render(context.Background(), &admin))
	assert.Contains(t, admin.String(), "/incus/instances/api/restart")

	var reader strings.Builder
	require.NoError(t, DetailPage(detailFixture(), "token", false).Render(context.Background(), &reader))
	assert.NotContains(t, reader.String(), "/incus/instances/api/restart")
	assert.NotContains(t, reader.String(), "/incus/instances/api/stop")
	assert.NotContains(t, reader.String(), "Lifecycle")
}

func TestDetailPageRendersEmptyStates(t *testing.T) {
	var output strings.Builder
	empty := Detail{Instance: Instance{Name: "api"}, Project: "production"}
	require.NoError(t, DetailPage(empty, "token", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "No snapshots exist for this instance.")
	assert.Contains(t, html, "No interface state is reported. A stopped instance has none.")
	assert.Contains(t, html, "No profiles are applied to this instance.")
	// The snapshots empty state is DetailPage's one @web.Icon invocation,
	// so this is where the component's rendered output is asserted.
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}

func TestLogsViewRendersLinesAndSourceToggle(t *testing.T) {
	logs := Logs{
		Lines: []LogLine{{Message: "console boot line"}},
		Name:  "api", Project: "production", Source: SourceConsole,
	}
	var output strings.Builder
	require.NoError(t, LogsView(logs, false).Render(context.Background(), &output))
	html := output.String()

	assert.Contains(t, html, "console boot line")
	assert.Contains(t, html, "Console log")
	// The toggle offers exactly the other source.
	assert.Contains(t, html, `href="/incus/instances/api/logs?project=production&amp;source=log"`)
	assert.Contains(t, html, "Supervisor log")
	assert.Contains(t, html, `href="/incus/instances/api?project=production"`)
	assert.Contains(t, html, `hx-trigger="every 5s"`)
	assert.NotContains(t, html, "@web.")
}

func TestLogsViewRendersUnavailableAndEmptyStates(t *testing.T) {
	logs := Logs{Name: "api", Project: "production", Source: SourceLog}

	var unavailable strings.Builder
	require.NoError(t, LogsView(logs, true).Render(context.Background(), &unavailable))
	assert.Contains(t, unavailable.String(), "The supervisor log could not be read.")

	// The console wording does not suggest retrying, because a console
	// buffer that was never enabled fails permanently.
	var console strings.Builder
	require.NoError(t, LogsView(Logs{Name: "api", Source: SourceConsole}, true).Render(context.Background(), &console))
	assert.Contains(t, console.String(), "may not have console logging enabled")
	assert.NotContains(t, console.String(), "Try again later")

	var empty strings.Builder
	require.NoError(t, LogsView(logs, false).Render(context.Background(), &empty))
	assert.Contains(t, empty.String(), "No log output.")
	assert.NotContains(t, empty.String(), "These logs are unavailable")
}

// TestLogsViewEscapesLogContent proves log text is rendered as text: a line
// containing markup must not become markup, since log content is entirely
// attacker-influenced.
func TestLogsViewEscapesLogContent(t *testing.T) {
	logs := Logs{
		Lines: []LogLine{{Message: `<script>alert("x")</script>`}},
		Name:  "api", Project: "production", Source: SourceConsole,
	}
	var output strings.Builder
	require.NoError(t, LogsView(logs, false).Render(context.Background(), &output))
	assert.NotContains(t, output.String(), "<script>alert")
	assert.Contains(t, output.String(), "&lt;script&gt;")
}

// TestDetailPageRendersSnapshotControls proves the snapshot create form and
// the per-row restore/delete controls render for an admin against a fixture
// that actually carries a snapshot, so the presence assertions are real.
func TestDetailPageRendersSnapshotControls(t *testing.T) {
	stopped := detailFixture()
	stopped.Instance.Running = false
	stopped.Instance.Status = "Stopped"

	var output strings.Builder
	require.NoError(t, DetailPage(stopped, "token", true).Render(context.Background(), &output))
	html := output.String()

	assert.Contains(t, html, `action="/incus/instances/api/snapshots"`)
	assert.Contains(t, html, "Create snapshot")
	assert.Contains(t, html, `action="/incus/instances/api/snapshots/nightly/restore"`)
	assert.Contains(t, html, `action="/incus/instances/api/snapshots/nightly/delete"`)
	assert.NotContains(t, html, "@web.")
}

// TestDetailPageWithholdsRestoreWhileRunning proves the restore control is
// replaced by an explanation while the instance runs — the same precondition
// the broker enforces — while delete stays available.
func TestDetailPageWithholdsRestoreWhileRunning(t *testing.T) {
	var output strings.Builder
	require.NoError(t, DetailPage(detailFixture(), "token", true).Render(context.Background(), &output))
	html := output.String()

	assert.NotContains(t, html, `action="/incus/instances/api/snapshots/nightly/restore"`)
	assert.Contains(t, html, "Stop to restore")
	assert.Contains(t, html, `action="/incus/instances/api/snapshots/nightly/delete"`)
}

// TestDetailPageHidesSnapshotControlsForNonAdmin proves every snapshot
// control — the create form and both per-row actions — is withheld from a
// read-only user, against a fixture that renders them all for an admin.
func TestDetailPageHidesSnapshotControlsForNonAdmin(t *testing.T) {
	stopped := detailFixture()
	stopped.Instance.Running = false

	var output strings.Builder
	require.NoError(t, DetailPage(stopped, "token", false).Render(context.Background(), &output))
	html := output.String()

	assert.NotContains(t, html, "/incus/instances/api/snapshots")
	assert.NotContains(t, html, "Create snapshot")
	assert.Contains(t, html, "No permission")
}

// TestDetailPageRendersForceStopOnlyWhileRunning proves force stop appears
// beside the graceful stop for a running instance and not at all otherwise.
func TestDetailPageRendersForceStopOnlyWhileRunning(t *testing.T) {
	var running strings.Builder
	require.NoError(t, DetailPage(detailFixture(), "token", true).Render(context.Background(), &running))
	assert.Contains(t, running.String(), `action="/incus/instances/api/stop-force"`)
	assert.Contains(t, running.String(), "Force stop")

	stopped := detailFixture()
	stopped.Instance.Running = false
	var output strings.Builder
	require.NoError(t, DetailPage(stopped, "token", true).Render(context.Background(), &output))
	assert.NotContains(t, output.String(), "/incus/instances/api/stop-force")
}

func TestPageRendersNetworkAndProfileCards(t *testing.T) {
	state := State{
		Project: "production",
		Networks: []Network{
			{IPv4: "10.209.192.1/24", Managed: true, Name: "incusbr0", Status: "Created", Type: "bridge", UsedBy: 2},
			{Managed: false, Name: "docker0", Type: "bridge"},
		},
		Profiles: []Profile{{Description: "Default Incus profile", Devices: 2, Name: "default", UsedBy: 1}},
	}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()

	assert.Contains(t, html, "Networks")
	assert.Contains(t, html, `href="/incus/networks/incusbr0?project=production"`)
	assert.Contains(t, html, `href="/incus/networks/docker0?project=production"`)
	assert.Contains(t, html, "Managed")
	assert.Contains(t, html, "Observed")
	assert.Contains(t, html, "10.209.192.1/24")

	assert.Contains(t, html, "Profiles")
	assert.Contains(t, html, `href="/incus/profiles/default?project=production"`)
	assert.Contains(t, html, "Default Incus profile")
	assert.NotContains(t, html, "@web.")
}

func TestNetworkPageRendersLeasesAndConfig(t *testing.T) {
	detail := NetworkDetail{
		Addresses: []string{"10.209.192.1/24"},
		Config:    []ConfigEntry{{Key: "ipv4.nat", Value: "true"}},
		Counters:  &TrafficCount{BytesReceived: 1048576, BytesSent: 2097152, PacketsReceived: 700, PacketsSent: 1},
		HWAddr:    "00:16:3e:11:22:33", Leases: []NetworkLease{{Address: "10.209.192.235", Hostname: "web-test", HWAddr: "00:16:3e:17:6f:6c", Type: "dynamic"}},
		LeasesAvailable: true, Managed: true, MTU: 1500, Name: "incusbr0",
		Project: "production", State: "up", Status: "Created", Type: "bridge",
		UsedBy: []string{"/1.0/instances/web-test"},
	}
	var output strings.Builder
	require.NoError(t, NetworkPage(detail).Render(context.Background(), &output))
	html := output.String()

	for _, value := range []string{
		"incusbr0", "DHCP leases", "10.209.192.235", "web-test", "dynamic",
		"00:16:3e:11:22:33", "10.209.192.1/24", "ipv4.nat", "1.0 MiB", "2.0 MiB",
		"managed by Incus", "700 packets", "1 packet",
	} {
		assert.Contains(t, html, value)
	}
	assert.NotContains(t, html, "@web.")
}

// TestNetworkPageDistinguishesUnmanagedFromEmpty proves an unmanaged
// network says Incus tracks no leases for it, rather than showing the same
// "no leases" text a managed network with none would show.
func TestNetworkPageDistinguishesUnmanagedFromEmpty(t *testing.T) {
	unmanaged := NetworkDetail{Leases: []NetworkLease{}, Name: "docker0", Project: "production", Type: "bridge"}
	var output strings.Builder
	require.NoError(t, NetworkPage(unmanaged).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Incus does not manage this network, so it tracks no leases for it.")
	assert.Contains(t, output.String(), "observed, not managed")
	assert.Contains(t, output.String(), "<svg")

	managed := NetworkDetail{Leases: []NetworkLease{}, LeasesAvailable: true, Managed: true, Name: "incusbr0", Project: "production", Type: "bridge"}
	var empty strings.Builder
	require.NoError(t, NetworkPage(managed).Render(context.Background(), &empty))
	assert.Contains(t, empty.String(), "This network has issued no leases.")
	assert.NotContains(t, empty.String(), "does not manage this network")
}

func TestProfilePageRendersDevicesAndUsedBy(t *testing.T) {
	detail := ProfileDetail{
		Config:      []ConfigEntry{{Key: "limits.memory", Value: "2GiB"}},
		Description: "Default Incus profile",
		Devices:     []Device{{Name: "eth0", Type: "nic", Properties: []ConfigEntry{{Key: "network", Value: "incusbr0"}}}},
		Name:        "default", Project: "production",
		UsedBy: []string{"/1.0/instances/web-test", "/1.0/instances/fedora"},
	}
	var output strings.Builder
	require.NoError(t, ProfilePage(detail).Render(context.Background(), &output))
	html := output.String()

	for _, value := range []string{
		"default", "Default Incus profile", "eth0", "network=incusbr0",
		"limits.memory", "2GiB", "Used by", "web-test", "fedora", "2 references",
	} {
		assert.Contains(t, html, value)
	}
	assert.NotContains(t, html, "@web.")
}

// TestDetailPagesExposeNoMutationControl pins the read-only contract for
// both new pages: neither renders a form, so neither can target a broker
// action, and there is none in the vocabulary for them anyway.
func TestDetailPagesExposeNoMutationControl(t *testing.T) {
	var network strings.Builder
	require.NoError(t, NetworkPage(NetworkDetail{
		Config: []ConfigEntry{{Key: "ipv4.nat", Value: "true"}}, LeasesAvailable: true,
		Leases: []NetworkLease{{Address: "10.0.0.1"}}, Managed: true, Name: "incusbr0",
		Project: "production", UsedBy: []string{"/1.0/instances/api"},
	}).Render(context.Background(), &network))
	assert.NotContains(t, network.String(), "<form")
	assert.NotContains(t, network.String(), "<button")

	var profile strings.Builder
	require.NoError(t, ProfilePage(ProfileDetail{
		Config:  []ConfigEntry{{Key: "limits.memory", Value: "2GiB"}},
		Devices: []Device{{Name: "eth0", Type: "nic"}}, Name: "default",
		Project: "production", UsedBy: []string{"/1.0/instances/api"},
	}).Render(context.Background(), &profile))
	assert.NotContains(t, profile.String(), "<form")
	assert.NotContains(t, profile.String(), "<button")
}

// TestPageRendersCreateFormForAdmin proves the creation form renders for an
// admin, offers exactly the two supported types, and lists the project's
// profiles alongside a default option.
func TestPageRendersCreateFormForAdmin(t *testing.T) {
	state := State{
		Project:  "production",
		Profiles: []Profile{{Name: "default"}, {Name: "web"}},
	}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()

	assert.Contains(t, html, `action="/incus/instances"`)
	assert.Contains(t, html, "Create instance")
	assert.Contains(t, html, `value="container"`)
	assert.Contains(t, html, `value="virtual-machine"`)
	assert.Contains(t, html, "Default for the project")
	assert.Contains(t, html, `value="web"`)
	// The form offers no way to name an image server.
	assert.NotContains(t, html, "linuxcontainers.org")
	assert.NotContains(t, html, `name="remote"`)
	assert.NotContains(t, html, `name="server"`)
}

func TestPageHidesCreateFormForNonAdmin(t *testing.T) {
	state := State{Project: "production", Profiles: []Profile{{Name: "default"}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", false).Render(context.Background(), &output))
	assert.NotContains(t, output.String(), `action="/incus/instances"`)
	assert.NotContains(t, output.String(), "Create instance")
}
