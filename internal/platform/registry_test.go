package platform

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubModule struct {
	manifest Manifest
}

func (m stubModule) Dashboard(context.Context, Host) ([]DashboardCard, error) { return nil, nil }
func (m stubModule) Manifest() Manifest                                       { return m.manifest }
func (m stubModule) Mount(*http.ServeMux, Host)                               {}

func TestRegistrySortsAndRejectsConflicts(t *testing.T) {
	a := assert.New(t)
	r := require.New(t)

	registry, err := NewRegistry(
		stubModule{manifest: Manifest{ID: "later", Name: "Later", Order: 20, Path: "/later"}},
		stubModule{manifest: Manifest{ID: "first", Name: "First", Order: 10, Path: "/first"}},
	)
	r.NoError(err)
	a.Equal([]string{"first", "later"}, []string{registry.Manifests()[0].ID, registry.Manifests()[1].ID})

	err = registry.Register(stubModule{manifest: Manifest{ID: "first", Name: "Again", Path: "/again"}})
	a.EqualError(err, `module "first" already registered`)

	err = registry.Register(stubModule{manifest: Manifest{ID: "again", Name: "Again", Path: "/first"}})
	a.EqualError(err, `module path "/first" already registered`)
}

func TestRegistryValidatesRequiredMetadata(t *testing.T) {
	_, err := NewRegistry(stubModule{manifest: Manifest{Name: "Missing", Path: "/missing"}})
	require.EqualError(t, err, "module manifest requires id, name, and path")
}
