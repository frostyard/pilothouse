package system

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCPUPercent(t *testing.T) {
	a := assert.New(t)
	a.InDelta(50, cpuPercent(cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 150, total: 300}), 0.001)
	a.Equal(float64(0), cpuPercent(cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 100, total: 200}))
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
