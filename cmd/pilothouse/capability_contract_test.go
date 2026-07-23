package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/frostyard/pilothouse/internal/web"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the contract-test harness described in the mill plan for
// issue #54: it builds the *real* module registry via newRegistry(...) (the
// same function run() calls) and a real web.NewServer, then drives the
// assembled app entirely through Server.Handler() HTTP round trips against
// a fake broker. c11 extends this same file with degraded-capability
// fixtures reusing the helpers below, so nothing here should be specific to
// the full-capability case beyond the fixture each test constructs.

// contractIdentity is the authenticated identity used by every contract
// test: an administrator, so every module's admin-gated view (activity,
// logs, files) actually queries the broker and renders its real content
// rather than an early "access denied" page that would never exercise the
// module's Query call.
var contractIdentity = auth.Identity{Admin: true, Groups: []string{"wheel"}, UID: 1000, Username: "operator"}

// fullCapabilitySet returns every capability.ID the vocabulary defines,
// matching a host with every optional dependency present — today's
// unchanged, pre-capability-gating behavior.
func fullCapabilitySet() capability.Set {
	return capability.New(
		capability.Systemd,
		capability.Journald,
		capability.Updex,
		capability.Sysext,
		capability.Bootc,
		capability.RPMOStree,
		capability.AutoupdateRPMOStree,
		capability.AutoupdateBootc,
		capability.Podman,
		capability.Docker,
		capability.Incus,
	)
}

// fakeCapabilityBroker implements web.BrokerClient. Session always succeeds
// for any token; Login always succeeds and returns contractIdentity; Query
// answers broker.QueryCapabilities with the configured capability.Set and
// otherwise fills the caller's target with a minimal valid canned
// zero-value-shaped response (an empty JSON object for struct targets, an
// empty JSON array for slice/array targets) so every module page can query
// its state and render without erroring, regardless of which broker ID it
// calls. Action/StreamAction/StreamQuery all succeed trivially.
type fakeCapabilityBroker struct {
	capabilities capability.Set
}

func newFakeCapabilityBroker(caps capability.Set) *fakeCapabilityBroker {
	return &fakeCapabilityBroker{capabilities: caps}
}

func (b *fakeCapabilityBroker) Action(context.Context, string, string, map[string]string, string) error {
	return nil
}

func (b *fakeCapabilityBroker) Health(context.Context) error { return nil }

func (b *fakeCapabilityBroker) Login(context.Context, string, string, string) (broker.LoginResponse, error) {
	return broker.LoginResponse{
		Session: broker.SessionResponse{CSRF: "contract-csrf", Identity: contractIdentity},
		Token:   "contract-token",
	}, nil
}

func (b *fakeCapabilityBroker) Logout(context.Context, string) error { return nil }

func (b *fakeCapabilityBroker) Query(_ context.Context, _, id string, _ map[string]string, target any) error {
	if id == broker.QueryCapabilities {
		encoded, err := json.Marshal(b.capabilities)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	}
	return cannedQueryResponse(target)
}

func (b *fakeCapabilityBroker) Session(context.Context, string) (broker.SessionResponse, error) {
	return broker.SessionResponse{CSRF: "contract-csrf", Identity: contractIdentity}, nil
}

func (b *fakeCapabilityBroker) StreamAction(context.Context, string, string, map[string]string, io.Reader) error {
	return nil
}

func (b *fakeCapabilityBroker) StreamQuery(context.Context, string, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

// cannedQueryResponse fills target (always a pointer, as every host.Query
// caller in this codebase passes one) with a minimal valid zero-value JSON
// response shaped to target's underlying kind: an empty array for a
// slice/array-typed target (e.g. []audit.Record, []jobs.Job), an empty
// object otherwise (every module State/Snapshot/Logs/Journal struct).
// Deriving the shape from target's real type, rather than hand-listing a
// response per broker.Query* ID, means this keeps working as modules and
// their state types change without needing per-ID maintenance here.
func cannedQueryResponse(target any) error {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	switch value.Elem().Kind() {
	case reflect.Slice, reflect.Array:
		return json.Unmarshal([]byte("[]"), target)
	default:
		return json.Unmarshal([]byte("{}"), target)
	}
}

// newCapabilityContractServer builds the production registry via
// newRegistry("", "") and wires it into a real web.NewServer backed by
// broker, returning both the registry (so tests can enumerate the real
// module list) and the assembled HTTP handler. Using newRegistry(...)
// rather than a hand-built module list is the whole point of this harness:
// per docs/agents/skills/completeness-tests-need-live-source-of-truth.md,
// a completeness assertion is only meaningful when it is checked against
// the live production wiring, not a second copy of the module list that
// could silently drift from it.
func newCapabilityContractServer(t *testing.T, brokerClient web.BrokerClient) (*platform.Registry, http.Handler) {
	t.Helper()
	registry, err := newRegistry("", "")
	require.NoError(t, err)
	server, err := web.NewServer(registry, brokerClient, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	return registry, server.Handler()
}

var loginCSRFPattern = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

// loginSession drives the real POST /login flow — GET /login to recover the
// server's per-instance login CSRF token from the rendered form, then POST
// credentials — and returns the resulting session cookie. This is the only
// way to populate internal/web.Server's capability cache from outside
// package web (login is what triggers refreshCapabilities), so every
// contract test needs it before asserting on capability-gated nav/routes.
func loginSession(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	getRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, getRequest)
	require.Equal(t, http.StatusOK, getRecorder.Code)
	match := loginCSRFPattern.FindStringSubmatch(getRecorder.Body.String())
	require.Lenf(t, match, 2, "login csrf token not found in rendered login page: %s", getRecorder.Body.String())

	form := url.Values{"csrf": {match[1]}, "username": {"operator"}, "password": {"password"}}
	postRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	postRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRecorder := httptest.NewRecorder()
	handler.ServeHTTP(postRecorder, postRequest)
	require.Equal(t, http.StatusSeeOther, postRecorder.Code, "login did not redirect: %s", postRecorder.Body.String())

	for _, cookie := range postRecorder.Result().Cookies() {
		if cookie.Name == "pilothouse_session" {
			return cookie
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil
}

// TestCapabilityContractFullCapabilityFixture proves the trivial,
// highest-value property first: under a fixture with every capability
// present (today's behavior, unchanged by the whole capability-gating
// phase), nothing regresses. GET / renders every registered module's
// nav/dashboard identifying text, and every registered module's primary
// route (its Manifest().Path) responds without 404ing.
func TestCapabilityContractFullCapabilityFixture(t *testing.T) {
	registry, handler := newCapabilityContractServer(t, newFakeCapabilityBroker(fullCapabilitySet()))
	cookie := loginSession(t, handler)

	modules := registry.Modules()
	require.NotEmpty(t, modules)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()

	for _, module := range modules {
		manifest := module.Manifest()
		assert.Containsf(t, body, manifest.Name,
			"dashboard is missing a nav/dashboard reference for module %q under the full-capability fixture", manifest.ID)
	}

	for _, module := range modules {
		manifest := module.Manifest()
		routeRequest := httptest.NewRequest(http.MethodGet, manifest.Path, nil)
		routeRequest.AddCookie(cookie)
		routeRecorder := httptest.NewRecorder()
		handler.ServeHTTP(routeRecorder, routeRequest)
		assert.NotEqualf(t, http.StatusNotFound, routeRecorder.Code,
			"module %q primary route %s returned 404 under the full-capability fixture", manifest.ID, manifest.Path)
	}
}
