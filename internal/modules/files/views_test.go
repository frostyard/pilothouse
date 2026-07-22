package files

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderedFilesComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var output strings.Builder
	require.NoError(t, component.Render(context.Background(), &output))
	return output.String()
}

func filesViewState() State {
	return State{
		Roots: []Root{
			{ID: "archive", Path: "/srv/archive", Writable: false},
			{ID: "safe", Path: "/srv/safe", Writable: true},
		},
		Active: Root{ID: "safe", Path: "/srv/safe", Writable: true},
		Path:   "reports/2026",
		Filters: ListRequest{
			Root: "safe", Path: "reports/2026", Filter: "quarterly report", Sort: "modified", Direction: "desc", Hidden: true,
		},
		Truncated: true,
		Entries: []Entry{
			{Name: "incoming", Type: EntryDirectory, Modified: time.Date(2026, 7, 21, 12, 30, 0, 0, time.UTC), UID: 1000, GID: 1000, Owner: "ops", Group: "ops", Mode: 0o755},
			{Name: "report <final>.txt", Type: EntryRegular, Size: 2048, Modified: time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC), UID: 1000, GID: 1000, Owner: "ops", Group: "ops", Mode: 0o640},
			{Name: "link", Type: EntrySymlink, LinkTarget: "<target>", Modified: time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC), UID: 1000, GID: 1000, Owner: "ops", Group: "ops", Mode: 0o777},
			{Name: "socket", Type: EntryOther, Modified: time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC), UID: 0, GID: 0, Owner: "root", Group: "root", Mode: 0o600},
		},
	}
}

func TestPageRendersFilesBrowser(t *testing.T) {
	html := renderedFilesComponent(t, Page(filesViewState(), "csrf"))
	for _, value := range []string{
		"files-root-bar", "archive", "Read-only", "safe", "Read/write", "/srv/safe",
		`href="/files/archive?direction=desc&amp;filter=quarterly+report&amp;hidden=true&amp;path=&amp;sort=modified"`,
		`href="/files/safe?direction=desc&amp;filter=quarterly+report&amp;hidden=true&amp;path=reports%2F2026&amp;sort=modified"`,
		`href="/files/safe?direction=desc&amp;filter=quarterly+report&amp;hidden=true&amp;path=reports&amp;sort=modified"`,
		`href="/files/safe?direction=asc&amp;filter=quarterly+report&amp;hidden=true&amp;path=reports%2F2026&amp;sort=name"`,
		`name="filter" value="quarterly report"`, `name="hidden" value="true" checked`,
		"4 entries", "Results are truncated by safety limits.",
		`action="/files/safe/upload?path=reports%2F2026"`, `name="csrf" value="csrf"`, `name="file" type="file"`,
		`href="/files/safe?direction=desc&amp;filter=quarterly+report&amp;hidden=true&amp;path=reports%2F2026%2Fincoming&amp;sort=modified"`,
		`href="/files/safe/download?path=reports%2F2026%2Freport+%3Cfinal%3E.txt"`,
		"&lt;target&gt;", "data-label=\"Modified\"", "datetime=\"2026-07-21T12:30:00Z\"",
	} {
		assert.Contains(t, html, value)
	}
	assert.Less(t, strings.Index(html, `name="csrf"`), strings.Index(html, `name="file"`))
	assert.NotContains(t, html, `<target>`)
	assert.NotContains(t, html, `href="/files/safe/download?path=link`)
	assert.NotContains(t, html, "@web.")
}

func TestPageRendersReadOnlyAndEmptyStates(t *testing.T) {
	state := filesViewState()
	state.Active = state.Roots[0]
	state.Filters = ListRequest{Root: "archive", Sort: "name", Direction: "asc"}
	state.Entries = nil
	html := renderedFilesComponent(t, Page(state, "csrf"))
	assert.Contains(t, html, "This root is read-only. Uploads are disabled.")
	assert.Contains(t, html, "This directory is empty.")
	assert.NotContains(t, html, `name="file"`)

	state.Filters.Filter = "missing"
	html = renderedFilesComponent(t, Page(state, "csrf"))
	assert.Contains(t, html, "No entries match the active filter.")
}

func TestPageRendersNoRootsState(t *testing.T) {
	html := renderedFilesComponent(t, Page(State{}, "csrf"))
	assert.Contains(t, html, "No file roots are configured.")
	assert.Contains(t, html, "--files-root")
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}

func TestAccessDeniedRendersComponentWithoutLiteralSyntax(t *testing.T) {
	html := renderedFilesComponent(t, AccessDenied())
	assert.Contains(t, html, "Administrator access required")
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}

func TestUnavailableRendersInaccessibleAndUnavailableStates(t *testing.T) {
	assert.Contains(t, renderedFilesComponent(t, Unavailable(true)), "This directory is inaccessible or no longer exists.")
	html := renderedFilesComponent(t, Unavailable(false))
	assert.Contains(t, html, "Files are temporarily unavailable.")
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}

func TestModuleManifestAndDashboard(t *testing.T) {
	module := New()
	assert.Equal(t, platform.Manifest{ID: "files", Name: "Files", Description: "Browse and transfer configured host files", Icon: "disk", Order: 38, Path: "/files"}, module.Manifest())
	cards, err := module.Dashboard(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, cards)
}
