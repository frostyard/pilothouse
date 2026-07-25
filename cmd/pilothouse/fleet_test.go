package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/frostyard/pilothouse/internal/web"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers the --dev gate on the Fleet preview module. Fleet is a
// static, non-functional mock (internal/modules/fleet) with no real
// multi-system transport or enrollment behind it, so production builds must
// not expose it at all: packaging/pilothouse.service's ExecStart does not
// pass --dev, so run() calls newRegistry(false).
//
// The point of gating at *registration* rather than with a capability gate is
// that Mount() is never called, so Fleet's routes are genuinely absent from
// the mux — the 404s asserted below come from there being no route, not from
// platform.Gate rejecting a mounted one. The assertions therefore go through
// the real production path end to end: the actual newRegistry(dev) the
// binary's run() calls, a real web.NewServer, and real authenticated HTTP
// round trips through Server.Handler(), per docs/agents/skills/exercise-the-
// actual-boundary-not-a-precomputed-shim.md.
//
// cmd/pilothouse/capability_contract_test.go's harness deliberately stays on
// newRegistry(true) (see newCapabilityContractServer); the dev-off default is
// covered here instead of by expanding that harness's fixture matrix.

// systemPickerRegionPattern isolates the sidebar's system-picker block — the
// markup between its opening <div class="system-picker"> and the primary
// navigation that follows it. Scoping to it (rather than checking the whole
// page for "/fleet") is what makes the picker assertion independent of the
// nav assertion, since before this change the picker carried its own
// hardcoded href that no nav-scoped check would have caught.
var systemPickerRegionPattern = regexp.MustCompile(`(?s)<div class="system-picker">(.*?)<nav\b`)

// newFleetGateServer builds the production registry for the given --dev value
// via the same newRegistry the binary's run() calls, wires it into a real
// web.NewServer backed by the same fake broker the capability contract
// harness uses (full capabilities, so nothing here is gated off for an
// unrelated capability reason), and returns both.
func newFleetGateServer(t *testing.T, dev bool) (*platform.Registry, http.Handler) {
	t.Helper()
	caps := fullCapabilitySet()
	brokerClient := newFakeCapabilityBroker(t, caps, cannedHostImageStatus(), calibratedAutoUpdateStatus(caps), calibratedExtensionsState(caps))
	registry, err := newRegistry(dev)
	require.NoError(t, err)
	server, err := web.NewServer(registry, brokerClient, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	return registry, server.Handler()
}

// registryHasFleet reports whether the registry actually constructed contains
// the fleet module, read from the manifests the registry holds. This is a
// direct check of what newRegistry built, not a restatement of the flag it
// was handed.
func registryHasFleet(registry *platform.Registry) bool {
	for _, module := range registry.Modules() {
		if module.Manifest().ID == "fleet" {
			return true
		}
	}
	return false
}

// fleetRoutes is every route internal/modules/fleet's Mount registers a
// static, parameterless handler for. Each must be unreachable when fleet is
// not registered and reachable when it is.
var fleetRoutes = []string{"/fleet", "/fleet/enroll"}

func TestFleetAbsentWithoutDevFlag(t *testing.T) {
	registry, handler := newFleetGateServer(t, false)

	assert.False(t, registryHasFleet(registry),
		"newRegistry(false) must not construct the fleet preview module")

	cookie := loginSession(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()

	navSection := extractRequiredSection(t, navSectionPattern, body, "GET /", "primary navigation")
	// Anchor first, so the absence assertions below cannot pass vacuously
	// against an empty or mis-located region.
	require.Contains(t, navSection, `href="/storage"`,
		"primary navigation must still carry the modules that are registered")
	assert.NotContains(t, navSection, `href="/fleet"`,
		"primary navigation must not link to the unregistered fleet module")
	assert.NotContains(t, navSection, ">Fleet<",
		"primary navigation must not carry a Fleet entry")

	pickerSection := extractRequiredSection(t, systemPickerRegionPattern, body, "GET /", "system picker")
	require.Contains(t, pickerSection, "connected · this host",
		"the system-picker must still render the local system entry")
	assert.NotContains(t, pickerSection, "/fleet",
		"the system-picker link must be derived from the module list, not hardcoded")

	for _, route := range fleetRoutes {
		routeRequest := httptest.NewRequest(http.MethodGet, route, nil)
		routeRequest.AddCookie(cookie)
		routeRecorder := httptest.NewRecorder()
		handler.ServeHTTP(routeRecorder, routeRequest)
		assert.Equalf(t, http.StatusNotFound, routeRecorder.Code,
			"GET %s must 404 when fleet was never mounted", route)
	}
}

// TestFleetPresentWithDevFlag is the regression guard for the other
// direction: --dev must still produce exactly the pre-change behavior, so the
// gate can't silently become "fleet is gone everywhere".
func TestFleetPresentWithDevFlag(t *testing.T) {
	registry, handler := newFleetGateServer(t, true)

	assert.True(t, registryHasFleet(registry),
		"newRegistry(true) must construct the fleet preview module")

	cookie := loginSession(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()

	navSection := extractRequiredSection(t, navSectionPattern, body, "GET /", "primary navigation")
	assert.Contains(t, navSection, `href="/fleet"`,
		"primary navigation must link to the registered fleet module")
	assert.Contains(t, navSection, ">Fleet<",
		"primary navigation must carry a Fleet entry")

	pickerSection := extractRequiredSection(t, systemPickerRegionPattern, body, "GET /", "system picker")
	assert.Contains(t, pickerSection, `href="/fleet"`,
		"the system-picker must link to the registered fleet module")

	for _, route := range fleetRoutes {
		routeRequest := httptest.NewRequest(http.MethodGet, route, nil)
		routeRequest.AddCookie(cookie)
		routeRecorder := httptest.NewRecorder()
		handler.ServeHTTP(routeRecorder, routeRequest)
		assert.Equalf(t, http.StatusOK, routeRecorder.Code,
			"GET %s must be reachable when fleet is registered", route)
	}
}
