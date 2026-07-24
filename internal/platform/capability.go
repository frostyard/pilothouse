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

// CapabilityGateAny is implemented by a Module that requires at least one of
// several host capabilities to be present for its whole surface (navigation,
// dashboard cards, routes) to be available — the "any-of" sibling of
// CapabilityGate's "all-of" contract. A Module that does not implement
// CapabilityGateAny has no any-of capability requirement and is always
// available as far as this interface is concerned, the same default
// convention as CapabilityGate. The two interfaces are deliberately kept
// separate rather than folding an any-of flag into CapabilityGate: no module
// needs both AND and OR semantics on its whole-module gate simultaneously,
// and separate interfaces avoid ambiguity about which test applies when a
// module is inspected. Implementing one does not imply or satisfy the other.
type CapabilityGateAny interface {
	// RequiredAnyCapabilities returns the capability IDs of which at least
	// one must be present (HasAny semantics) for the module to be
	// available. An empty or nil result means no capability can satisfy the
	// requirement (HasAny with zero ids is always false), so the module
	// would never be available under this interface alone — in practice a
	// module implementing CapabilityGateAny is expected to return at least
	// one ID.
	RequiredAnyCapabilities() []capability.ID
}

// GateAny returns an http.HandlerFunc that checks host's currently
// advertised capability.Set against ids and either delegates to next (when
// at least one id in ids is present) or responds 404 (when none are). It is
// the any-of sibling of Gate, letting a module's routes stay mounted on the
// shared mux while a request against a capability set the host doesn't
// satisfy any of 404s at request time.
func GateAny(host Host, ids []capability.ID, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !host.Capabilities(r.Context()).HasAny(ids...) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// AvailableAny reports whether module is available given caps: a Module
// implementing CapabilityGateAny is available only when caps has at least
// one of its RequiredAnyCapabilities (HasAny semantics); a Module that does
// not implement CapabilityGateAny has no any-of capability requirement and
// is always available under this interface, mirroring Available's
// default-available convention. This is the same test GateAny applies to a
// single request, applied instead to a whole registry's module list.
func AvailableAny(module Module, caps capability.Set) bool {
	gate, ok := module.(CapabilityGateAny)
	if !ok {
		return true
	}
	return caps.HasAny(gate.RequiredAnyCapabilities()...)
}
