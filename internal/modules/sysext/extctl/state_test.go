package extctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/modules/sysext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// *SystemManager is the only production sysext.ExtensionsSource; cmd/pilothoused's
// registerExtensions serves broker.QueryExtensionsState from this interface,
// so the concrete manager must keep satisfying it.
var _ sysext.ExtensionsSource = (*SystemManager)(nil)

// stateFixture builds a SystemManager over a temp definitions root holding one
// sysupdate.d directory with a feature file (so definitionDirectories finds it)
// and a runner whose canned outputs cover every command State can issue:
// updex's per-directory feature list and feature check, and systemd-sysext's
// list and status. Callers override individual entries -- or move one to the
// error map -- to build each degradation case.
//
// The canned inventory is deliberately mixed, exactly as the criteria require:
//
//	docker -- managed AND installed AND merged, with one pending update
//	zinc   -- managed only, enabled=false, with NO pending update
//	         (its check result reports update_available=false)
//	legacy -- installed only, no updex definition at all: unmanaged, read-only
type stateFixture struct {
	definitions string
	root        string
	runner      *fakeRunner
}

func newStateFixture(t *testing.T) *stateFixture {
	t.Helper()
	root := t.TempDir()
	definitions := filepath.Join(root, "sysupdate.d")
	require.NoError(t, os.MkdirAll(definitions, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(definitions, "docker.feature"), []byte("[Feature]"), 0o644))
	fixture := &stateFixture{definitions: definitions, root: root, runner: &fakeRunner{
		errors: map[string]error{},
		outputs: map[string][]byte{
			strings.Join([]string{"updex", "-C", definitions, "--json", "features", "list"}, " "): []byte(
				`[{"name":"docker","description":"Docker engine","enabled":true},{"name":"zinc","description":"Zinc","enabled":false}]`),
			strings.Join([]string{"updex", "-C", definitions, "--json", "features", "check"}, " "): []byte(
				`[{"feature":"docker","results":[{"component":"engine","current_version":"1","newest_version":"2","update_available":true}]},` +
					`{"feature":"zinc","results":[{"component":"base","current_version":"3","newest_version":"3","update_available":false}]}]`),
			"systemd-sysext list --json=short --no-pager": []byte(
				`[{"name":"docker","path":"/not-present/docker_9.9_13_x86-64.raw"},{"name":"legacy","path":"/not-present/legacy.raw"}]`),
			"systemd-sysext status --json=short --no-pager": []byte(`[{"hierarchy":"/usr","extensions":["docker"]}]`),
		},
	}}
	return fixture
}

func (f *stateFixture) featuresListKey() string {
	return strings.Join([]string{"updex", "-C", f.definitions, "--json", "features", "list"}, " ")
}

func (f *stateFixture) featuresCheckKey() string {
	return strings.Join([]string{"updex", "-C", f.definitions, "--json", "features", "check"}, " ")
}

func (f *stateFixture) manager() *SystemManager {
	return NewSystemManager(f.runner, f.root, "updex")
}

// empty replaces the whole inventory with successful, genuinely empty answers
// from both tools: no feature definitions, no pending updates, nothing
// installed, nothing merged.
func (f *stateFixture) empty() *stateFixture {
	f.runner.outputs[f.featuresListKey()] = []byte(`[]`)
	f.runner.outputs[f.featuresCheckKey()] = []byte(`[]`)
	f.runner.outputs["systemd-sysext list --json=short --no-pager"] = []byte(`[]`)
	f.runner.outputs["systemd-sysext status --json=short --no-pager"] = []byte(`[]`)
	return f
}

func (f *stateFixture) invoked(command string) bool {
	for _, call := range f.runner.calls {
		if call[0] == command {
			return true
		}
	}
	return false
}

func extensionByName(t *testing.T, state sysext.ExtensionsState, name string) sysext.Extension {
	t.Helper()
	for _, extension := range state.Extensions {
		if extension.Name == name {
			return extension
		}
	}
	require.FailNowf(t, "extension missing", "no extension named %q in %v", name, state.Extensions)
	return sysext.Extension{}
}

// TestStateUnionsBothSourcesWhenAvailable is the headline success case: both
// sources answer, and the inventory is the union of updex definitions,
// installed images, and merged extensions -- including one managed extension
// with a pending update, one managed extension with none, and one installed
// extension with no updex definition at all.
func TestStateUnionsBothSourcesWhenAvailable(t *testing.T) {
	fixture := newStateFixture(t)

	state, err := fixture.manager().State(context.Background(), true, true)

	require.NoError(t, err)
	assert.True(t, state.UpdexAvailable)
	assert.Empty(t, state.UpdexError)
	assert.True(t, state.SysextAvailable)
	assert.Empty(t, state.SysextError)
	require.Len(t, state.Extensions, 3)
	assert.Equal(t, []string{"docker", "legacy", "zinc"}, []string{state.Extensions[0].Name, state.Extensions[1].Name, state.Extensions[2].Name},
		"the union inventory is sorted by extension name")

	docker := extensionByName(t, state, "docker")
	assert.Equal(t, sysext.Extension{
		Description: "Docker engine",
		Enabled:     true,
		Installed:   true,
		Managed:     true,
		Merged:      true,
		Name:        "docker",
		Path:        "/not-present/docker_9.9_13_x86-64.raw",
		Updates:     []sysext.AvailableUpdate{{Feature: "docker", Component: "engine", Current: "1", Newest: "2"}},
		Version:     "9.9",
	}, docker, "a managed, installed, merged extension carries every field from both sources plus its pending update")

	zinc := extensionByName(t, state, "zinc")
	assert.True(t, zinc.Managed)
	assert.False(t, zinc.Enabled)
	assert.False(t, zinc.Installed)
	assert.False(t, zinc.Merged)
	assert.Empty(t, zinc.Updates, "a managed extension whose check reports nothing available has no updates")

	legacy := extensionByName(t, state, "legacy")
	assert.False(t, legacy.Managed, "an installed extension with no updex definition is unmanaged, hence read-only")
	assert.True(t, legacy.Installed)
	assert.False(t, legacy.Merged)
	assert.Empty(t, legacy.Updates, "the update check only ever reports on definitions updex itself enumerated")
	assert.Equal(t, "installed", legacy.Version)
}

// TestStateReportsEmptySuccessWhenBothSourcesAreEmpty pins the empty *success*
// state the spec separates from a gated-off host: both tools answer, both
// sources are available, and Extensions is an empty slice rather than nil.
func TestStateReportsEmptySuccessWhenBothSourcesAreEmpty(t *testing.T) {
	fixture := newStateFixture(t).empty()

	state, err := fixture.manager().State(context.Background(), true, true)

	require.NoError(t, err)
	assert.True(t, state.UpdexAvailable)
	assert.True(t, state.SysextAvailable)
	assert.Empty(t, state.UpdexError)
	assert.Empty(t, state.SysextError)
	assert.NotNil(t, state.Extensions, "an empty inventory is an empty slice, never nil")
	assert.Empty(t, state.Extensions)
}

// TestStateNeverAttemptsUpdexWhenCapabilityAbsent covers the "never attempted"
// half of the availability distinction for the updex source: nothing is
// managed, no updates are reported anywhere, and UpdexError stays empty
// because there was no failure to report -- updex was simply never run.
func TestStateNeverAttemptsUpdexWhenCapabilityAbsent(t *testing.T) {
	fixture := newStateFixture(t)

	state, err := fixture.manager().State(context.Background(), false, true)

	require.NoError(t, err)
	assert.False(t, state.UpdexAvailable)
	assert.Empty(t, state.UpdexError, "a source that was never attempted reports no error")
	assert.False(t, fixture.invoked("updex"), "updex must never be executed when its capability is absent")
	assert.True(t, state.SysextAvailable)
	require.Len(t, state.Extensions, 2, "only installed/merged extensions remain")
	for _, extension := range state.Extensions {
		assert.False(t, extension.Managed, "nothing can be managed without updex definitions: %s", extension.Name)
		assert.Empty(t, extension.Updates, "no updates can be known without updex: %s", extension.Name)
	}
	docker := extensionByName(t, state, "docker")
	assert.True(t, docker.Installed)
	assert.True(t, docker.Merged)
	assert.Equal(t, "9.9", docker.Version)
}

// TestStateNeverAttemptsSysextWhenCapabilityAbsent is the symmetric
// never-attempted case for the systemd-sysext source.
func TestStateNeverAttemptsSysextWhenCapabilityAbsent(t *testing.T) {
	fixture := newStateFixture(t)

	state, err := fixture.manager().State(context.Background(), true, false)

	require.NoError(t, err)
	assert.False(t, state.SysextAvailable)
	assert.Empty(t, state.SysextError, "a source that was never attempted reports no error")
	assert.False(t, fixture.invoked("systemd-sysext"), "systemd-sysext must never be executed when its capability is absent")
	assert.True(t, state.UpdexAvailable)
	require.Len(t, state.Extensions, 2, "only updex-managed definitions remain")
	for _, extension := range state.Extensions {
		assert.True(t, extension.Managed, "%s", extension.Name)
		assert.False(t, extension.Installed, "%s", extension.Name)
		assert.False(t, extension.Merged, "%s", extension.Name)
		assert.Empty(t, extension.Version, "%s", extension.Name)
	}
	assert.Len(t, extensionByName(t, state, "docker").Updates, 1, "update availability still comes from updex alone")
}

// TestStateDegradesUpdexSourceIndependently walks both updex sub-calls -- the
// per-directory feature list and the pending-update check -- and asserts each
// failure degrades only the updex source: UpdexAvailable false, UpdexError set
// to the failure's message, and every systemd-sysext-sourced fact still
// reported.
func TestStateDegradesUpdexSourceIndependently(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		failing func(*stateFixture) string
	}{
		{"features-list", func(f *stateFixture) string { return f.featuresListKey() }},
		{"features-check", func(f *stateFixture) string { return f.featuresCheckKey() }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateFixture(t)
			fixture.runner.errors[testCase.failing(fixture)] = errors.New("updex exploded")

			state, err := fixture.manager().State(context.Background(), true, true)

			require.NoError(t, err, "a source-level failure is never State's own error")
			assert.False(t, state.UpdexAvailable)
			assert.Contains(t, state.UpdexError, "updex exploded")
			assert.True(t, state.SysextAvailable, "the other source is untouched")
			assert.Empty(t, state.SysextError)
			require.Len(t, state.Extensions, 2, "the inventory still carries the installed extensions")
			docker := extensionByName(t, state, "docker")
			assert.True(t, docker.Installed)
			assert.True(t, docker.Merged)
			assert.Equal(t, "9.9", docker.Version)
			for _, extension := range state.Extensions {
				assert.False(t, extension.Managed, "no definition data survives a failed updex read: %s", extension.Name)
				assert.Empty(t, extension.Updates, "no update data survives a failed updex read: %s", extension.Name)
			}
		})
	}
}

// TestStateDegradesSysextSourceIndependently is the symmetric walk over the
// two systemd-sysext sub-calls (`list` behind installed() and `status` behind
// merged()): each failure degrades only that source and leaves every
// updex-sourced fact -- Managed, Enabled, Description, and Updates -- intact.
func TestStateDegradesSysextSourceIndependently(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		failing string
	}{
		{"list", "systemd-sysext list --json=short --no-pager"},
		{"status", "systemd-sysext status --json=short --no-pager"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateFixture(t)
			fixture.runner.errors[testCase.failing] = errors.New("systemd-sysext exploded")

			state, err := fixture.manager().State(context.Background(), true, true)

			require.NoError(t, err, "a source-level failure is never State's own error")
			assert.False(t, state.SysextAvailable)
			assert.Contains(t, state.SysextError, "systemd-sysext exploded")
			assert.True(t, state.UpdexAvailable, "the other source is untouched")
			assert.Empty(t, state.UpdexError)
			require.Len(t, state.Extensions, 2, "only the updex-managed definitions remain")
			docker := extensionByName(t, state, "docker")
			assert.True(t, docker.Managed)
			assert.True(t, docker.Enabled)
			assert.Equal(t, "Docker engine", docker.Description)
			assert.Equal(t, []sysext.AvailableUpdate{{Feature: "docker", Component: "engine", Current: "1", Newest: "2"}}, docker.Updates)
			assert.False(t, docker.Installed)
			assert.False(t, docker.Merged)
			assert.Empty(t, extensionByName(t, state, "zinc").Updates)
		})
	}
}
