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
