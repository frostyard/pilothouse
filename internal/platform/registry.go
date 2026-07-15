package platform

import (
	"fmt"
	"slices"
)

type Registry struct {
	modules []Module
}

func NewRegistry(modules ...Module) (*Registry, error) {
	r := &Registry{}
	for _, module := range modules {
		if err := r.Register(module); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Manifests() []Manifest {
	manifests := make([]Manifest, 0, len(r.modules))
	for _, module := range r.modules {
		manifests = append(manifests, module.Manifest())
	}
	return manifests
}

func (r *Registry) Modules() []Module {
	return slices.Clone(r.modules)
}

func (r *Registry) Register(module Module) error {
	manifest := module.Manifest()
	if manifest.ID == "" || manifest.Name == "" || manifest.Path == "" {
		return fmt.Errorf("module manifest requires id, name, and path")
	}
	for _, registered := range r.modules {
		if registered.Manifest().ID == manifest.ID {
			return fmt.Errorf("module %q already registered", manifest.ID)
		}
		if registered.Manifest().Path == manifest.Path {
			return fmt.Errorf("module path %q already registered", manifest.Path)
		}
	}
	r.modules = append(r.modules, module)
	slices.SortStableFunc(r.modules, func(a, b Module) int {
		if a.Manifest().Order != b.Manifest().Order {
			return a.Manifest().Order - b.Manifest().Order
		}
		if a.Manifest().Name < b.Manifest().Name {
			return -1
		}
		if a.Manifest().Name > b.Manifest().Name {
			return 1
		}
		return 0
	})
	return nil
}
