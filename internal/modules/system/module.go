package system

import (
	"context"
	"fmt"
	"net/http"

	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct {
	collector Collector
}

func New(collector Collector) *Module {
	return &Module{collector: collector}
}

func (m *Module) Dashboard(ctx context.Context, _ platform.Host) ([]platform.DashboardCard, error) {
	snapshot, err := m.collector.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{
		{Component: Hero(snapshot), Order: 10, Span: platform.SpanFull},
		{Component: Metric("cpu", "CPU load", snapshot.CPUPercent, fmt.Sprintf("%.1f%%", snapshot.CPUPercent), fmt.Sprintf("%d logical cores", snapshot.CPUs), "green"), Order: 20, Span: platform.SpanThird},
		{Component: Metric("memory", "Memory", snapshot.MemoryPercent, formatBytes(snapshot.MemoryUsed), fmt.Sprintf("of %s", formatBytes(snapshot.MemoryTotal)), "orange"), Order: 21, Span: platform.SpanThird},
		{Component: Metric("disk", "Persistent storage", snapshot.DiskPercent, formatBytes(snapshot.DiskUsed), fmt.Sprintf("%s free", formatBytes(snapshot.DiskFree)), "blue"), Order: 22, Span: platform.SpanThird},
		{Component: Resources(snapshot), Order: 30, Span: platform.SpanHalf},
	}, nil
}

func (m *Module) Manifest() platform.Manifest {
	return platform.Manifest{
		Description: "CPU, memory, storage, network, and host identity",
		Icon:        "server",
		ID:          "system",
		Name:        "System",
		Order:       10,
		Path:        "/system",
	}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /system", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := m.collector.Snapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active:  "system",
			Body:    SystemPage(snapshot),
			Eyebrow: "Host telemetry",
			Title:   "System",
		})
	})
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

func formatUptime(value int64) string {
	days := value / 86400
	hours := (value % 86400) / 3600
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	minutes := (value % 3600) / 60
	return fmt.Sprintf("%dh %dm", hours, minutes)
}
