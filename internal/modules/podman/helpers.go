package podman

import (
	"fmt"
	"strings"
)

func runningCount(state State) int {
	count := 0
	for _, container := range state.Containers {
		if container.Running {
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

func stateLabel(container Container) string {
	if container.Running {
		return "Running"
	}
	if container.State == "" {
		return "Stopped"
	}
	return strings.ToUpper(container.State[:1]) + container.State[1:]
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
