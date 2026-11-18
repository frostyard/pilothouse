package docker

import (
	"strings"

	"github.com/frostyard/pilothouse/internal/web"
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
	return web.FormatBytes(value)
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
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
