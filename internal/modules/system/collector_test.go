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
