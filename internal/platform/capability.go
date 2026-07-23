package platform

import (
	"net/http"

	"github.com/frostyard/pilothouse/internal/capability"
)

// CapabilityGate is implemented by a Module that requires one or more host
// capabilities to be present for its whole surface (navigation, dashboard
// cards, routes) to be available. A Module that does not implement
// CapabilityGate has no capability requirement and is always available —
// this is the spec's default for modules like system, files, activity,
// fleet, and storage's own inventory reads.
type CapabilityGate interface {
	// RequiredCapabilities returns the capability IDs that must all be
	// present (HasAll semantics) for the module to be available. An empty
	// or nil result means the module is always available, the same as a
	// Module that does not implement CapabilityGate at all.
	RequiredCapabilities() []capability.ID
}

// Gate returns an http.HandlerFunc that checks host's currently advertised
// capability.Set against ids and either delegates to next (when every id in
// ids is present, including the zero-ids case) or responds 404 (when at
// least one id is missing). This is what lets a module's routes stay
// mounted on the shared mux while a request against a capability the host
// lacks 404s at request time, rather than the route being conditionally
// registered at startup.
func Gate(host Host, ids []capability.ID, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !host.Capabilities(r.Context()).HasAll(ids...) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// Available reports whether module is available given caps: a Module
// implementing CapabilityGate is available only when caps has every one of
// its RequiredCapabilities (HasAll semantics); a Module that does not
// implement CapabilityGate has no capability requirement and is always
// available. This is the same test Gate applies to a single request,
// applied instead to a whole registry's module list so navigation and
// dashboard rendering can filter out modules the host currently lacks the
// capabilities for.
func Available(module Module, caps capability.Set) bool {
	gate, ok := module.(CapabilityGate)
	if !ok {
		return true
	}
	return caps.HasAll(gate.RequiredCapabilities()...)
}
