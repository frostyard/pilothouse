package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHost struct {
	page            platform.Page
	queryDeadline   time.Time
	queryErr        error
	queryID         string
	queryParameters map[string]string
	snapshot        Snapshot
}

func (*fakeHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }
func (*fakeHost) CSRFToken(*http.Request) string                                        { return "" }
func (*fakeHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (*fakeHost) Identity(*http.Request) auth.Identity { return auth.Identity{} }
func (host *fakeHost) Query(ctx context.Context, queryID string, parameters map[string]string, target any) error {
	host.queryID = queryID
	host.queryParameters = parameters
	host.queryDeadline, _ = ctx.Deadline()
	if host.queryErr != nil {
		return host.queryErr
	}
	*target.(*Snapshot) = host.snapshot
	return nil
}
func (host *fakeHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	host.page = page
	return nil
}
func (*fakeHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }

func TestModuleUsesOnlyStorageQuery(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Summary: Summary{ActiveMounts: 3}}}
	module := New()
	cards, err := module.Dashboard(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, broker.QueryStorageState, host.queryID)
	assert.Nil(t, host.queryParameters)
	assert.Equal(t, platform.SpanHalf, cards[0].Span)
}

func TestHealthMapsStorageSeverity(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Findings: []Finding{{ResourceID: "disk:abc", Severity: HealthCritical, Title: "Disk health failed", Detail: "Media errors reported"}}}}
	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, platform.SeverityCritical, findings[0].Severity)
	assert.Equal(t, "/storage#disk-abc", findings[0].Path)
}

func TestHealthMapsAllStorageSeverities(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Findings: []Finding{
		{ResourceID: "healthy", Severity: HealthHealthy},
		{ResourceID: "warning", Severity: HealthWarning},
		{ResourceID: "unknown", Severity: HealthUnknown},
		{ResourceID: "unrecognized", Severity: Health("unexpected")},
		{ResourceID: "empty", Severity: ""},
	}}}
	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 5)
	assert.Equal(t, []platform.Severity{platform.SeverityInfo, platform.SeverityWarning, platform.SeverityUnknown, platform.SeverityUnknown, platform.SeverityUnknown}, []platform.Severity{findings[0].Severity, findings[1].Severity, findings[2].Severity, findings[3].Severity, findings[4].Severity})
}

func TestHealthFindingPathHasExactlyOnePageAnchor(t *testing.T) {
	snapshot := Snapshot{
		Resources: []Resource{{ID: "disk:abc"}},
		Findings:  []Finding{{ResourceID: "disk:abc", Severity: HealthCritical}},
	}
	findings, err := New().Health(context.Background(), &fakeHost{snapshot: snapshot})
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "/storage#disk-abc", findings[0].Path)

	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))
	assert.Equal(t, 1, strings.Count(output.String(), `id="disk-abc"`))
}

func TestManifest(t *testing.T) {
	assert.Equal(t, platform.Manifest{ID: "storage", Name: "Storage", Path: "/storage", Icon: "disk", Order: 25}, New().Manifest())
}

func TestStoragePageUsesFixedQueryAndDeadline(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Summary: Summary{ActiveMounts: 1}}}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	started := time.Now()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/storage", nil))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryStorageState, host.queryID)
	assert.Nil(t, host.queryParameters)
	assert.Equal(t, "storage", host.page.Active)
	assert.Equal(t, "Storage", host.page.Title)
	require.False(t, host.queryDeadline.IsZero())
	assert.WithinDuration(t, started.Add(12*time.Second), host.queryDeadline, time.Second)
}

func TestStoragePageRendersUnavailableState(t *testing.T) {
	host := &fakeHost{queryErr: errors.New("broker connection refused")}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/storage", nil))

	require.Equal(t, http.StatusOK, response.Code)
	var output strings.Builder
	require.NoError(t, host.page.Body.Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Storage status is unavailable.")
	assert.NotContains(t, output.String(), "broker connection refused")
}
