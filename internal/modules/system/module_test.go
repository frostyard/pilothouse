package system

import (
	"context"
	"testing"

	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCollector struct{ snapshot Snapshot }

func (c fakeCollector) Snapshot(context.Context) (Snapshot, error) { return c.snapshot, nil }

func TestHealthReportsResourceThresholds(t *testing.T) {
	module := New(fakeCollector{snapshot: Snapshot{CPUs: 2, DiskPercent: 91, MemoryPercent: 86, Load1: 4.1}})
	findings, err := module.Health(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, []platform.Severity{platform.SeverityCritical, platform.SeverityWarning, platform.SeverityCritical}, []platform.Severity{findings[0].Severity, findings[1].Severity, findings[2].Severity})
	assert.Equal(t, []string{"system.disk", "system.memory", "system.load"}, []string{findings[0].ID, findings[1].ID, findings[2].ID})
}
