package maintenance

import (
	"bytes"
	"context"
	"testing"

	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageRendersMaintenanceStateAndComponents(t *testing.T) {
	state := State{OSVersion: "Snosi", RebootRequired: true, RebootReasons: []string{"Update requires reboot"}, Updates: []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}}, Jobs: []Job{{ID: 1, Status: jobs.StatusSucceeded}}}
	var output bytes.Buffer
	require.NoError(t, Page(state, "csrf", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "Update requires reboot")
	assert.Contains(t, html, "Reboot host")
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}
