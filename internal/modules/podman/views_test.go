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
	state := State{Images: []Image{{ID: "free", Name: "free"}, {ID: "used", Name: "used", Containers: 2}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "Actions")
	assert.Contains(t, html, `/podman/images/free/remove`)
	assert.Contains(t, html, `title="Delete image"`)
	assert.Contains(t, html, `title="In use by 2 container(s)"`)
	assert.Contains(t, html, "disabled")
	assert.Contains(t, html, `<svg`)
}
