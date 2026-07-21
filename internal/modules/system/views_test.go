package system

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemPageRendersDetailedResourceCards(t *testing.T) {
	snapshot := Snapshot{
		CPUs:              8,
		CPUPercent:        42.5,
		CPUUserPercent:    24.5,
		CPUSystemPercent:  12.5,
		CPUIOWaitPercent:  5.5,
		DiskFree:          25 * 1024 * 1024 * 1024,
		DiskPercent:       75,
		DiskTotal:         100 * 1024 * 1024 * 1024,
		DiskUsed:          75 * 1024 * 1024 * 1024,
		Hostname:          "host-one",
		InodesFree:        250000,
		InodesPercent:     75,
		InodesTotal:       1000000,
		MemoryAvailable:   4 * 1024 * 1024 * 1024,
		MemoryCached:      2 * 1024 * 1024 * 1024,
		MemoryPercent:     50,
		MemoryTotal:       8 * 1024 * 1024 * 1024,
		MemoryUsed:        4 * 1024 * 1024 * 1024,
		NetworkReceive:    3 * 1024 * 1024,
		NetworkSend:       2 * 1024 * 1024,
		SwapTotal:         2 * 1024 * 1024 * 1024,
		SwapUsed:          512 * 1024 * 1024,
		NetworkInterfaces: []NetworkInterface{{Name: "enp2s0", Receive: 2 * 1024 * 1024, Send: 1024 * 1024, SpeedMbps: 1000, State: "up"}},
	}
	var output strings.Builder
	require.NoError(t, SystemPage(snapshot).Render(context.Background(), &output))

	html := output.String()
	for _, value := range []string{"User", "24.5%", "I/O wait", "Available", "4.0 GiB", "Cached", "Swap", "512.0 MiB / 2.0 GiB", "Inodes used", "Inodes free", "250.0K", "enp2s0", "up / 1000 Mbps", "RX 2.0 MiB", "TX 1.0 MiB"} {
		assert.Contains(t, html, value)
	}
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}

func TestSystemPageRendersNetworkEmptyState(t *testing.T) {
	var output strings.Builder
	require.NoError(t, SystemPage(Snapshot{}).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "No non-loopback interfaces")
	assert.Contains(t, output.String(), "Not available")
}
