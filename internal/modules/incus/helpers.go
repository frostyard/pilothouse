package incus

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"
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

func memoryBytes(state State) uint64 {
	var total uint64
	for _, instance := range state.Instances {
		total += instance.Memory
	}
	return total
}

// addressLabel renders an instance's globally-scoped addresses. A stopped
// instance reports none, which is a real absence rather than a missing
// read, so it renders as a dash.
func addressLabel(instance Instance) string {
	return joinValues(instance.Addresses)
}

func memoryLabel(instance Instance) string {
	if instance.Memory == 0 {
		return "—"
	}
	return formatBytes(instance.Memory)
}

func memoryTotalLabel(detail Detail) string {
	if detail.MemoryTotal == 0 {
		return "no reported total"
	}
	return "of " + formatBytes(detail.MemoryTotal)
}

func processLabel(instance Instance) string {
	if instance.Processes == 0 {
		return "—"
	}
	return fmt.Sprint(instance.Processes)
}

func cpuLabel(instance Instance) string {
	if instance.CPUTime == 0 {
		return "no CPU time reported"
	}
	return time.Duration(instance.CPUTime).Round(time.Second).String() + " CPU time"
}

// uptimeLabel reports the recorded start timestamp rather than an elapsed
// duration, so the rendered page does not depend on the wall clock.
func uptimeLabel(instance Instance) string {
	if instance.StartedAt == "" {
		return "not started"
	}
	return "started " + instance.StartedAt
}

func joinValues(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	return strings.Join(values, ", ")
}

func entryLabel(entries []ConfigEntry) string {
	if len(entries) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, entry.Key+"="+entry.Value)
	}
	return strings.Join(parts, ", ")
}

func projectLink(project string) templ.SafeURL {
	return templ.SafeURL("/incus?" + url.Values{"project": {project}}.Encode())
}

func instanceLink(project, name string) templ.SafeURL {
	return templ.SafeURL("/incus/instances/" + url.PathEscape(name) + "?" + url.Values{"project": {project}}.Encode())
}

func logsLink(project, name, source string) templ.SafeURL {
	values := url.Values{"project": {project}, "source": {source}}
	return templ.SafeURL("/incus/instances/" + url.PathEscape(name) + "/logs?" + values.Encode())
}

func sourceLabel(source string) string {
	if source == SourceConsole {
		return "Console log"
	}
	return "Supervisor log"
}

func sourceDescription(source string) string {
	if source == SourceConsole {
		return "The instance's console ring buffer — boot and console output."
	}
	return "The supervisor log: lxc.log for a container, qemu.log for a virtual machine."
}

// sourceAlternate returns the other supported source, so the logs page can
// offer exactly one toggle without enumerating sources in the view.
func sourceAlternate(source string) string {
	if source == SourceConsole {
		return SourceLog
	}
	return SourceConsole
}

// countLabel renders a count with a correctly singularised noun, so a page
// never reads "1 snapshots".
func countLabel(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// unavailableLabel explains an unreadable log source. A console ring buffer
// that was never enabled fails permanently rather than transiently, so the
// console wording does not suggest retrying.
func unavailableLabel(source string) string {
	if source == SourceConsole {
		return "The console log could not be read. This instance may not have console logging enabled."
	}
	return "The supervisor log could not be read. Try again later."
}

func snapshotCreateLink(instance string) templ.SafeURL {
	return templ.SafeURL("/incus/instances/" + url.PathEscape(instance) + "/snapshots")
}

func snapshotActionLink(instance, snapshot, action string) templ.SafeURL {
	return templ.SafeURL("/incus/instances/" + url.PathEscape(instance) + "/snapshots/" + url.PathEscape(snapshot) + "/" + action)
}
