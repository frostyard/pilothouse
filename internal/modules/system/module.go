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
		{Component: Metric("cpu", "CPU load", snapshot.CPUPercent, fmt.Sprintf("%.1f%%", snapshot.CPUPercent), fmt.Sprintf("%d logical cores", snapshot.CPUs), "green", nil), Order: 20, Span: platform.SpanThird},
		{Component: Metric("memory", "Memory", snapshot.MemoryPercent, formatBytes(snapshot.MemoryUsed), fmt.Sprintf("of %s", formatBytes(snapshot.MemoryTotal)), "orange", nil), Order: 21, Span: platform.SpanThird},
		{Component: Metric("disk", "Persistent storage", snapshot.DiskPercent, formatBytes(snapshot.DiskUsed), fmt.Sprintf("%s free", formatBytes(snapshot.DiskFree)), "blue", nil), Order: 22, Span: platform.SpanThird},
		{Component: Resources(snapshot), Order: 30, Span: platform.SpanHalf},
	}, nil
}

func (m *Module) Health(ctx context.Context, _ platform.Host) ([]platform.Finding, error) {
	snapshot, err := m.collector.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	findings := make([]platform.Finding, 0, 3)
	if snapshot.DiskPercent >= 90 {
		findings = append(findings, resourceFinding("disk", platform.SeverityCritical, "Persistent storage is nearly full", fmt.Sprintf("/var is %.0f%% used", snapshot.DiskPercent)))
	} else if snapshot.DiskPercent >= 80 {
		findings = append(findings, resourceFinding("disk", platform.SeverityWarning, "Persistent storage is filling up", fmt.Sprintf("/var is %.0f%% used", snapshot.DiskPercent)))
	}
	if snapshot.MemoryPercent >= 95 {
		findings = append(findings, resourceFinding("memory", platform.SeverityCritical, "Memory pressure is critical", fmt.Sprintf("Memory is %.0f%% used", snapshot.MemoryPercent)))
	} else if snapshot.MemoryPercent >= 85 {
		findings = append(findings, resourceFinding("memory", platform.SeverityWarning, "Memory pressure is high", fmt.Sprintf("Memory is %.0f%% used", snapshot.MemoryPercent)))
	}
	loadRatio := snapshot.Load1 / float64(snapshot.CPUs)
	if loadRatio >= 2 {
		findings = append(findings, resourceFinding("load", platform.SeverityCritical, "System load is critical", fmt.Sprintf("1 minute load %.2f across %d CPUs", snapshot.Load1, snapshot.CPUs)))
	} else if loadRatio >= 1 {
		findings = append(findings, resourceFinding("load", platform.SeverityWarning, "System load is elevated", fmt.Sprintf("1 minute load %.2f across %d CPUs", snapshot.Load1, snapshot.CPUs)))
	}
	return findings, nil
}

func resourceFinding(id string, severity platform.Severity, title, detail string) platform.Finding {
	return platform.Finding{ID: "system." + id, Source: "System", Severity: severity, Title: title, Detail: detail, Path: "/system"}
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

func formatUsage(used, total uint64) string {
	if total == 0 {
		return "Not configured"
	}
	return fmt.Sprintf("%s / %s", formatBytes(used), formatBytes(total))
}

func formatCount(value uint64) string {
	const unit = 1000
	if value < unit {
		return fmt.Sprintf("%d", value)
	}
	divisor := float64(unit)
	suffix := "K"
	for _, next := range []string{"M", "B", "T"} {
		if float64(value)/divisor < unit {
			break
		}
		divisor *= unit
		suffix = next
	}
	return fmt.Sprintf("%.1f%s", float64(value)/divisor, suffix)
}

func networkDetail(network NetworkInterface) string {
	if network.SpeedMbps == 0 {
		return network.State + " / speed unknown"
	}
	return fmt.Sprintf("%s / %d Mbps", network.State, network.SpeedMbps)
}

func formatInodePercent(snapshot Snapshot) string {
	if snapshot.InodesTotal == 0 {
		return "Not available"
	}
	return fmt.Sprintf("%.1f%%", snapshot.InodesPercent)
}

func formatInodeFree(snapshot Snapshot) string {
	if snapshot.InodesTotal == 0 {
		return "Not available"
	}
	return formatCount(snapshot.InodesFree)
}
