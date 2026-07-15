package sysext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	return r.outputs[strings.Join(call, " ")], nil
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
