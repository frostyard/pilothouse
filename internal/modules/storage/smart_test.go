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

func TestParseSMARTAcceptsVerboseATAOutput(t *testing.T) {
	health, err := parseSMART(mustFixture(t, "smart-ata-full.json"), "/dev/sda")

	require.NoError(t, err)
	assert.Equal(t, HealthHealthy, health.Health)
	assert.Contains(t, health.Details, Detail{Label: "Temperature", Value: "37 C"})
}

func TestParseSMARTNVMeMediaErrorsAreCritical(t *testing.T) {
	health, err := parseSMART(mustFixture(t, "smart-nvme.json"), "/dev/nvme0n1")

	require.NoError(t, err)
	assert.Equal(t, HealthCritical, health.Health)
	assert.Contains(t, health.Details, Detail{Label: "Temperature", Value: "71 C"})
	assert.Contains(t, health.Details, Detail{Label: "Percentage used", Value: "82%"})
	assert.Contains(t, health.Details, Detail{Label: "Media errors", Value: "4"})
}

func TestParseSMARTNVMeTemperatureAndWearAreWarning(t *testing.T) {
	health, err := parseSMART([]byte(`{"device":{"name":"/dev/nvme0n1","info_name":"/dev/nvme0n1","protocol":"NVMe"},"smart_status":{"passed":true},"nvme_smart_health_information_log":{"temperature":71,"percentage_used":82,"media_errors":0,"num_err_log_entries":0,"power_on_hours":321}}`), "/dev/nvme0n1")

	require.NoError(t, err)
	assert.Equal(t, HealthWarning, health.Health)
}

func TestParseSMARTUsesNVMeTemperatureWithoutDuplicate(t *testing.T) {
	health, err := parseSMART([]byte(`{"device":{"name":"/dev/nvme0n1","info_name":"/dev/nvme0n1","protocol":"NVMe"},"smart_status":{"passed":true},"temperature":{"current":60},"nvme_smart_health_information_log":{"temperature":71,"percentage_used":10,"media_errors":0,"num_err_log_entries":0,"power_on_hours":321}}`), "/dev/nvme0n1")

	require.NoError(t, err)
	assert.Equal(t, HealthWarning, health.Health)
	assert.Equal(t, 1, detailCount(health.Details, Detail{Label: "Temperature", Value: "71 C"}))
	assert.NotContains(t, health.Details, Detail{Label: "Temperature", Value: "60 C"})
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

func TestSMARTEnricherUsesFixedCommand(t *testing.T) {
	enricher := newSMARTEnricher("/usr/sbin/smartctl")
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
	assert.Equal(t, 2, calls)
}

func TestSMARTEnricherSurfacesFailedDiskDespiteNonZeroExit(t *testing.T) {
	enricher := newSMARTEnricher("/usr/sbin/smartctl")
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"device":{"name":"/dev/sda","info_name":"/dev/sda","protocol":"ATA"},"smart_status":{"passed":false}}`), errors.New("run /usr/sbin/smartctl: exit status 8")
	}

	result, err := enricher.Collect(context.Background(), Inventory{DevicePaths: []string{"/dev/sda"}})

	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Equal(t, HealthCritical, result.Resources[0].Health)
}

func TestSMARTEnricherReportsErrorWhenFailedRunEmitsNoUsableData(t *testing.T) {
	enricher := newSMARTEnricher("/usr/sbin/smartctl")
	enricher.runner.run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("run /usr/sbin/smartctl: exit status 2")
	}

	result, err := enricher.Collect(context.Background(), Inventory{DevicePaths: []string{"/dev/sda"}})

	assert.Error(t, err)
	assert.Empty(t, result.Resources)
}

func TestSMARTEnricherLimitsDeviceReadsToFourWorkers(t *testing.T) {
	enricher := newSMARTEnricher("/usr/sbin/smartctl")
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
