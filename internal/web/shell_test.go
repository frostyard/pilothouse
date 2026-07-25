package web

import (
	"context"
	"regexp"
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

// systemPickerRegionPattern isolates the sidebar's system-picker block — the
// markup between its opening <div class="system-picker"> and the primary
// navigation that follows it. Per docs/agents/skills/scope-html-assertions-
// to-the-region-under-test.md, a whole-page Contains check could not tell
// "the system-picker still hardcodes a /fleet link" apart from "the nav loop
// rendered a legitimate /fleet entry", since both regions carry the same
// href; scoping to this region proves the picker independently.
var systemPickerRegionPattern = regexp.MustCompile(`(?s)<div class="system-picker">(.*?)<nav\b`)

func systemPickerRegion(t *testing.T, html string) string {
	t.Helper()
	match := systemPickerRegionPattern.FindStringSubmatch(html)
	require.NotNilf(t, match, "could not locate the system-picker region in rendered HTML: %s", html)
	return match[1]
}

// TestLayoutSystemPickerLinksFleetOnlyWhenRegistered proves both directions of
// the system-picker's fleet link. The link is derived from data.Modules — the
// same already-filtered manifest list the nav loops read — so it tracks
// module registration (cmd/pilothouse's --dev flag) rather than a hardcoded
// href that could point at an unregistered, 404ing route.
func TestLayoutSystemPickerLinksFleetOnlyWhenRegistered(t *testing.T) {
	base := LayoutData{
		CSRF:     "csrf",
		Identity: auth.Identity{Admin: true, Username: "snow"},
		Path:     "/",
		Title:    "Overview",
	}

	t.Run("fleet registered", func(t *testing.T) {
		data := base
		data.Modules = []platform.Manifest{
			{ID: "fleet", Name: "Fleet", Path: "/fleet"},
			{ID: "services", Name: "Services", Path: "/services"},
		}
		var output strings.Builder
		require.NoError(t, Layout(data, templ.Raw("<p>content</p>")).Render(context.Background(), &output))

		region := systemPickerRegion(t, output.String())
		assert.Contains(t, region, `href="/fleet"`)
		assert.Contains(t, region, "connected · this host")
		assert.NotContains(t, region, "@web.")
		assert.NotContains(t, region, "@Icon(")
	})

	t.Run("fleet not registered", func(t *testing.T) {
		data := base
		data.Modules = []platform.Manifest{
			{ID: "services", Name: "Services", Path: "/services"},
			{ID: "storage", Name: "Storage", Path: "/storage"},
		}
		var output strings.Builder
		require.NoError(t, Layout(data, templ.Raw("<p>content</p>")).Render(context.Background(), &output))

		html := output.String()
		region := systemPickerRegion(t, html)
		assert.NotContains(t, region, `href="/fleet"`)
		assert.NotContains(t, region, "@web.")
		assert.NotContains(t, region, "@Icon(")
		// The picker still identifies the local system; only the link into
		// the unregistered Fleet preview is gone.
		assert.Contains(t, region, "connected · this host")
		// Site (c): the nav loops key off the same list, so they no-op for
		// fleet without any code change of their own.
		assert.NotContains(t, html, `href="/fleet"`)
	})
}

func TestLoginRendersLocalArtwork(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Login("Try again", "snow", "csrf").Render(context.Background(), &output))

	html := output.String()
	assert.Contains(t, html, `src="/static/frozen-reflection.png"`)
	assert.Contains(t, html, "Try again")
	assert.Contains(t, html, `value="snow"`)
}
