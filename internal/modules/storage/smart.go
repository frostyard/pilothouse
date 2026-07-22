package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
)

const smartEnricherName = "smart"

var smartctlArgs = []string{"--json=c", "--all"}

type smartEnricher struct {
	path   string
	runner commandRunner
}

func NewSMARTEnricher(path string) Enricher {
	return newSMARTEnricher(path)
}

func newSMARTEnricher(path string) *smartEnricher {
	return &smartEnricher{path: path, runner: commandRunner{limit: maxAdapterBytes}}
}

func (e *smartEnricher) Name() string    { return smartEnricherName }
func (*smartEnricher) CacheHealth() bool { return true }

func (e *smartEnricher) Collect(ctx context.Context, inventory Inventory) (AdapterResult, error) {
	paths := smartDevicePaths(inventory)
	result := AdapterResult{Resources: make([]Resource, 0, len(paths))}
	if len(paths) == 0 {
		return result, nil
	}

	type collected struct {
		health Resource
		path   string
		err    error
	}
	jobs := make(chan string)
	results := make(chan collected, len(paths))
	workers := min(4, len(paths))
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for path := range jobs {
				output, err := e.runner.Run(ctx, e.path, append(slices.Clone(smartctlArgs), path)...)
				if err != nil {
					// smartctl exits non-zero for failing disks while still
					// emitting complete data; surface it instead of dropping it.
					if health, parseErr := parseSMART(output, path); parseErr == nil {
						results <- collected{health: health, path: path}
						continue
					}
					results <- collected{path: path, err: err}
					continue
				}
				health, err := parseSMART(output, path)
				results <- collected{health: health, path: path, err: err}
			}
		}()
	}
	go func() {
		for _, path := range paths {
			jobs <- path
		}
		close(jobs)
		group.Wait()
		close(results)
	}()

	var errs []error
	for collected := range results {
		if collected.err != nil {
			errs = append(errs, fmt.Errorf("collect SMART data for %s: %w", collected.path, collected.err))
			continue
		}
		collected.health.ID = smartResourceID(inventory, collected.path)
		collected.health.Kind = "disk"
		collected.health.Name = collected.path
		collected.health.Path = collected.path
		result.Resources = append(result.Resources, collected.health)
	}
	if len(errs) != 0 {
		return result, errors.Join(errs...)
	}
	return result, nil
}

func smartDevicePaths(inventory Inventory) []string {
	if len(inventory.Resources) == 0 {
		return slices.Clone(inventory.DevicePaths)
	}
	paths := make([]string, 0, len(inventory.DevicePaths))
	for _, path := range inventory.DevicePaths {
		for _, resource := range inventory.Resources {
			if resource.Kind == "disk" && resource.Path == path {
				paths = append(paths, path)
				break
			}
		}
	}
	return paths
}

func smartResourceID(inventory Inventory, path string) string {
	for _, resource := range inventory.Resources {
		if resource.Kind == "disk" && resource.Path == path {
			return resource.ID
		}
	}
	return stableID("disk", path)
}

type smartDocument struct {
	Device struct {
		InfoName string `json:"info_name"`
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	SmartStatus  *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current uint64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours uint64 `json:"hours"`
	} `json:"power_on_time"`
	ATASMARTAttributes *struct {
		Table []struct {
			ID  uint64 `json:"id"`
			Raw struct {
				Value uint64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMESMARTHealthInformationLog *struct {
		Temperature      uint64 `json:"temperature"`
		PercentageUsed   uint64 `json:"percentage_used"`
		MediaErrors      uint64 `json:"media_errors"`
		NumErrLogEntries uint64 `json:"num_err_log_entries"`
		PowerOnHours     uint64 `json:"power_on_hours"`
	} `json:"nvme_smart_health_information_log"`
}

func parseSMART(input []byte, path string) (Resource, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	var document smartDocument
	if err := decoder.Decode(&document); err != nil {
		return Resource{}, fmt.Errorf("decode SMART output: %w", err)
	}
	if decoder.More() {
		return Resource{}, errors.New("decode SMART output: multiple JSON values")
	}
	if document.Device.Name != path {
		return Resource{}, fmt.Errorf("SMART device does not match %s", path)
	}
	if err := validateSMARTFields(document); err != nil {
		return Resource{}, err
	}

	health := HealthUnknown
	if document.SmartStatus != nil {
		health = HealthHealthy
		if !document.SmartStatus.Passed {
			health = HealthCritical
		}
	}
	details := make([]Detail, 0, 9)
	appendSMARTDetail(&details, "Model", document.ModelName)
	appendSMARTDetail(&details, "Serial", document.SerialNumber)
	if document.Temperature != nil && document.NVMESMARTHealthInformationLog == nil {
		appendSMARTDetail(&details, "Temperature", strconv.FormatUint(document.Temperature.Current, 10)+" C")
		if document.Temperature.Current >= 70 {
			health = higherHealth(health, HealthWarning)
		}
	}
	if document.PowerOnTime != nil {
		appendSMARTDetail(&details, "Power-on hours", strconv.FormatUint(document.PowerOnTime.Hours, 10))
	}
	if attributes := document.ATASMARTAttributes; attributes != nil {
		for _, attribute := range attributes.Table {
			switch attribute.ID {
			case 5:
				appendSMARTDetail(&details, "Reallocated sectors", strconv.FormatUint(attribute.Raw.Value, 10))
				if attribute.Raw.Value != 0 {
					health = higherHealth(health, HealthWarning)
				}
			case 197:
				appendSMARTDetail(&details, "Pending sectors", strconv.FormatUint(attribute.Raw.Value, 10))
				if attribute.Raw.Value != 0 {
					health = higherHealth(health, HealthWarning)
				}
			}
		}
	}
	if log := document.NVMESMARTHealthInformationLog; log != nil {
		appendSMARTDetail(&details, "Temperature", strconv.FormatUint(log.Temperature, 10)+" C")
		appendSMARTDetail(&details, "Percentage used", strconv.FormatUint(log.PercentageUsed, 10)+"%")
		appendSMARTDetail(&details, "Media errors", strconv.FormatUint(log.MediaErrors, 10))
		appendSMARTDetail(&details, "Error log entries", strconv.FormatUint(log.NumErrLogEntries, 10))
		appendSMARTDetail(&details, "Power-on hours", strconv.FormatUint(log.PowerOnHours, 10))
		if log.MediaErrors != 0 {
			health = HealthCritical
		} else if log.Temperature >= 70 || log.PercentageUsed >= 80 {
			health = higherHealth(health, HealthWarning)
		}
	}
	return Resource{Health: health, Details: details}, nil
}

func validateSMARTFields(document smartDocument) error {
	for _, field := range []string{document.Device.Name, document.Device.InfoName, document.Device.Protocol, document.ModelName, document.SerialNumber} {
		if len(field) > maxFieldBytes {
			return errors.New("SMART field exceeds limit")
		}
	}
	if document.ATASMARTAttributes != nil && len(document.ATASMARTAttributes.Table) > maxDetails {
		return errors.New("too many SMART attributes")
	}
	return nil
}

func appendSMARTDetail(details *[]Detail, label, value string) {
	if value != "" && len(*details) < maxDetails {
		*details = append(*details, Detail{Label: label, Value: value})
	}
}
