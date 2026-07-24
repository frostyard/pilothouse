package sysext

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderPage renders Page with the given state and flags. Every test below
// goes through the real component rather than a string fixture, so a
// regression in the templ source is what fails the assertion.
func renderPage(t *testing.T, state ExtensionsState, admin, enableDisable, refresh, update bool) string {
	t.Helper()
	builder := &strings.Builder{}
	require.NoError(t, Page(state, "csrf-token", admin, enableDisable, refresh, update).Render(context.Background(), builder))
	return builder.String()
}

func renderSummary(t *testing.T, state ExtensionsState) string {
	t.Helper()
	builder := &strings.Builder{}
	require.NoError(t, Summary(state).Render(context.Background(), builder))
	return builder.String()
}

// mixedState carries one extension of each shape the row-level assertions
// need: managed (may show enable/disable), unmanaged-but-installed (never
// may), and merged-but-disabled (managed, so it may, and it is the shape
// maintenance derives its reboot reason from). Only the managed one has a
// pending update, so the "Update available" badge is provable both ways.
func mixedState() ExtensionsState {
	return ExtensionsState{
		SysextAvailable: true,
		UpdexAvailable:  true,
		Extensions: []Extension{
			{
				Description: "Managed by updex",
				Enabled:     true,
				Installed:   true,
				Managed:     true,
				Name:        "managed-ext",
				Updates: []AvailableUpdate{
					{Feature: "managed-ext", Component: "managed-component", Current: "1.0.0", Newest: "1.1.0"},
				},
				Version: "1.0.0",
			},
			{Installed: true, Name: "unmanaged-ext", Version: "2.0.0"},
			{Managed: true, Merged: true, Name: "merged-disabled-ext", Version: "3.0.0"},
		},
	}
}

// extensionRow isolates one extension's <tr> so a per-row assertion cannot
// be satisfied by markup belonging to a different row or to another section
// of the same page, per docs/agents/skills/scope-html-assertions-to-the-
// region-under-test.md.
func extensionRow(t *testing.T, body, name string) string {
	t.Helper()
	table := section(t, body, "Available extensions")
	rows := strings.Split(table, "<tr>")
	for _, row := range rows {
		if strings.Contains(row, ">"+name+"<") {
			return row
		}
	}
	t.Fatalf("no row for extension %q in the extensions table", name)
	return ""
}

// section isolates the <article> card whose toolbar carries the given
// heading, so "Available extensions" assertions cannot be satisfied by the
// "Available updates" card or vice versa.
func section(t *testing.T, body, heading string) string {
	t.Helper()
	cards := strings.Split(body, `<article class="card table-card">`)
	for _, card := range cards {
		if strings.Contains(card, "<h2>"+heading+"</h2>") {
			return card
		}
	}
	t.Fatalf("no card with heading %q", heading)
	return ""
}

// TestPageRendersComponentsNotLiteralCallSyntax is the guard AGENTS.md
// requires for every changed templ component invocation: a component
// embedded in a text node renders its call syntax literally instead of its
// output.
func TestPageRendersComponentsNotLiteralCallSyntax(t *testing.T) {
	for name, body := range map[string]string{
		"page":    renderPage(t, mixedState(), true, true, true, true),
		"summary": renderSummary(t, mixedState()),
		"empty":   renderPage(t, ExtensionsState{}, true, true, true, true),
	} {
		assert.NotContains(t, body, "@web.", "%s: templ component call syntax leaked into the rendered HTML", name)
		assert.Contains(t, body, "<svg", "%s: expected at least one icon component to have rendered", name)
	}
}

// TestPerRowControlsRespectManagedAndCapabilities is the row-level view
// audit: every enable/disable form belongs to the same gated route family,
// so all of them collapse behind one flag — and an unmanaged extension has
// no updex definition, so it never renders one at any flag setting.
func TestPerRowControlsRespectManagedAndCapabilities(t *testing.T) {
	state := mixedState()

	available := renderPage(t, state, true, true, true, true)
	assert.Contains(t, extensionRow(t, available, "managed-ext"), "/sysext/managed-ext/disable",
		"an enabled managed extension should offer its disable control")
	assert.Contains(t, extensionRow(t, available, "merged-disabled-ext"), "/sysext/merged-disabled-ext/enable",
		"a merged-but-disabled managed extension should offer its enable control")
	unmanagedRow := extensionRow(t, available, "unmanaged-ext")
	assert.NotContains(t, unmanagedRow, "/sysext/unmanaged-ext/enable")
	assert.NotContains(t, unmanagedRow, "/sysext/unmanaged-ext/disable")
	assert.Contains(t, unmanagedRow, "Unmanaged",
		"an unmanaged extension should say so rather than silently render no control")

	// Same populated rows, capability absent: every per-row form disappears
	// together, including the ones the managed rows had above.
	gated := renderPage(t, state, true, false, true, true)
	for _, name := range []string{"managed-ext", "merged-disabled-ext", "unmanaged-ext"} {
		row := extensionRow(t, gated, name)
		assert.NotContains(t, row, "/sysext/"+name+"/enable",
			"%s: enable form must not render without both tools", name)
		assert.NotContains(t, row, "/sysext/"+name+"/disable",
			"%s: disable form must not render without both tools", name)
	}

	// A non-admin sees no per-row control regardless of capabilities.
	readOnly := renderPage(t, state, false, true, true, true)
	assert.NotContains(t, readOnly, "/sysext/managed-ext/disable")
	assert.Contains(t, readOnly, "No permission")
}

// TestGlobalActionsCollapseIndependently pins that the refresh and update
// buttons follow their own capabilities, not each other's.
func TestGlobalActionsCollapseIndependently(t *testing.T) {
	state := mixedState()

	both := renderPage(t, state, true, true, true, true)
	assert.Contains(t, both, `action="/sysext/actions/refresh"`)
	assert.Contains(t, both, `action="/sysext/actions/update"`)

	refreshOnly := renderPage(t, state, true, true, true, false)
	assert.Contains(t, refreshOnly, `action="/sysext/actions/refresh"`)
	assert.NotContains(t, refreshOnly, `action="/sysext/actions/update"`)

	updateOnly := renderPage(t, state, true, true, false, true)
	assert.NotContains(t, updateOnly, `action="/sysext/actions/refresh"`)
	assert.Contains(t, updateOnly, `action="/sysext/actions/update"`)

	neither := renderPage(t, state, true, true, false, false)
	assert.NotContains(t, neither, `action="/sysext/actions/refresh"`)
	assert.NotContains(t, neither, `action="/sysext/actions/update"`)
}

// TestSummaryCountsPendingUpdates covers the "Updates" mini-row that moved
// here from maintenance.Summary: it is the sum of len(Updates) across every
// extension, not a count of extensions that have any.
func TestSummaryCountsPendingUpdates(t *testing.T) {
	state := mixedState()
	// Two components pending on one extension, one on another, none on the
	// third — so a per-extension count (2) and a per-component count (3)
	// cannot both pass.
	state.Extensions[0].Updates = append(state.Extensions[0].Updates,
		AvailableUpdate{Feature: "managed-ext", Component: "second-component", Current: "1.0.0", Newest: "1.2.0"})
	state.Extensions[2].Updates = []AvailableUpdate{
		{Feature: "merged-disabled-ext", Component: "third-component", Current: "3.0.0", Newest: "3.1.0"},
	}

	body := renderSummary(t, state)
	assert.Contains(t, body, "<strong>Updates</strong><small>Enabled system extensions</small>")
	assert.Contains(t, body, `<span class="badge">3</span>`,
		"the Updates mini-row must sum pending components across every extension")

	none := mixedState()
	for index := range none.Extensions {
		none.Extensions[index].Updates = nil
	}
	assert.Contains(t, renderSummary(t, none), `<span class="badge">0</span>`)
}

// TestAvailableUpdatesSection covers the table relocated from the
// Maintenance page, including its original empty-state copy.
func TestAvailableUpdatesSection(t *testing.T) {
	populated := section(t, renderPage(t, mixedState(), true, true, true, true), "Available updates")
	for _, want := range []string{
		"<th>Extension</th>", "<th>Component</th>", "<th>Current</th>", "<th>Newest</th>",
		"managed-ext", "managed-component", "1.0.0", "1.1.0",
	} {
		assert.Contains(t, populated, want, "the Available updates table should render %q", want)
	}
	assert.NotContains(t, populated, "Enabled extensions are up to date.",
		"the empty state must not render alongside pending updates")

	// Every extension present, none with updates: the section stays and
	// shows the relocated empty-state copy.
	none := mixedState()
	for index := range none.Extensions {
		none.Extensions[index].Updates = nil
	}
	empty := section(t, renderPage(t, none, true, true, true, true), "Available updates")
	assert.Contains(t, empty, "Enabled extensions are up to date.")
	assert.NotContains(t, empty, "managed-component")
}

// TestUpdateAvailableBadge pins the per-row badge to the row's own Updates
// slice — asserted inside the isolated row so the "Available updates" table
// below cannot satisfy it.
func TestUpdateAvailableBadge(t *testing.T) {
	body := renderPage(t, mixedState(), true, true, true, true)
	assert.Contains(t, extensionRow(t, body, "managed-ext"), "Update available",
		"an extension with pending updates should carry the badge")
	assert.NotContains(t, extensionRow(t, body, "unmanaged-ext"), "Update available",
		"an extension with no pending updates must not carry the badge")
	assert.NotContains(t, extensionRow(t, body, "merged-disabled-ext"), "Update available")
}

// TestEmptyInventoryRendersEmptyStates covers the successful empty state: a
// host with a tool present but nothing installed or defined.
func TestEmptyInventoryRendersEmptyStates(t *testing.T) {
	body := renderPage(t, ExtensionsState{SysextAvailable: true, UpdexAvailable: true}, true, true, true, true)
	assert.Contains(t, section(t, body, "Available extensions"), "No extension definitions were found.")
	assert.Contains(t, section(t, body, "Available updates"), "Enabled extensions are up to date.")
}

// TestActiveFirst covers the ordering helper behind Summary's mini-list:
// merged extensions first, then merely-enabled ones, then the rest by name.
// It moved here with the rest of the view helpers when the exec-backed
// manager left this package for internal/modules/sysext/extctl.
func TestActiveFirst(t *testing.T) {
	extensions := []Extension{{Name: "available"}, {Enabled: true, Name: "enabled"}, {Merged: true, Name: "merged"}}
	sorted := activeFirst(extensions)
	assert.Equal(t, []string{"merged", "enabled", "available"}, []string{sorted[0].Name, sorted[1].Name, sorted[2].Name})
}
