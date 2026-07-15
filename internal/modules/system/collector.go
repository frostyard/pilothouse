package system

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Collector interface {
	Snapshot(context.Context) (Snapshot, error)
}

type LinuxCollector struct {
	disk string
	mu   sync.Mutex
	root string
}

type Snapshot struct {
	CPUPercent     float64
	CPUs           int
	DiskFree       uint64
	DiskPercent    float64
	DiskTotal      uint64
	DiskUsed       uint64
	Hostname       string
	Kernel         string
	Load1          float64
	Load15         float64
	Load5          float64
	MemoryPercent  float64
	MemoryTotal    uint64
	MemoryUsed     uint64
	NetworkReceive uint64
	NetworkSend    uint64
	OS             string
	Uptime         time.Duration
	Version        string
}

type cpuTimes struct {
	idle  uint64
	total uint64
}

func NewLinuxCollector(root string) *LinuxCollector {
	if root == "" {
		root = "/"
	}
	return &LinuxCollector{disk: filepath.Join(root, "var"), root: root}
}

func (c *LinuxCollector) Snapshot(ctx context.Context) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	first, err := c.readCPU()
	if err != nil {
		return Snapshot{}, err
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-time.After(120 * time.Millisecond):
	}
	second, err := c.readCPU()
	if err != nil {
		return Snapshot{}, err
	}

	memoryTotal, memoryUsed, err := c.readMemory()
	if err != nil {
		return Snapshot{}, err
	}
	diskTotal, diskUsed, diskFree, err := c.readDisk()
	if err != nil {
		return Snapshot{}, err
	}
	load1, load5, load15, err := c.readLoad()
	if err != nil {
		return Snapshot{}, err
	}
	networkReceive, networkSend, err := c.readNetwork()
	if err != nil {
		return Snapshot{}, err
	}
	uptime, err := c.readUptime()
	if err != nil {
		return Snapshot{}, err
	}
	osName, version, err := c.readOSRelease()
	if err != nil {
		return Snapshot{}, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read hostname: %w", err)
	}
	kernel, err := c.readTrimmed("proc/sys/kernel/osrelease")
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		CPUPercent:     cpuPercent(first, second),
		CPUs:           runtime.NumCPU(),
		DiskFree:       diskFree,
		DiskPercent:    percent(diskUsed, diskTotal),
		DiskTotal:      diskTotal,
		DiskUsed:       diskUsed,
		Hostname:       hostname,
		Kernel:         kernel,
		Load1:          load1,
		Load15:         load15,
		Load5:          load5,
		MemoryPercent:  percent(memoryUsed, memoryTotal),
		MemoryTotal:    memoryTotal,
		MemoryUsed:     memoryUsed,
		NetworkReceive: networkReceive,
		NetworkSend:    networkSend,
		OS:             osName,
		Uptime:         uptime,
		Version:        version,
	}, nil
}

func cpuPercent(first, second cpuTimes) float64 {
	total := second.total - first.total
	if total == 0 {
		return 0
	}
	idle := second.idle - first.idle
	return clamp(float64(total-idle) / float64(total) * 100)
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return clamp(float64(used) / float64(total) * 100)
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func (c *LinuxCollector) path(parts ...string) string {
	return filepath.Join(append([]string{c.root}, parts...)...)
}

func (c *LinuxCollector) readCPU() (cpuTimes, error) {
	value, err := c.readTrimmed("proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	line := strings.Split(value, "\n")[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("parse proc/stat: missing aggregate cpu row")
	}
	var values []uint64
	for _, field := range fields[1:] {
		parsed, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuTimes{}, fmt.Errorf("parse proc/stat value %q: %w", field, err)
		}
		values = append(values, parsed)
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return cpuTimes{idle: idle, total: total}, nil
}

func (c *LinuxCollector) readDisk() (uint64, uint64, uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.disk, &stat); err != nil {
		return 0, 0, 0, fmt.Errorf("read disk usage: %w", err)
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	return total, total - free, free, nil
}

func (c *LinuxCollector) readLoad() (float64, float64, float64, error) {
	value, err := c.readTrimmed("proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("parse proc/loadavg: expected three values")
	}
	values := make([]float64, 3)
	for i := range values {
		values[i], err = strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("parse proc/loadavg value %q: %w", fields[i], err)
		}
	}
	return values[0], values[1], values[2], nil
}

func (c *LinuxCollector) readMemory() (uint64, uint64, error) {
	file, err := os.Open(c.path("proc/meminfo"))
	if err != nil {
		return 0, 0, fmt.Errorf("read proc/meminfo: %w", err)
	}
	defer func() { _ = file.Close() }()
	values := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value * 1024
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("scan proc/meminfo: %w", err)
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return 0, 0, fmt.Errorf("parse proc/meminfo: MemTotal missing")
	}
	return total, total - available, nil
}

func (c *LinuxCollector) readNetwork() (uint64, uint64, error) {
	file, err := os.Open(c.path("proc/net/dev"))
	if err != nil {
		return 0, 0, fmt.Errorf("read proc/net/dev: %w", err)
	}
	defer func() { _ = file.Close() }()
	var receive uint64
	var send uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if strings.TrimSpace(parts[0]) == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr == nil && txErr == nil {
			receive += rx
			send += tx
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("scan proc/net/dev: %w", err)
	}
	return receive, send, nil
}

func (c *LinuxCollector) readOSRelease() (string, string, error) {
	value, err := c.readTrimmed("etc/os-release")
	if err != nil {
		return "Linux", "", nil
	}
	osName := "Linux"
	var version string
	var imageVersion string
	for _, line := range strings.Split(value, "\n") {
		if name, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			osName = strings.Trim(name, `"`)
		}
		if parsed, ok := strings.CutPrefix(line, "VERSION="); ok {
			version = strings.Trim(parsed, `"`)
		}
		if parsed, ok := strings.CutPrefix(line, "IMAGE_VERSION="); ok {
			imageVersion = strings.Trim(parsed, `"`)
		}
	}
	if version != "" && imageVersion != "" {
		return osName, version + " - " + imageVersion, nil
	}
	if imageVersion != "" {
		return osName, imageVersion, nil
	}
	return osName, version, nil
}

func (c *LinuxCollector) readVersion() (string, error) {
	_, version, err := c.readOSRelease()
	return version, err
}

func (c *LinuxCollector) readTrimmed(path string) (string, error) {
	value, err := os.ReadFile(c.path(path))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(value)), nil
}

func (c *LinuxCollector) readUptime() (time.Duration, error) {
	value, err := c.readTrimmed("proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, fmt.Errorf("parse proc/uptime: missing uptime")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse proc/uptime: %w", err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
