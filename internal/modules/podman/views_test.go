package podman

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummaryRendersIconComponent(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Summary(State{}).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "<svg")
	assert.NotContains(t, output.String(), "@web.Icon")
}

func TestImagesRenderActionsAndDisabledUsage(t *testing.T) {
	state := State{Containers: []Container{{ID: runningID, Name: "api"}}, Images: []Image{{ID: "free", Name: "free"}, {ID: "used", Name: "used", Containers: 2}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "Actions")
	assert.Contains(t, html, `/podman/images/free/remove`)
	assert.Contains(t, html, `title="Delete image"`)
	assert.Contains(t, html, `title="In use by 2 container(s)"`)
	assert.Contains(t, html, "disabled")
	assert.Contains(t, html, `<svg`)
	assert.Contains(t, html, `/podman/containers/`+runningID+`/logs`)
}

func TestLogsViewRendersLinesAndStates(t *testing.T) {
	logs := Logs{ID: runningID, Name: "api", Lines: []LogLine{{Timestamp: "2026-01-02T03:04:05Z", Stream: "stdout", Message: "ready"}}}
	var output strings.Builder
	require.NoError(t, LogsView(logs, false).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "ready")
	assert.Contains(t, html, "2026-01-02T03:04:05Z")
	assert.Contains(t, html, `hx-trigger="every 5s"`)
	assert.NotContains(t, html, "@web.")
	output.Reset()
	require.NoError(t, LogsView(logs, true).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Recent container logs are unavailable")
}
