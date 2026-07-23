package logs

import (
	"context"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(context.Context, platform.Host) ([]platform.DashboardCard, error) {
	return nil, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{
		ID: "logs", Name: "Logs", Description: "Inspect the systemd journal",
		Icon: "activity", Order: 37, Path: "/logs",
	}
}

// RequiredCapabilities makes the whole module — its nav entry and its single
// route — available only when the host advertises both Systemd and
// Journald: QueryLogs resolves units via the systemd D-Bus client before
// reading journal entries, per docs/capabilities.md's QueryLogs exception.
func (*Module) RequiredCapabilities() []capability.ID {
	return []capability.ID{capability.Systemd, capability.Journald}
}

func normalizeHTTPFilters(filters Filters) Filters {
	filters.Query = truncateQuery(strings.ReplaceAll(strings.TrimSpace(filters.Query), "\x00", ""))
	if _, ok := PriorityNumber(filters.Priority); !ok {
		filters.Priority = ""
	}
	if !validUnitName(filters.Unit) {
		filters.Unit = ""
	}
	if WindowDuration(filters.Window) == 0 {
		filters.Window = "1h"
	}
	return filters
}

func truncateQuery(query string) string {
	var truncated strings.Builder
	truncated.Grow(min(len(query), queryMaxBytes))
	runes, bytes := 0, 0
	for _, runeValue := range query {
		runeBytes := utf8.RuneLen(runeValue)
		if runes == queryMaxRunes || bytes+runeBytes > queryMaxBytes {
			break
		}
		truncated.WriteRune(runeValue)
		runes++
		bytes += runeBytes
	}
	return truncated.String()
}

func pollURL(filters Filters) templ.SafeURL {
	values := url.Values{
		"priority": {filters.Priority},
		"query":    {filters.Query},
		"unit":     {filters.Unit},
		"window":   {filters.Window},
	}
	return templ.SafeURL("/logs?" + values.Encode())
}
