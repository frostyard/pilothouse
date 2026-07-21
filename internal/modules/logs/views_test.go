package logs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderedLogsComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var output strings.Builder
	require.NoError(t, component.Render(context.Background(), &output))
	return output.String()
}

func logsViewState() State {
	return State{
		Filters: Filters{Query: "panic now", Priority: "warning", Unit: "sshd.service", Window: "6h"},
		Units:   []string{"dbus.socket", "sshd.service"},
		Entries: []Entry{{
			Timestamp: time.Date(2026, 7, 21, 12, 30, 0, 0, time.UTC),
			Priority:  3, Severity: "err", Source: "sshd.service",
			Message: "failed <safely>\nretrying",
		}},
	}
}

func TestPageRendersAllLogFiltersAndSelectedValues(t *testing.T) {
	html := renderedLogsComponent(t, Page(logsViewState(), false))
	for _, value := range []string{`class="card filter-bar"`, `class="filter-bar-actions"`, `name="query"`, `name="priority"`, `name="unit"`, `name="window"`, `value="warning" selected`, `value="sshd.service" selected`, `value="6h" selected`, "Apply filters", "Reset filters", `href="/logs"`} {
		assert.Contains(t, html, value)
	}
}

func TestResultsRendersFiveSecondPollingWithEncodedFilters(t *testing.T) {
	html := renderedLogsComponent(t, Results(logsViewState(), false))
	for _, value := range []string{`id="journal-logs"`, `hx-trigger="every 5s"`, `hx-select="#journal-logs"`, `hx-target="#journal-logs"`, `hx-swap="outerHTML"`, `hx-get="/logs?priority=warning&amp;query=panic+now&amp;unit=sshd.service&amp;window=6h"`} {
		assert.Contains(t, html, value)
	}
}

func TestResultsRendersEscapedMultilineNewestFirstEntries(t *testing.T) {
	html := renderedLogsComponent(t, Results(logsViewState(), false))
	for _, value := range []string{`datetime="2026-07-21T12:30:00Z"`, "err", "sshd.service", "&lt;safely&gt;", "failed &lt;safely&gt;\nretrying", `class="log-message"`} {
		assert.Contains(t, html, value)
	}
}

func TestResultsRendersEmptyFilteredEmptyAndTruncatedStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state State
		want  string
	}{
		{name: "unfiltered", state: State{Filters: Filters{Window: "1h"}}, want: "No journal entries were found in this time window."},
		{name: "filtered", state: State{Filters: Filters{Query: "panic", Window: "1h"}}, want: "No journal entries match these filters."},
		{name: "truncated", state: State{Filters: Filters{Window: "1h"}, Truncated: true}, want: "No matching entries were found before a safety limit was reached."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, renderedLogsComponent(t, Results(tc.state, false)), tc.want)
		})
	}
}

func TestResultsRendersUnavailableStateWithPolling(t *testing.T) {
	html := renderedLogsComponent(t, Results(logsViewState(), true))
	assert.Contains(t, html, "System journal entries are unavailable. Retrying automatically.")
	assert.Contains(t, html, `hx-trigger="every 5s"`)
	assert.Contains(t, html, `hx-get="/logs?priority=warning&amp;query=panic+now&amp;unit=sshd.service&amp;window=6h"`)
}

func TestAccessDeniedRendersComponentWithoutLiteralSyntax(t *testing.T) {
	html := renderedLogsComponent(t, AccessDenied())
	assert.Contains(t, html, "Administrator access required")
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersMissingSelectedUnit(t *testing.T) {
	state := logsViewState()
	state.Filters.Unit = "transient.service"
	html := renderedLogsComponent(t, Page(state, false))
	assert.Contains(t, html, `value="transient.service" selected`)
}
