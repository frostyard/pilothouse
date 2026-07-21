package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testHost struct {
	action     string
	parameters map[string]string
	query      string
	page       platform.Page
}

func (*testHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }

func (*testHost) CSRFToken(*http.Request) string { return "token" }
func (h *testHost) Execute(_ context.Context, _ *http.Request, action string, parameters map[string]string) error {
	h.action, h.parameters = action, parameters
	return nil
}
func (*testHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: true} }
func (h *testHost) Query(_ context.Context, query string, parameters map[string]string, result any) error {
	h.query, h.parameters = query, parameters
	if journal, ok := result.(*Journal); ok {
		*journal = Journal{Unit: parameters["unit"], Description: "Backup"}
	}
	return nil
}
func (h *testHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
	return nil
}
func (*testHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }

func TestUnitActionDispatch(t *testing.T) {
	host := &testHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/services/backup.timer/restart", nil))
	assert.Equal(t, broker.ActionServicesRestart, host.action)
	assert.Equal(t, map[string]string{"unit": "backup.timer"}, host.parameters)
	destination, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "backup.timer restarted", destination.Query().Get("notice"))

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/services/backup.scope/start", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestUnitActionRedirectPreservesFilters(t *testing.T) {
	host := &testHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	form := url.Values{
		"query":     {"backup job"},
		"status":    {"failed"},
		"type":      {"timer"},
		"unit-file": {"enabled"},
	}
	request := httptest.NewRequest(http.MethodPost, "/services/backup.timer/reset-failed", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	require.Equal(t, http.StatusSeeOther, response.Code)
	destination, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "backup job", destination.Query().Get("query"))
	assert.Equal(t, "failed", destination.Query().Get("status"))
	assert.Equal(t, "timer", destination.Query().Get("type"))
	assert.Equal(t, "enabled", destination.Query().Get("unit-file"))
	assert.Equal(t, "backup.timer failure reset", destination.Query().Get("notice"))
}

func TestLogsQueryUsesFixedBrokerQueryAndUnitParameter(t *testing.T) {
	host := &testHost{}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/services/backup.timer/logs", nil))
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryServicesJournal, host.query)
	assert.Equal(t, map[string]string{"unit": "backup.timer"}, host.parameters)
	assert.Equal(t, "backup.timer logs", host.page.Title)
}

func TestFilterStateCombinesServiceFilters(t *testing.T) {
	state := State{
		Summary: Summary{Total: 4, Active: 2, Failed: 1},
		Units: []Unit{
			{Name: "backup.timer", Description: "Nightly archive", ActiveState: "active", UnitFileState: "enabled"},
			{Name: "dbus.socket", Description: "Message bus", ActiveState: "active", UnitFileState: "static"},
			{Name: "broken.service", Description: "Broken worker", ActiveState: "failed", UnitFileState: "disabled"},
			{Name: "idle.service", Description: "Idle worker", ActiveState: "inactive", UnitFileState: "disabled"},
		},
	}

	tests := []struct {
		name    string
		filters Filters
		want    []string
	}{
		{name: "status", filters: Filters{Status: "failed"}, want: []string{"broken.service"}},
		{name: "type", filters: Filters{Type: "socket"}, want: []string{"dbus.socket"}},
		{name: "unit file", filters: Filters{UnitFileState: "disabled"}, want: []string{"broken.service", "idle.service"}},
		{name: "case insensitive description", filters: Filters{Query: "ARCHIVE"}, want: []string{"backup.timer"}},
		{name: "combined", filters: Filters{Query: "worker", Status: "inactive", Type: "service", UnitFileState: "disabled"}, want: []string{"idle.service"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filtered := filterState(state, test.filters)
			names := make([]string, 0, len(filtered.Units))
			for _, unit := range filtered.Units {
				names = append(names, unit.Name)
			}
			assert.Equal(t, test.want, names)
			assert.Equal(t, state.Summary, filtered.Summary)
		})
	}
}

func TestFilterOptionsIncludeStandardAndObservedStates(t *testing.T) {
	state := State{Units: []Unit{
		{ActiveState: "active", UnitFileState: "enabled"},
		{ActiveState: "activating", UnitFileState: "static"},
		{ActiveState: "maintenance", UnitFileState: "enabled"},
	}}
	options := filterOptions(state)
	assert.Equal(t, []string{"active", "inactive", "failed", "activating", "maintenance"}, options.Statuses)
	assert.Equal(t, []string{"enabled", "static"}, options.UnitFileStates)

	normalized := normalizeFilters(Filters{Status: "invented", Type: "scope", UnitFileState: "masked"}, options)
	assert.Equal(t, Filters{}, normalized)
}
