package web

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardDoesNotBoostLinksInsidePollingRegion(t *testing.T) {
	var output strings.Builder
	require.NoError(t, Dashboard(nil).Render(context.Background(), &output))
	assert.Contains(t, output.String(), `hx-boost="false"`)
	assert.Contains(t, output.String(), `hx-select="#dashboard"`)
}
