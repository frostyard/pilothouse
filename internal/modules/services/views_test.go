package services

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageRendersUnitActionsAndProtectsBrokerUnits(t *testing.T) {
	state := State{Units: []Unit{{Name: "backup.timer", ActiveState: "active"}, {Name: "pilothouse.service", ActiveState: "active"}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "/services/backup.timer/stop")
	assert.Contains(t, html, "/services/backup.timer/disable")
	assert.NotContains(t, html, "/services/pilothouse.service/stop")
	assert.NotContains(t, html, "/services/pilothouse.service/disable")
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
