package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullTestCapabilities matches c1's default: every capability present, so
// existing tests that don't care about gating keep exercising the
// full-capability path unchanged.
var fullTestCapabilities = capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)

type fakeHost struct {
	admin           bool
	confirmCalls    []string
	confirmResult   bool
	executeErr      error
	executeID       string
	executeParams   map[string]string
	page            platform.Page
	queryDeadline   time.Time
	queryErr        error
	queryID         string
	queryParameters map[string]string
	snapshot        Snapshot
	validateCalls   int
	validateResult  bool
	// caps overrides Capabilities' return value when capsSet is true.
	// Leaving both zero (the default for a bare &fakeHost{}) falls back to
	// fullTestCapabilities, so existing tests that never touch capability
	// gating keep exercising the full-capability path unchanged; tests that
	// need to exercise gating set both caps and capsSet explicitly.
	caps    capability.Set
	capsSet bool
}

func (host *fakeHost) Capabilities(context.Context) capability.Set {
	if !host.capsSet {
		return fullTestCapabilities
	}
	return host.caps
}
func (host *fakeHost) ConfirmAction(_ http.ResponseWriter, _ *http.Request, _ string, resource string) bool {
	host.confirmCalls = append(host.confirmCalls, resource)
	return host.confirmResult
}
func (*fakeHost) CSRFToken(*http.Request) string { return "csrf-token" }
func (host *fakeHost) Execute(_ context.Context, _ *http.Request, id string, parameters map[string]string) error {
	host.executeID = id
	host.executeParams = parameters
	return host.executeErr
}
func (host *fakeHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: host.admin} }
func (host *fakeHost) Query(ctx context.Context, queryID string, parameters map[string]string, target any) error {
	host.queryID = queryID
	host.queryParameters = parameters
	host.queryDeadline, _ = ctx.Deadline()
	if host.queryErr != nil {
		return host.queryErr
	}
	*target.(*Snapshot) = host.snapshot
	return nil
}
func (host *fakeHost) Render(_ http.ResponseWriter, _ *http.Request, page platform.Page) error {
	host.page = page
	return nil
}
func (host *fakeHost) ValidateAction(http.ResponseWriter, *http.Request) bool {
	host.validateCalls++
	return host.validateResult
}
func (*fakeHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool { return true }
func (*fakeHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (*fakeHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

func TestModuleUsesOnlyStorageQuery(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Summary: Summary{ActiveMounts: 3}}}
	module := New()
	cards, err := module.Dashboard(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, broker.QueryStorageState, host.queryID)
	assert.Nil(t, host.queryParameters)
	assert.Equal(t, platform.SpanHalf, cards[0].Span)
}

func TestHealthMapsStorageSeverity(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Findings: []Finding{{ResourceID: "disk:abc", Severity: HealthCritical, Title: "Disk health failed", Detail: "Media errors reported"}}}}
	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, platform.SeverityCritical, findings[0].Severity)
	assert.Equal(t, "/storage#disk-abc", findings[0].Path)
}

func TestHealthUsesAllocatedFindingAnchors(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{
		Resources: []Resource{{ID: "disk:abc"}, {ID: "disk/abc"}},
		Findings:  []Finding{{ResourceID: "disk/abc", Severity: HealthWarning}, {Severity: HealthWarning}},
	}}

	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 2)
	assert.Equal(t, "/storage#disk-abc-", findings[0].Path)
	assert.Equal(t, "/storage", findings[1].Path)
}

func TestHealthMapsAllStorageSeverities(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Findings: []Finding{
		{ResourceID: "healthy", Severity: HealthHealthy},
		{ResourceID: "warning", Severity: HealthWarning},
		{ResourceID: "unknown", Severity: HealthUnknown},
		{ResourceID: "unrecognized", Severity: Health("unexpected")},
		{ResourceID: "empty", Severity: ""},
	}}}
	findings, err := New().Health(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, findings, 5)
	assert.Equal(t, []platform.Severity{platform.SeverityInfo, platform.SeverityWarning, platform.SeverityUnknown, platform.SeverityUnknown, platform.SeverityUnknown}, []platform.Severity{findings[0].Severity, findings[1].Severity, findings[2].Severity, findings[3].Severity, findings[4].Severity})
}

func TestHealthFindingPathHasExactlyOnePageAnchor(t *testing.T) {
	snapshot := Snapshot{
		Resources: []Resource{{ID: "disk:abc"}},
		Findings:  []Finding{{ResourceID: "disk:abc", Severity: HealthCritical}},
	}
	findings, err := New().Health(context.Background(), &fakeHost{snapshot: snapshot})
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "/storage#disk-abc", findings[0].Path)

	var output strings.Builder
	require.NoError(t, Page(snapshot, false).Render(context.Background(), &output))
	assert.Equal(t, 1, strings.Count(output.String(), `id="disk-abc"`))
}

func TestManifest(t *testing.T) {
	assert.Equal(t, platform.Manifest{ID: "storage", Name: "Storage", Path: "/storage", Icon: "disk", Order: 25}, New().Manifest())
}

// TestModuleDoesNotImplementCapabilityGate proves storage.Module is a
// deliberate exception to the whole-module gating pattern used by
// services/backups/maintenance/logs: only its three remote-mount routes are
// capability-gated (via platform.Gate in Mount), while inventory
// (nav/dashboard/GET /storage) has no capability requirement per
// docs/capabilities.md's QueryStorageState exception. If Module ever grew a
// RequiredCapabilities method, this assertion (and the equivalent
// c2 available-modules filter behavior below) would start failing.
func TestModuleDoesNotImplementCapabilityGate(t *testing.T) {
	_, ok := any(New()).(platform.CapabilityGate)
	assert.False(t, ok, "storage.Module must not implement platform.CapabilityGate")
}

// TestModuleStaysAvailableWithoutSystemd exercises c2's real
// platform.Available (the same predicate internal/web's nav/dashboard
// filtering uses) against a capability.Set with Systemd absent, proving
// storage stays in the available-modules filter even on a no-systemd host —
// equivalent to the acceptance criterion's "storage stays in c2's
// available-modules filter under a no-systemd fixture" phrasing.
func TestModuleStaysAvailableWithoutSystemd(t *testing.T) {
	assert.True(t, platform.Available(New(), capability.Set{}))
}

func TestStoragePageUsesFixedQueryAndDeadline(t *testing.T) {
	host := &fakeHost{snapshot: Snapshot{Summary: Summary{ActiveMounts: 1}}}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	started := time.Now()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/storage", nil))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryStorageState, host.queryID)
	assert.Nil(t, host.queryParameters)
	assert.Equal(t, "storage", host.page.Active)
	assert.Equal(t, "Storage", host.page.Title)
	require.False(t, host.queryDeadline.IsZero())
	assert.WithinDuration(t, started.Add(12*time.Second), host.queryDeadline, time.Second)
}

func TestStoragePageRendersUnavailableState(t *testing.T) {
	host := &fakeHost{queryErr: errors.New("broker connection refused")}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/storage", nil))

	require.Equal(t, http.StatusOK, response.Code)
	var output strings.Builder
	require.NoError(t, host.page.Body.Render(context.Background(), &output))
	assert.Contains(t, output.String(), "Storage status is unavailable.")
	assert.NotContains(t, output.String(), "broker connection refused")
}

func TestStorageActionCreateCredentialsUsesExactParametersWithoutConfirmation(t *testing.T) {
	host := &fakeHost{admin: true, confirmResult: true, validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	request := httptest.NewRequest(http.MethodPost, "/storage/mounts", strings.NewReader("protocol=smb-credentials&server=nas.example&share=media&username=mount-user&password=secret&target=%2Fmnt%2Fmedia&version=3.1.1&read_only=false"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, 1, host.validateCalls)
	assert.Empty(t, host.confirmCalls)
	assert.Equal(t, broker.ActionStorageCreateSMBCredentials, host.executeID)
	assert.Equal(t, map[string]string{"server": "nas.example", "share": "media", "username": "mount-user", "password": "secret", "target": "/mnt/media", "version": "3.1.1", "read_only": "false"}, host.executeParams)
	assert.NotContains(t, response.Header().Get("Location"), "secret")
}

func TestStorageActionCreateCredentialsWithOwnershipUsesOwnedAction(t *testing.T) {
	host := &fakeHost{admin: true, confirmResult: true, validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	request := httptest.NewRequest(http.MethodPost, "/storage/mounts", strings.NewReader("protocol=smb-credentials&server=nas.example&share=media&username=mount-user&password=secret&target=%2Fmnt%2Fmedia&version=3.1.1&read_only=false&uid=001000&gid=000100"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, broker.ActionStorageCreateSMBCredentialsOwned, host.executeID)
	assert.Equal(t, map[string]string{"server": "nas.example", "share": "media", "username": "mount-user", "password": "secret", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000", "gid": "100"}, host.executeParams)
	assert.NotContains(t, response.Header().Get("Location"), "secret")
}

func TestStorageActionRejectsNonAdminBeforeValidationOrBroker(t *testing.T) {
	host := &fakeHost{validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/storage/mounts/0123456789abcdef0123456789abcdef/mount", nil))

	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Zero(t, host.validateCalls)
	assert.Empty(t, host.executeID)
}

func TestStorageActionUnmountConfirmsExactResourceAndUsesHXRedirect(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	host := &fakeHost{admin: true, confirmResult: true, validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	request := httptest.NewRequest(http.MethodPost, "/storage/mounts/"+id+"/unmount", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Contains(t, response.Header().Get("HX-Redirect"), "Mount+unmounted")
	assert.Equal(t, 1, host.validateCalls)
	assert.Equal(t, []string{"storage/mount/" + id}, host.confirmCalls)
	assert.Equal(t, broker.ActionStorageUnmount, host.executeID)
	assert.Equal(t, map[string]string{"id": id}, host.executeParams)
}

func TestStorageActionFailureDoesNotExposeBrokerError(t *testing.T) {
	host := &fakeHost{admin: true, executeErr: errors.New("credential path leaked"), validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/storage/mounts/0123456789abcdef0123456789abcdef/mount", nil))

	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.Contains(t, response.Header().Get("Location"), "Action+failed.+Review+Activity+for+the+recorded+outcome.")
	assert.NotContains(t, response.Header().Get("Location"), "credential")
}

func TestStorageActionCreateNFSAndSMBGuestUsePasswordFreeParameters(t *testing.T) {
	for _, test := range []struct {
		name     string
		form     string
		actionID string
		params   map[string]string
	}{
		{"nfs", "protocol=nfs&host=nas.example&export=%2Fmedia&target=%2Fmnt%2Fmedia&version=4.2&read_only=true", broker.ActionStorageCreateNFS, map[string]string{"host": "nas.example", "export": "/media", "target": "/mnt/media", "version": "4.2", "read_only": "true"}},
		{"smb guest", "protocol=smb-guest&server=nas.example&share=media&target=%2Fmnt%2Fmedia&version=3.1.1&read_only=false", broker.ActionStorageCreateSMBGuest, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false"}},
		{"smb guest owned", "protocol=smb-guest&server=nas.example&share=media&target=%2Fmnt%2Fmedia&version=3.1.1&read_only=false&uid=001000&gid=000100", broker.ActionStorageCreateSMBGuestOwned, map[string]string{"server": "nas.example", "share": "media", "target": "/mnt/media", "version": "3.1.1", "read_only": "false", "uid": "1000", "gid": "100"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{admin: true, validateResult: true}
			mux := http.NewServeMux()
			New().Mount(mux, host)
			request := httptest.NewRequest(http.MethodPost, "/storage/mounts", strings.NewReader(test.form))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			mux.ServeHTTP(httptest.NewRecorder(), request)

			assert.Equal(t, test.actionID, host.executeID)
			assert.Equal(t, test.params, host.executeParams)
			assert.NotContains(t, host.executeParams, "password")
		})
	}
}

func TestStorageActionCreateRejectsInvalidSMBOwnership(t *testing.T) {
	for _, test := range []struct {
		name string
		form string
	}{
		{"uid only", "uid=1000"},
		{"gid only", "gid=100"},
		{"sentinel", "uid=4294967295&gid=100"},
		{"signed", "uid=%2B1000&gid=100"},
		{"malformed", "uid=nope&gid=100"},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{admin: true, validateResult: true}
			mux := http.NewServeMux()
			New().Mount(mux, host)
			request := httptest.NewRequest(http.MethodPost, "/storage/mounts", strings.NewReader("protocol=smb-guest&server=nas.example&share=media&target=%2Fmnt%2Fmedia&version=3.1.1&read_only=false&"+test.form))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			mux.ServeHTTP(response, request)

			assert.Equal(t, http.StatusSeeOther, response.Code)
			assert.Empty(t, host.executeID)
			assert.Equal(t, "/storage?kind=error&notice=Action+failed.+Review+Activity+for+the+recorded+outcome.", response.Header().Get("Location"))
			assert.NotContains(t, response.Header().Get("Location"), test.form)
		})
	}
}

func TestStorageNewMountAndCreateRejectNonAdminWithoutActionValidation(t *testing.T) {
	host := &fakeHost{validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	newForm := httptest.NewRecorder()
	mux.ServeHTTP(newForm, httptest.NewRequest(http.MethodGet, "/storage/mounts/new", nil))
	create := httptest.NewRecorder()
	mux.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/storage/mounts", strings.NewReader("protocol=nfs")))

	assert.Equal(t, http.StatusForbidden, newForm.Code)
	assert.Equal(t, http.StatusForbidden, create.Code)
	assert.Zero(t, host.validateCalls)
	assert.Empty(t, host.executeID)
}

func TestStorageActionDeleteConfirmsAndExecutes(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	host := &fakeHost{admin: true, confirmResult: true, validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/storage/mounts/"+id+"/delete", nil))

	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, []string{"storage/mount/" + id}, host.confirmCalls)
	assert.Equal(t, broker.ActionStorageDelete, host.executeID)
	assert.Equal(t, map[string]string{"id": id}, host.executeParams)
}

// TestStoragePageStaysAvailableWithoutSystemd proves GET /storage — unlike
// the three remote-mount routes below — is not wrapped in platform.Gate:
// with Systemd absent it still returns 200 and renders inventory, capacity,
// and findings, matching docs/capabilities.md's QueryStorageState
// exception.
func TestStoragePageStaysAvailableWithoutSystemd(t *testing.T) {
	host := &fakeHost{
		caps:     capability.Set{},
		capsSet:  true,
		snapshot: Snapshot{Summary: Summary{ActiveMounts: 2, UsableBytes: 10 * 1024 * 1024 * 1024, UsedBytes: 4 * 1024 * 1024 * 1024}, Findings: []Finding{{ResourceID: "disk:abc", Title: "Disk needs attention"}}},
	}
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/storage", nil))

	require.Equal(t, http.StatusOK, response.Code)
	var output strings.Builder
	require.NoError(t, host.page.Body.Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "2 active mounts")
	assert.Contains(t, html, "10.0 GiB usable")
	assert.Contains(t, html, "Disk needs attention")
}

// TestStorageRemoteMountRoutesRequireSystemd proves the three remote-mount
// routes GET /storage/mounts/new, POST /storage/mounts, and
// POST /storage/mounts/{id}/{action} — and only those routes — 404 when the
// host's capability set lacks Systemd, without ever reaching
// host.ValidateAction/host.Execute; GET /storage stays reachable on the same
// mux (proven above), showing the gate is scoped to just these three routes,
// not the whole module.
func TestStorageRemoteMountRoutesRequireSystemd(t *testing.T) {
	host := &fakeHost{admin: true, caps: capability.Set{}, capsSet: true, confirmResult: true, validateResult: true}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	newForm := httptest.NewRecorder()
	mux.ServeHTTP(newForm, httptest.NewRequest(http.MethodGet, "/storage/mounts/new", nil))
	create := httptest.NewRecorder()
	mux.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/storage/mounts", strings.NewReader("protocol=nfs")))
	mount := httptest.NewRecorder()
	mux.ServeHTTP(mount, httptest.NewRequest(http.MethodPost, "/storage/mounts/0123456789abcdef0123456789abcdef/mount", nil))
	unmount := httptest.NewRecorder()
	mux.ServeHTTP(unmount, httptest.NewRequest(http.MethodPost, "/storage/mounts/0123456789abcdef0123456789abcdef/unmount", nil))
	deleteMount := httptest.NewRecorder()
	mux.ServeHTTP(deleteMount, httptest.NewRequest(http.MethodPost, "/storage/mounts/0123456789abcdef0123456789abcdef/delete", nil))

	for name, response := range map[string]*httptest.ResponseRecorder{
		"GET /storage/mounts/new":           newForm,
		"POST /storage/mounts":              create,
		"POST /storage/mounts/{id}/mount":   mount,
		"POST /storage/mounts/{id}/unmount": unmount,
		"POST /storage/mounts/{id}/delete":  deleteMount,
	} {
		assert.Equal(t, http.StatusNotFound, response.Code, name)
	}
	assert.Zero(t, host.validateCalls)
	assert.Empty(t, host.executeID)
	assert.Empty(t, host.confirmCalls)
}

// TestStoragePageThreadsCapabilitiesIntoRemoteMountControls exercises the
// real GET /storage handler (not a direct ManagedPage call) end to end,
// proving module.go actually derives remoteMountsAvailable from
// host.Capabilities(r.Context()).Has(capability.Systemd) and threads it into
// the rendered page: with Systemd present the Add-remote-mount link and a
// managed mount's Unmount/Delete controls render; with Systemd absent none
// of them do, even though the same managed "mounted" mount would otherwise
// show Unmount and always shows Delete.
func TestStoragePageThreadsCapabilitiesIntoRemoteMountControls(t *testing.T) {
	snapshot := Snapshot{Mounts: []Mount{{ID: "remote:0123456789abcdef0123456789abcdef", Managed: true, State: "mounted"}}}
	for _, test := range []struct {
		name      string
		caps      capability.Set
		available bool
	}{
		{"systemd present", capability.New(capability.Systemd), true},
		{"systemd absent", capability.Set{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{admin: true, caps: test.caps, capsSet: true, snapshot: snapshot}
			mux := http.NewServeMux()
			New().Mount(mux, host)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/storage", nil))

			require.Equal(t, http.StatusOK, response.Code)
			var output strings.Builder
			require.NoError(t, host.page.Body.Render(context.Background(), &output))
			html := output.String()
			assert.Equal(t, test.available, strings.Contains(html, "Add remote mount"))
			assert.Equal(t, test.available, strings.Contains(html, `>Unmount</button>`))
			assert.Equal(t, test.available, strings.Contains(html, `>Delete</button>`))
		})
	}
}
