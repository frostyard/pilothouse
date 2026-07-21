package logs

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestAndDashboard(t *testing.T) {
	module := New()
	assert.Equal(t, platform.Manifest{
		ID: "logs", Name: "Logs", Description: "Inspect the systemd journal",
		Icon: "activity", Order: 37, Path: "/logs",
	}, module.Manifest())
	cards, err := module.Dashboard(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, cards)
	assert.Equal(t, "org.frostyard.pilothouse.logs.list", broker.QueryLogs)
}

func TestNormalizeHTTPFiltersAndPollURL(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{
		Query: strings.Repeat("界", 201), Priority: "verbose",
		Unit: "../bad.service", Window: "7d",
	})
	assert.LessOrEqual(t, utf8.RuneCountInString(filters.Query), queryMaxRunes)
	assert.LessOrEqual(t, len(filters.Query), queryMaxBytes)
	assert.Equal(t, "", filters.Priority)
	assert.Equal(t, "", filters.Unit)
	assert.Equal(t, "1h", filters.Window)
	assert.Equal(t,
		"/logs?priority=&query="+url.QueryEscape(filters.Query)+"&unit=&window=1h",
		string(pollURL(filters)),
	)
}

func TestNormalizeHTTPFiltersASCIIQueryHitsRuneCapFirst(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{Query: strings.Repeat("x", 1_100)})

	assert.Len(t, filters.Query, queryMaxRunes)
	assert.LessOrEqual(t, len(filters.Query), queryMaxBytes)
}

func TestNormalizeHTTPFiltersKeepsValidValues(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{
		Query: "query", Priority: "warning", Unit: "sshd.service", Window: "6h",
	})

	assert.Equal(t, Filters{Query: "query", Priority: "warning", Unit: "sshd.service", Window: "6h"}, filters)
}
