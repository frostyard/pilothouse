package storage

import (
	"testing"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/stretchr/testify/assert"
)

func TestStorageBrokerID(t *testing.T) {
	assert.Equal(t, "org.frostyard.pilothouse.storage.state", broker.QueryStorageState)
}

func TestStableIDIsDeterministicAndNamespaced(t *testing.T) {
	assert.Equal(t, stableID("disk", "8:0"), stableID("disk", "8:0"))
	assert.NotEqual(t, stableID("disk", "8:0"), stableID("filesystem", "8:0"))
	assert.Regexp(t, `^disk:[a-f0-9]{16}$`, stableID("disk", "8:0"))
}

func TestHealthSeverityOrder(t *testing.T) {
	assert.Greater(t, healthRank(HealthCritical), healthRank(HealthWarning))
	assert.Greater(t, healthRank(HealthWarning), healthRank(HealthUnknown))
	assert.Greater(t, healthRank(HealthUnknown), healthRank(HealthHealthy))
}
