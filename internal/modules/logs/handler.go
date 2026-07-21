package logs

import (
	"context"
	"net/http"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			_ = host.Render(w, r, platform.Page{Body: AccessDenied()})
			return
		}

		filters := normalizeHTTPFilters(Filters{
			Query:    r.URL.Query().Get("query"),
			Priority: r.URL.Query().Get("priority"),
			Unit:     r.URL.Query().Get("unit"),
			Window:   r.URL.Query().Get("window"),
		})
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		parameters := map[string]string{
			"query": filters.Query, "priority": filters.Priority,
			"unit": filters.Unit, "window": filters.Window,
		}
		var state State
		unavailable := host.Query(ctx, broker.QueryLogs, parameters, &state) != nil
		if unavailable {
			state = State{Filters: filters, Entries: []Entry{}, Units: []string{}}
		} else {
			state.Filters = filters
		}
		_ = host.Render(w, r, platform.Page{
			Active: "logs", Body: Page(state, unavailable), Eyebrow: "system journal", Title: "Logs",
		})
	})
}
