package logs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type logsHost struct {
	admin      bool
	queryID    string
	parameters map[string]string
	queryErr   error
	queryCalls int
	page       platform.Page
}

func (*logsHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }
func (*logsHost) CSRFToken(*http.Request) string                                        { return "" }
func (*logsHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (h *logsHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: h.admin} }
func (h *logsHost) Query(_ context.Context, id string, parameters map[string]string, target any) error {
	h.queryCalls++
	h.queryID, h.parameters = id, parameters
	if h.queryErr != nil {
		return h.queryErr
	}
	state := target.(*State)
	state.Filters = Filters{
		Query: parameters["query"], Priority: parameters["priority"],
		Unit: parameters["unit"], Window: parameters["window"],
	}
	state.Units = []string{"sshd.service"}
	return nil
}
func (h *logsHost) Render(w http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
	return page.Body.Render(context.Background(), w)
}
func (*logsHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }
func (*logsHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (*logsHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (*logsHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

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

func TestNormalizeHTTPFiltersStripsNUL(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{Query: "panic\x00now"})

	assert.Equal(t, "panicnow", filters.Query)
}

func TestNormalizeHTTPFiltersKeepsValidValues(t *testing.T) {
	filters := normalizeHTTPFilters(Filters{
		Query: "query", Priority: "warning", Unit: "sshd.service", Window: "6h",
	})

	assert.Equal(t, Filters{Query: "query", Priority: "warning", Unit: "sshd.service", Window: "6h"}, filters)
}

func TestLogsPageRequiresAdministratorWithoutBrokerCall(t *testing.T) {
	host := &logsHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

	assert.Zero(t, host.queryCalls)
	assert.Equal(t, "logs", host.page.Active)
	assert.Equal(t, "Logs", host.page.Title)
	assert.Equal(t, "system journal", host.page.Eyebrow)
	assert.Contains(t, response.Body.String(), "Administrator access required")
	assert.Contains(t, response.Body.String(), "<svg")
	assert.NotContains(t, response.Body.String(), "@web.")
}

func TestLogsPageDispatchesOnlyFixedQueryWithExactFilters(t *testing.T) {
	host := &logsHost{admin: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs?query=panic+now&priority=warning&unit=sshd.service&window=6h", nil))

	assert.Equal(t, broker.QueryLogs, host.queryID)
	assert.Equal(t, map[string]string{"query": "panic now", "priority": "warning", "unit": "sshd.service", "window": "6h"}, host.parameters)
	assert.Equal(t, "logs", host.page.Active)
	assert.Equal(t, "Logs", host.page.Title)
}

func TestLogsPageRendersUnavailableStateWithoutReaderErrorDisclosure(t *testing.T) {
	host := &logsHost{admin: true, queryErr: errors.New("secret backend detail")}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "System journal entries are unavailable. Retrying automatically.")
	assert.Contains(t, response.Body.String(), `hx-trigger="every 5s"`)
	assert.NotContains(t, response.Body.String(), "secret backend detail")
}
