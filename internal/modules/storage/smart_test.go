package storage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSMARTATAFailureIsCritical(t *testing.T) {
	health, err := parseSMART(mustFixture(t, "smart-ata.json"), "/dev/sda")

	require.NoError(t, err)
	assert.Equal(t, HealthCritical, health.Health)
	assert.Contains(t, health.Details, Detail{Label: "Model", Value: "Example ATA SSD"})
	assert.Contains(t, health.Details, Detail{Label: "Reallocated sectors", Value: "8"})
	assert.Contains(t, health.Details, Detail{Label: "Pending sectors", Value: "2"})
}

func TestParseSMARTNVMeWarning(t *testing.T) {
	health, err := parseSMART(mustFixture(t, "smart-nvme.json"), "/dev/nvme0n1")

	require.NoError(t, err)
	assert.Equal(t, HealthWarning, health.Health)
	assert.Contains(t, health.Details, Detail{Label: "Temperature", Value: "71 C"})
	assert.Contains(t, health.Details, Detail{Label: "Percentage used", Value: "82%"})
	assert.Contains(t, health.Details, Detail{Label: "Media errors", Value: "4"})
}

func TestParseSMARTHealthyDevices(t *testing.T) {
	tests := []struct {
		name  string
		input string
		path  string
	}{
		{"ATA", `{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"},"smart_status":{"passed":true},"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":0}},{"id":197,"raw":{"value":0}}]}}`, "/dev/sda"},
		{"NVMe", `{"device":{"name":"/dev/nvme0n1","info_name":"/dev/nvme0n1","protocol":"NVMe"},"smart_status":{"passed":true},"nvme_smart_health_information_log":{"temperature":35,"percentage_used":10,"media_errors":0,"num_err_log_entries":0,"power_on_hours":1}}`, "/dev/nvme0n1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			health, err := parseSMART([]byte(test.input), test.path)
			require.NoError(t, err)
			assert.Equal(t, HealthHealthy, health.Health)
		})
	}
}

func TestParseSMARTReturnsUnknownWithoutOverallHealth(t *testing.T) {
	health, err := parseSMART([]byte(`{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"}}`), "/dev/sda")

	require.NoError(t, err)
	assert.Equal(t, HealthUnknown, health.Health)
}

func TestParseSMARTRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		path  string
	}{
		{"mismatched device", `{"device":{"name":"/dev/sdb","info_name":"/dev/sdb","protocol":"ATA"}}`, "/dev/sda"},
		{"malformed health", `{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"},"smart_status":{"passed":"yes"}}`, "/dev/sda"},
		{"overflow counter", `{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"},"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":18446744073709551616}}]}}`, "/dev/sda"},
		{"oversized field", `{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"},"model_name":"` + strings.Repeat("a", maxFieldBytes+1) + `"}`, "/dev/sda"},
		{"unknown structure", `{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"},"unrecognized":{"value":true}}`, "/dev/sda"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseSMART([]byte(test.input), test.path)
			assert.Error(t, err)
		})
	}
}

func TestParseSMARTRejectsMultipleJSONValues(t *testing.T) {
	_, err := parseSMART([]byte(`{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"}} {}`), "/dev/sda")

	assert.Error(t, err)
}

func TestSMARTEnricherUsesFixedCommandAndCache(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newHealthCache(func() time.Time { return now })
	enricher := newSMARTEnricher("/usr/sbin/smartctl", cache)
	var calls int
	enricher.runner.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
		calls++
		assert.Equal(t, "/usr/sbin/smartctl", path)
		assert.Equal(t, []string{"--json=c", "--all", "/dev/nvme0n1"}, args)
		return mustFixture(t, "smart-nvme.json"), nil
	}

	result, err := enricher.Collect(context.Background(), Inventory{DevicePaths: []string{"/dev/nvme0n1"}})
	require.NoError(t, err)
	assert.Len(t, result.Resources, 1)
	_, err = enricher.Collect(context.Background(), Inventory{DevicePaths: []string{"/dev/nvme0n1"}})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestSMARTEnricherReturnsStaleDataAfterRefreshFailure(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newHealthCache(func() time.Time { return now })
	enricher := newSMARTEnricher("/usr/sbin/smartctl", cache)
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		return mustFixture(t, "smart-nvme.json"), nil
	}
	_, err := enricher.Collect(context.Background(), Inventory{DevicePaths: []string{"/dev/nvme0n1"}})
	require.NoError(t, err)
	now = now.Add(healthCacheTTL + time.Second)
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }

	result, err := enricher.Collect(context.Background(), Inventory{DevicePaths: []string{"/dev/nvme0n1"}})

	require.Error(t, err)
	assert.Contains(t, result.Resources[0].Details, staleHealthDetail)
}

func TestSMARTEnricherLimitsDeviceReadsToFourWorkers(t *testing.T) {
	enricher := newSMARTEnricher("/usr/sbin/smartctl", NewHealthCache())
	started := make(chan struct{}, 5)
	release := make(chan struct{})
	var once sync.Once
	enricher.runner.run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		started <- struct{}{}
		select {
		case <-release:
			return mustFixture(t, "smart-nvme.json"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = enricher.Collect(context.Background(), Inventory{DevicePaths: []string{"/dev/nvme0n1", "/dev/nvme1n1", "/dev/nvme2n1", "/dev/nvme3n1", "/dev/nvme4n1"}})
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("four workers did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("started more than four device reads")
	case <-time.After(20 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	<-done
}
