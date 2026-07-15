package incus

import (
	"fmt"
	"strings"
)

func runningCount(state State) int {
	count := 0
	for _, instance := range state.Instances {
		if instance.Running {
			count++
		}
	}
	return count
}

func imageBytes(state State) uint64 {
	var total uint64
	for _, image := range state.Images {
		total += image.Size
	}
	return total
}

func formatBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor := uint64(unit)
	exponent := 0
	for quotient := value / unit; quotient >= unit && exponent < 5; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func stateLabel(instance Instance) string {
	if instance.Status == "" {
		return "Stopped"
	}
	return strings.ToUpper(instance.Status[:1]) + instance.Status[1:]
}
