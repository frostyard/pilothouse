package platform

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/frostyard/pilothouse/internal/auth"
)

type DashboardCard struct {
	Component templ.Component
	Order     int
	Span      Span
}

type Host interface {
	CSRFToken(*http.Request) string
	Execute(context.Context, *http.Request, string, map[string]string) error
	Identity(*http.Request) auth.Identity
	Query(context.Context, string, map[string]string, any) error
	Render(http.ResponseWriter, *http.Request, Page) error
	ValidateAction(http.ResponseWriter, *http.Request) bool
}

type Manifest struct {
	Description string
	Icon        string
	ID          string
	Name        string
	Order       int
	Path        string
}

type Module interface {
	Dashboard(context.Context, Host) ([]DashboardCard, error)
	Manifest() Manifest
	Mount(*http.ServeMux, Host)
}

type Page struct {
	Active  string
	Body    templ.Component
	Eyebrow string
	Title   string
}

type Span string

const (
	SpanFull  Span = "full"
	SpanHalf  Span = "half"
	SpanThird Span = "third"
)
