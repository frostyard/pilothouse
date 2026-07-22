package files

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/frostyard/pilothouse/internal/platform"
)

const (
	filesFilterMaxRunes = 200
	filesFilterMaxBytes = 1024
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(context.Context, platform.Host) ([]platform.DashboardCard, error) {
	return nil, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{
		ID: "files", Name: "Files", Description: "Browse and transfer configured host files",
		Icon: "disk", Order: 38, Path: "/files",
	}
}

// Mount is intentionally empty until the Files HTTP handlers are introduced.
func (*Module) Mount(*http.ServeMux, platform.Host) {}

func normalizeHTTPFilters(filters ListRequest) ListRequest {
	filters.Filter = truncateFilter(strings.ReplaceAll(strings.TrimSpace(filters.Filter), "\x00", ""))
	if !validFilesSort(filters.Sort) {
		filters.Sort = "name"
	}
	if filters.Direction != "asc" && filters.Direction != "desc" {
		filters.Direction = "asc"
	}
	return filters
}

func truncateFilter(filter string) string {
	var truncated strings.Builder
	truncated.Grow(min(len(filter), filesFilterMaxBytes))
	runes, bytes := 0, 0
	for _, value := range filter {
		valueBytes := utf8.RuneLen(value)
		if runes == filesFilterMaxRunes || bytes+valueBytes > filesFilterMaxBytes {
			break
		}
		truncated.WriteRune(value)
		runes++
		bytes += valueBytes
	}
	return truncated.String()
}

func validFilesSort(sort string) bool {
	return sort == "name" || sort == "size" || sort == "modified" || sort == "owner" || sort == "permissions"
}

func filesURL(root, path string, filters ListRequest) templ.SafeURL {
	filters = normalizeHTTPFilters(filters)
	values := url.Values{
		"direction": {filters.Direction},
		"filter":    {filters.Filter},
		"hidden":    {boolString(filters.Hidden)},
		"path":      {path},
		"sort":      {filters.Sort},
	}
	return templ.SafeURL("/files/" + url.PathEscape(root) + "?" + values.Encode())
}

func downloadURL(root, path string) templ.SafeURL {
	values := url.Values{"path": {path}}
	return templ.SafeURL("/files/" + url.PathEscape(root) + "/download?" + values.Encode())
}

func uploadURL(root, path string) templ.SafeURL {
	values := url.Values{"path": {path}}
	return templ.SafeURL("/files/" + url.PathEscape(root) + "/upload?" + values.Encode())
}

func childPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "/" + name
}

func breadcrumbPaths(path string) []breadcrumb {
	if path == "" {
		return nil
	}
	segments := strings.Split(path, "/")
	crumbs := make([]breadcrumb, 0, len(segments))
	for index, segment := range segments {
		crumbs = append(crumbs, breadcrumb{Name: segment, Path: strings.Join(segments[:index+1], "/")})
	}
	return crumbs
}

func nextSort(filters ListRequest, sort string) ListRequest {
	filters = normalizeHTTPFilters(filters)
	if filters.Sort == sort && filters.Direction == "asc" {
		filters.Direction = "desc"
	} else {
		filters.Sort = sort
		filters.Direction = "asc"
	}
	return filters
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

type breadcrumb struct {
	Name string
	Path string
}
