package fleet

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFleetScreensRenderPreviewState(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, Page(New().systems).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Interface preview")
	assert.Contains(t, output.String(), "/fleet/systems/cayo-01")
	assert.Contains(t, output.String(), "connected-label")
	assert.Contains(t, output.String(), "1 updates")
	assert.Contains(t, output.String(), "1 findings")
	assert.NotContains(t, output.String(), "@web.")
	output.Reset()
	require.NoError(t, SystemPage(New().systems[1]).Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Remote actions")
	assert.Contains(t, output.String(), "disabled")
	assert.NotContains(t, output.String(), "@web.")
	output.Reset()
	require.NoError(t, Enroll().Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Enrollment is not active")
	assert.Contains(t, output.String(), "Remote shell and generic filesystem access remain outside the protocol")
	assert.NotContains(t, output.String(), "@web.")
}
