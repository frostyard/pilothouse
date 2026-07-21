package system

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCPUPercent(t *testing.T) {
	a := assert.New(t)
	a.InDelta(50, cpuPercent(cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 150, total: 300}), 0.001)
	a.Equal(float64(0), cpuPercent(cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 100, total: 200}))
	a.Equal(float64(0), cpuPercent(cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 50, total: 100}))
}

func TestCPUBreakdown(t *testing.T) {
	user, system, wait := cpuBreakdown(
		cpuTimes{user: 100, system: 50, iowait: 20, total: 300},
		cpuTimes{user: 140, system: 70, iowait: 30, total: 400},
	)
	assert.InDelta(t, 40, user, 0.001)
	assert.InDelta(t, 20, system, 0.001)
	assert.InDelta(t, 10, wait, 0.001)
}

func TestReadMemoryDetails(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 400 kB\nBuffers: 25 kB\nCached: 200 kB\nSReclaimable: 50 kB\nShmem: 25 kB\nSwapTotal: 500 kB\nSwapFree: 300 kB\n")

	stats, err := NewLinuxCollector(root).readMemory()
	assert.NoError(t, err)
	assert.Equal(t, uint64(400*1024), stats.available)
	assert.Equal(t, uint64(250*1024), stats.cached)
	assert.Equal(t, uint64(600*1024), stats.used)
	assert.Equal(t, uint64(200*1024), stats.swapUsed)
}

func TestReadMemoryWithoutSwapAndClampsAvailable(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 1200 kB\n")

	stats, err := NewLinuxCollector(root).readMemory()
	assert.NoError(t, err)
	assert.Zero(t, stats.used)
	assert.Zero(t, stats.swapTotal)
	assert.Zero(t, stats.swapUsed)
}

func TestReadNetworkDetails(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/net/dev", "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\nlo: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0\nveth0: 300 0 0 0 0 0 0 0 400 0 0 0 0 0 0 0\nenp2s0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n")
	writeFixture(t, root, "sys/class/net/enp2s0/operstate", "up\n")
	writeFixture(t, root, "sys/class/net/enp2s0/speed", "1000\n")
	writeFixture(t, root, "sys/class/net/veth0/operstate", "down\n")
	writeFixture(t, root, "sys/class/net/veth0/speed", "-1\n")

	interfaces, receive, send, err := NewLinuxCollector(root).readNetwork()
	assert.NoError(t, err)
	assert.Equal(t, uint64(400), receive)
	assert.Equal(t, uint64(600), send)
	assert.Equal(t, []NetworkInterface{
		{Name: "enp2s0", Receive: 100, Send: 200, SpeedMbps: 1000, State: "up"},
		{Name: "veth0", Receive: 300, Send: 400, State: "down"},
	}, interfaces)
}

func TestReadVersion(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		expected string
		missing  bool
	}{
		{name: "version and image version", contents: "VERSION=13\nIMAGE_VERSION=20260713134539\n", expected: "13 - 20260713134539"},
		{name: "version only", contents: "VERSION=13\n", expected: "13"},
		{name: "missing file", missing: true, expected: ""},
		{name: "quoted values", contents: "VERSION=\"13 stable\"\nIMAGE_VERSION=\"20260713134539\"\n", expected: "13 stable - 20260713134539"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if !test.missing {
				assert.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
				assert.NoError(t, os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte(test.contents), 0o644))
			}
			version, err := NewLinuxCollector(root).readVersion()
			assert.NoError(t, err)
			assert.Equal(t, test.expected, version)
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		expected string
		value    uint64
	}{
		{expected: "512 B", value: 512},
		{expected: "1.0 KiB", value: 1024},
		{expected: "1.5 GiB", value: 1610612736},
	}
	for _, test := range tests {
		t.Run(test.expected, func(t *testing.T) {
			assert.Equal(t, test.expected, formatBytes(test.value))
		})
	}
}

func writeFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
}
