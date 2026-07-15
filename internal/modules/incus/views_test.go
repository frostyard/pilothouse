package incus

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

func TestPageRendersSelectedProject(t *testing.T) {
	state := State{Project: "production", Projects: []Project{{Name: "default"}, {Name: "production"}}, Instances: []Instance{{Name: "api"}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	assert.Contains(t, output.String(), `value="production" selected`)
	assert.Contains(t, output.String(), `name="project" value="production"`)
}

func TestImagesRenderActionsAndDisabledUsage(t *testing.T) {
	state := State{Project: "production", Images: []Image{{Fingerprint: "free", Name: "free"}, {Fingerprint: "used", Name: "used", Instances: 2}}}
	var output strings.Builder
	require.NoError(t, Page(state, "token", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "Actions")
	assert.Contains(t, html, `/incus/images/free/remove`)
	assert.Contains(t, html, `name="project" value="production"`)
	assert.Contains(t, html, `title="Delete image"`)
	assert.Contains(t, html, `title="In use by 2 instance(s)"`)
	assert.Contains(t, html, "disabled")
	assert.Contains(t, html, `<svg`)
}
