package fleet

import (
	"context"
	"net/http"

	"github.com/frostyard/pilothouse/internal/platform"
)

type System struct {
	Attention int
	Connected bool
	ID        string
	Image     string
	Kernel    string
	Role      string
	State     string
	Updates   int
	Uptime    string
}

type Module struct {
	systems []System
}

func New() *Module {
	return &Module{systems: []System{
		{ID: "local", Image: "cayo 2026.07", Role: "This system", Kernel: "Current host", Uptime: "Connected", State: "healthy", Connected: true},
		{ID: "cayo-01", Image: "cayo 2026.07", Role: "Server", Kernel: "6.12.30", Uptime: "84 days", Updates: 1, Attention: 1, State: "attention"},
		{ID: "workstation-01", Image: "snow 2026.07", Role: "Desktop", Kernel: "6.15.4-bpo", Uptime: "11 days", Updates: 2, Attention: 3, State: "healthy"},
		{ID: "surface-go", Image: "snowfield 2026.07", Role: "Surface", Kernel: "6.15.4-surface", Uptime: "3 days", State: "healthy"},
	}}
}

func (*Module) Dashboard(context.Context, platform.Host) ([]platform.DashboardCard, error) {
	return nil, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "fleet", Name: "Fleet", Description: "View and manage connected Pilothouse systems", Icon: "server", Order: 1, Path: "/fleet"}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /fleet", func(w http.ResponseWriter, r *http.Request) {
		_ = host.Render(w, r, platform.Page{Active: "fleet", Body: Page(m.systems), Eyebrow: "Fleet preview", Title: "Systems"})
	})
	mux.HandleFunc("GET /fleet/enroll", func(w http.ResponseWriter, r *http.Request) {
		_ = host.Render(w, r, platform.Page{Active: "fleet", Body: Enroll(), Eyebrow: "Fleet preview", Title: "Connect a system"})
	})
	mux.HandleFunc("GET /fleet/systems/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, system := range m.systems {
			if system.ID == r.PathValue("id") {
				_ = host.Render(w, r, platform.Page{Active: "fleet", Body: SystemPage(system), Eyebrow: "Fleet preview · remote system", Title: system.ID})
				return
			}
		}
		http.NotFound(w, r)
	})
}
