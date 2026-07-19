package sysext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRunner struct {
	calls   [][]string
	errors  map[string]error
	outputs map[string][]byte
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	key := strings.Join(call, " ")
	return r.outputs[key], r.errors[key]
}

func TestParseUpdexCheckAcceptsMessageAndArrayStream(t *testing.T) {
	output := []byte(`{"type":"message","message":"checking"}
[{"feature":"docker","results":[{"component":"rootfs","current_version":"1","newest_version":"2","update_available":true}]}]`)

	updates, err := parseUpdexCheck(output)

	require.NoError(t, err)
	assert.Equal(t, []AvailableUpdate{{Feature: "docker", Component: "rootfs", Current: "1", Newest: "2"}}, updates)
}

func TestParseUpdexCheckReturnsOnlyAvailableEntries(t *testing.T) {
	output := []byte(`[{"feature":"docker","results":[
		{"component":"cli","current_version":"2","newest_version":"2","update_available":false},
		{"component":"engine","current_version":"1","newest_version":"2","update_available":true}
	]}]`)

	updates, err := parseUpdexCheck(output)

	require.NoError(t, err)
	assert.Equal(t, []AvailableUpdate{{Feature: "docker", Component: "engine", Current: "1", Newest: "2"}}, updates)
}

func TestParseUpdexCheckRejectsMalformedOrMissingArray(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		_, err := parseUpdexCheck([]byte(`{"type":"message"}
[`))
		require.Error(t, err)
	})
	t.Run("missing", func(t *testing.T) {
		_, err := parseUpdexCheck([]byte(`{"type":"message","message":"done"}`))
		require.ErrorContains(t, err, "update check array missing")
	})
}

func TestSystemManagerCheckCombinesDirectoriesAndSortsUpdates(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "sysupdate.d")
	component := filepath.Join(root, "sysupdate.docker.d")
	require.NoError(t, os.MkdirAll(shared, 0o755))
	require.NoError(t, os.MkdirAll(component, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(shared, "dev.feature"), []byte("[Feature]"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(component, "docker.feature"), []byte("[Feature]"), 0o644))
	runner := &fakeRunner{outputs: map[string][]byte{
		strings.Join([]string{"updex", "-C", shared, "--json", "features", "check"}, " "):    []byte(`[{"feature":"zinc","results":[{"component":"base","current_version":"1","newest_version":"2","update_available":true}]}]`),
		strings.Join([]string{"updex", "-C", component, "--json", "features", "check"}, " "): []byte(`[{"feature":"docker","results":[{"component":"engine","current_version":"3","newest_version":"4","update_available":true},{"component":"cli","current_version":"4","newest_version":"4","update_available":false}]}]`),
	}}

	updates, err := NewSystemManager(runner, root, "updex").Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []AvailableUpdate{
		{Feature: "docker", Component: "engine", Current: "3", Newest: "4"},
		{Feature: "zinc", Component: "base", Current: "1", Newest: "2"},
	}, updates)
	assert.Equal(t, [][]string{
		{"updex", "-C", shared, "--json", "features", "check"},
		{"updex", "-C", component, "--json", "features", "check"},
	}, runner.calls)
}

func TestSystemManagerCheckReturnsCommandError(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "sysupdate.d")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "dev.feature"), []byte("[Feature]"), 0o644))
	key := strings.Join([]string{"updex", "-C", directory, "--json", "features", "check"}, " ")
	commandErr := errors.New("check failed")
	runner := &fakeRunner{errors: map[string]error{key: commandErr}}

	_, err := NewSystemManager(runner, root, "updex").Check(context.Background())

	require.ErrorIs(t, err, commandErr)
}

func TestParseUpdexFeaturesAcceptsMessageStream(t *testing.T) {
	output := []byte("{\"type\":\"message\",\"message\":\"loading\"}\n[{\"name\":\"docker\",\"description\":\"Docker\",\"enabled\":true}]")
	features, err := parseUpdexFeatures(output)
	require.NoError(t, err)
	require.Len(t, features, 1)
	assert.Equal(t, "docker", features[0].Name)
	assert.True(t, features[0].Enabled)
}

func TestSystemManagerDiscoversSharedAndComponentDefinitions(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sysupdate.d"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sysupdate.docker.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sysupdate.d", "dev.feature"), []byte("[Feature]"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sysupdate.docker.d", "docker.feature"), []byte("[Feature]"), 0o644))

	manager := NewSystemManager(&fakeRunner{}, root, "updex")
	directories, err := manager.definitionDirectories()
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(root, "sysupdate.d"), filepath.Join(root, "sysupdate.docker.d")}, directories)
}

func TestExtensionVersion(t *testing.T) {
	assert.Equal(t, "5+29.6.1-1~debian.13~trixie", extensionVersion("docker", "/var/lib/extensions.d/docker_5+29.6.1-1~debian.13~trixie_13_x86-64.raw"))
	assert.Equal(t, "installed", extensionVersion("docker", "/not-present/docker.raw"))
}

func TestActiveFirst(t *testing.T) {
	features := []Feature{{Name: "available"}, {Enabled: true, Name: "enabled"}, {Merged: true, Name: "merged"}}
	sorted := activeFirst(features)
	assert.Equal(t, []string{"merged", "enabled", "available"}, []string{sorted[0].Name, sorted[1].Name, sorted[2].Name})
}

func TestDisableForcesRemovalOfMergedExtension(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "sysupdate.d")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "docker.feature"), []byte("[Feature]"), 0o644))
	runner := &fakeRunner{outputs: map[string][]byte{
		"systemd-sysext list --json=short --no-pager":                                       []byte(`[{"name":"docker","path":"/not-present/docker.raw"}]`),
		"systemd-sysext status --json=short --no-pager":                                     []byte(`[{"hierarchy":"/usr","extensions":["docker"]}]`),
		strings.Join([]string{"updex", "-C", directory, "--json", "features", "list"}, " "): []byte(`[{"name":"docker","description":"Docker","enabled":true}]`),
	}}
	manager := NewSystemManager(runner, root, "updex")

	require.NoError(t, manager.Disable(context.Background(), "docker"))
	assert.Equal(t, []string{"updex", "-C", directory, "--json", "features", "disable", "docker", "--now", "--force"}, runner.calls[len(runner.calls)-1])
}
