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

func TestPageRendersStorageCards(t *testing.T) {
	state := State{
		Project: "production",
		Pools:   []StoragePool{{Name: "fast", Driver: "zfs", Status: "Created", UsedBy: 2}},
		Volumes: []StorageVolume{{Name: "data", Pool: "fast", ContentType: "filesystem", UsedBy: 1}},
		Buckets: []StorageBucket{{Name: "assets", Pool: "fast", S3URL: "https://s3.example/assets"}},
	}
	var output strings.Builder
	require.NoError(t, Page(state, "token", false).Render(context.Background(), &output))
	for _, value := range []string{"Storage pools", "Storage volumes", "Storage buckets", "fast", "data", "assets", "https://s3.example/assets"} {
		assert.Contains(t, output.String(), value)
	}
}

func TestPageRendersStorageEmptyStates(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Page(State{Project: "default"}, "token", false).Render(context.Background(), &output))
	for _, value := range []string{"No storage pools were found.", "No custom storage volumes were found in the default project.", "No storage buckets were found."} {
		assert.Contains(t, output.String(), value)
	}
}
