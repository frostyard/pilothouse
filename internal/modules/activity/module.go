package activity

import (
	"context"
	"net/http"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(context.Context, platform.Host) ([]platform.DashboardCard, error) {
	return nil, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "activity", Name: "Activity", Description: "Privileged action history", Icon: "activity", Order: 90, Path: "/activity"}
}

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /activity", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			_ = host.Render(w, r, platform.Page{Active: "activity", Body: AccessDenied(), Eyebrow: "Action audit", Title: "Activity"})
			return
		}
		parameters := map[string]string{"limit": "100"}
		if outcome := r.URL.Query().Get("outcome"); outcome != "" {
			parameters["outcome"] = outcome
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		var records []audit.Record
		if err := host.Query(ctx, broker.QueryActivity, parameters, &records); err != nil {
			http.Error(w, "Activity history is unavailable.", http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{Active: "activity", Body: Page(records, parameters["outcome"]), Eyebrow: "Action audit", Title: "Activity"})
	})
}
