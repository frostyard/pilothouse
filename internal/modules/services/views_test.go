package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageRendersUnitActionsAndProtectsBrokerUnits(t *testing.T) {
	state := State{Units: []Unit{{Name: "backup.timer", ActiveState: "active"}, {Name: "pilothouse.service", ActiveState: "active"}}}
	var output strings.Builder
	require.NoError(t, Page(state, Filters{}, FilterOptions{}, "token", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "/services/backup.timer/stop")
	assert.Contains(t, html, "/services/backup.timer/disable")
	assert.NotContains(t, html, "/services/pilothouse.service/stop")
	assert.NotContains(t, html, "/services/pilothouse.service/disable")
	assert.Contains(t, html, "/services/backup.timer/logs")
	assert.Contains(t, html, "/services/pilothouse.service/logs")
}

func TestPageRendersFiltersAndUnitDescriptionSpacing(t *testing.T) {
	state := State{
		Summary: Summary{Total: 4},
		Units:   []Unit{{Name: "backup.timer", Description: "Nightly backup", ActiveState: "failed", UnitFileState: "enabled"}},
	}
	filters := Filters{Query: "backup", Status: "failed", Type: "timer", UnitFileState: "enabled"}
	options := FilterOptions{Statuses: []string{"active", "inactive", "failed", "activating"}, UnitFileStates: []string{"disabled", "enabled"}}
	var output strings.Builder
	require.NoError(t, Page(state, filters, options, "token", true).Render(context.Background(), &output))

	html := output.String()
	for _, value := range []string{`type="search"`, `value="backup"`, `value="failed" selected`, `value="timer" selected`, `value="enabled" selected`, `name="query" value="backup"`, `name="status" value="failed"`, `name="type" value="timer"`, `name="unit-file" value="enabled"`, `href="/services"`, "Reset filters", "1 of 4 shown", `<small class="table-detail">Nightly backup</small>`} {
		assert.Contains(t, html, value)
	}
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersFilteredEmptyState(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(State{Summary: Summary{Total: 3}}, Filters{Status: "failed"}, FilterOptions{Statuses: []string{"failed"}}, "", false).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "No units match these filters.")
}

func TestLogsRendersEntriesDisclosureAndBackLink(t *testing.T) {
	journal := Journal{Unit: "backup.timer", Description: "Nightly backup", Entries: []JournalEntry{{Timestamp: time.Date(2026, 7, 15, 12, 30, 0, 0, time.UTC), Priority: 3, Severity: "err", Message: "backup failed", Unit: "backup.timer"}}}
	var output strings.Builder
	require.NoError(t, Logs(journal, false).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "Last hour, up to 200 entries")
	assert.Contains(t, html, "Nightly backup")
	assert.Contains(t, html, "backup failed")
	assert.Contains(t, html, "datetime=\"2026-07-15T12:30:00Z\"")
	assert.Contains(t, html, "href=\"/services\"")
	assert.NotContains(t, html, "<script")
}

func TestLogsRendersEmptyAndUnavailableStates(t *testing.T) {
	for _, tc := range []struct {
		unavailable bool
		want        string
	}{{false, "No journal entries were found in the last hour."}, {true, "Recent diagnostics are unavailable. Try again later."}} {
		var output strings.Builder
		require.NoError(t, Logs(Journal{Unit: "backup.timer"}, tc.unavailable).Render(context.Background(), &output))
		assert.Contains(t, output.String(), tc.want)
	}
}

func TestSummaryCardRendersChevronIcon(t *testing.T) {
	state := State{Summary: Summary{Active: 2, Failed: 1, Total: 3}}
	var output strings.Builder
	require.NoError(t, SummaryCard(state).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, "Manage")
	assert.Contains(t, html, "m9 18 6-6-6-6")
	assert.NotContains(t, html, "@web.Icon")
}
