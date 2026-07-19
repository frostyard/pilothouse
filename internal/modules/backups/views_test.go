package backups

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummaryRendersStatusAndChevronComponent(t *testing.T) {
	state := State{Configured: true, Timers: []Timer{{Name: "nightly.timer", Health: HealthHealthy, Detail: "Last backup completed successfully."}}}
	var output strings.Builder
	require.NoError(t, Summary(state).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "1 healthy")
	assert.Contains(t, html, "nightly.timer")
	assert.Contains(t, html, "m9 18 6-6-6-6")
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersTimerDetailsAndEmptyState(t *testing.T) {
	state := State{Configured: true, Timers: []Timer{{
		Name: "nightly.timer", Service: "nightly.service", ActiveState: "active", UnitFileState: "enabled",
		LastRun: time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC), NextRun: time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC),
		Result: "success", Health: HealthHealthy, Detail: "Last backup completed successfully.",
	}}}
	var output strings.Builder
	require.NoError(t, Page(state).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "nightly.service")
	assert.Contains(t, html, "2026-07-18 01:00 UTC")
	assert.Contains(t, html, "success")
	assert.NotContains(t, html, "@web.")

	output.Reset()
	require.NoError(t, Page(State{}).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "No backup timers are configured.")
	assert.NotContains(t, output.String(), "@web.")
}
