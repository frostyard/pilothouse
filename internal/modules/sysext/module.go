package sysext

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct {
	manager Manager
}

func New(manager Manager) *Module {
	return &Module{manager: manager}
}

func (m *Module) Dashboard(ctx context.Context, _ platform.Host) ([]platform.DashboardCard, error) {
	features, err := m.manager.List(ctx)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{
		Component: Summary(features),
		Order:     31,
		Span:      platform.SpanHalf,
	}}, nil
}

func (m *Module) Manifest() platform.Manifest {
	return platform.Manifest{
		Description: "Install, remove, update, and refresh Snosi system extensions",
		Icon:        "sysext",
		ID:          "sysext",
		Name:        "Extensions",
		Order:       20,
		Path:        "/sysext",
	}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /sysext", func(w http.ResponseWriter, r *http.Request) {
		features, err := m.manager.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = host.Render(w, r, platform.Page{
			Active:  "sysext",
			Body:    Page(features, host.CSRFToken(r), host.Identity(r).Admin),
			Eyebrow: "Immutable add-ons",
			Title:   "System extensions",
		})
	})
	mux.HandleFunc("POST /sysext/{name}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		name := r.PathValue("name")
		action := r.PathValue("action")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		var err error
		switch action {
		case "disable":
			if !host.ConfirmAction(w, r, "Disable system extension "+name, "sysext/feature/"+name) {
				return
			}
			err = host.Execute(ctx, r, broker.ActionSysextDisable, map[string]string{"name": name})
		case "enable":
			err = host.Execute(ctx, r, broker.ActionSysextEnable, map[string]string{"name": name})
		default:
			http.NotFound(w, r)
			return
		}
		m.redirect(w, r, fmt.Sprintf("%s %sd", name, action), err)
	})
	mux.HandleFunc("POST /sysext/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.ValidateAction(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
		defer cancel()
		action := r.PathValue("action")
		var err error
		switch action {
		case "refresh":
			if !host.ConfirmAction(w, r, "Refresh system extensions", "sysext/global") {
				return
			}
			err = host.Execute(ctx, r, broker.ActionSysextRefresh, nil)
		case "update":
			if !host.ConfirmAction(w, r, "Update system extensions", "sysext/global") {
				return
			}
			err = host.Execute(ctx, r, broker.ActionSysextUpdate, nil)
		default:
			http.NotFound(w, r)
			return
		}
		m.redirect(w, r, fmt.Sprintf("Extensions %sd", action), err)
	})
}

func (m *Module) redirect(w http.ResponseWriter, r *http.Request, success string, err error) {
	values := url.Values{}
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", "Action failed. Review Activity for the recorded outcome.")
	} else {
		values.Set("notice", success)
	}
	destination := "/sysext?" + values.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
