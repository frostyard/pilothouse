package web

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardDoesNotBoostLinksInsidePollingRegion(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Dashboard(nil).Render(context.Background(), &output))
	assert.Contains(t, output.String(), `hx-boost="false"`)
	assert.Contains(t, output.String(), `hx-select="#dashboard"`)
}

func TestLayoutRendersFrostyardShellAndComponents(t *testing.T) {
	var output strings.Builder
	data := LayoutData{
		Active:   "fleet",
		CSRF:     "csrf",
		Eyebrow:  "Fleet preview",
		Identity: auth.Identity{Admin: true, Username: "snow"},
		Modules: []platform.Manifest{
			{ID: "fleet", Name: "Fleet", Path: "/fleet"},
			{ID: "services", Name: "Services", Path: "/services"},
		},
		Path:  "/fleet",
		Title: "Systems",
	}
	require.NoError(t, Layout(data, templ.Raw("<p>content</p>")).Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, "frostyard admin")
	assert.Contains(t, html, `<span class="nav-number">01</span>`)
	assert.Contains(t, html, `<span class="nav-number">02</span>`)
	assert.Contains(t, html, `<span class="nav-number">03</span>`)
	assert.Contains(t, html, `href="/fleet" class="nav-link active"`)
	assert.Contains(t, html, "<svg")
	assert.Contains(t, html, "<p>content</p>")
	assert.NotContains(t, html, "@Icon(")
	assert.NotContains(t, html, "@body")
}

func TestLoginRendersLocalArtwork(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Login("Try again", "snow", "csrf").Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, `src="/static/frozen-reflection.png"`)
	assert.Contains(t, html, "Try again")
	assert.Contains(t, html, `value="snow"`)
}
