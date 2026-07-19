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

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityInfo     Severity = "info"
	SeverityUnknown  Severity = "unknown"
	SeverityWarning  Severity = "warning"
)

type Finding struct {
	Detail   string
	ID       string
	Path     string
	Severity Severity
	Source   string
	Title    string
}

type Host interface {
	ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool
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

// HealthProvider is optional so modules can expose operational findings without
// expanding the core module contract.
type HealthProvider interface {
	Health(context.Context, Host) ([]Finding, error)
	Manifest() Manifest
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
