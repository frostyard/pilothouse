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
