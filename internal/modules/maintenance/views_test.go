package maintenance

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageRendersMaintenanceStateAndComponents(t *testing.T) {
	state := State{OSVersion: "Snosi", RebootRequired: true, RebootReasons: []string{"Update requires reboot"}, Updates: []sysext.AvailableUpdate{{Feature: "docker", Component: "root", Current: "1", Newest: "2"}}, Jobs: []Job{{ID: 1, Status: jobs.StatusSucceeded}}}
	var output bytes.Buffer
	require.NoError(t, Page(state, nil, "csrf", true).Render(context.Background(), &output))
	html := output.String()
	assert.Contains(t, html, "Update requires reboot")
	assert.Contains(t, html, "Reboot host")
	assert.Contains(t, html, "<svg")
	assert.NotContains(t, html, "@web.")
}

// hostImageOpenTag is the opening tag of the host-image section's own
// container. Every host-image assertion below is scoped to the fragment it
// opens (or asserted absent by looking for this exact marker), rather than run
// against the whole page: the page legitimately mentions reboots, images, and
// versions in three other regions, so a page-wide Contains could not tell
// "rendered in the host-image section" apart from "rendered somewhere else"
// (docs/agents/skills/scope-html-assertions-to-the-region-under-test.md).
const hostImageOpenTag = `<article class="card" id="host-image"`

// rebootCardOpenTag is the opening tag of the Systemd-gated reboot-posture
// area — the pre-existing reboot-required card that also contains the reboot
// form. It is scoped separately so the "soft-reboot eligibility renders
// exactly once, and in the host-image section" claim is checkable at both
// ends: present in one region, absent from the other.
const rebootCardOpenTag = `<article class="card error-card">`

// softRebootMarker is the data attribute the soft-reboot-eligibility indicator
// carries. Counting it is how "rendered exactly once on the page" is asserted
// without depending on prose wording.
const softRebootMarker = `data-soft-reboot=`

// renderMaintenancePage drives a real GET /maintenance through the module's
// own Mount + handler against a fake host advertising caps, and returns the
// rendered page body HTML. Going through the handler (rather than calling Page
// directly) is deliberate: it is the handler that decides whether to fetch
// QueryHostImageStatus at all, so these fixtures exercise the production
// capability gating and the rendering together.
func renderMaintenancePage(t *testing.T, caps capability.Set, state State, hostImage HostImageStatus) string {
	t.Helper()
	host := &moduleHost{caps: caps, capsSet: true, state: state, hostImage: hostImage}
	mux := http.NewServeMux()
	New().Mount(mux, host)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.True(t, host.rendered, "GET /maintenance must render a page")

	html := renderComponent(t, host.page.Body)
	// AGENTS.md's templ composition rule: a component call embedded in a text
	// node renders literally. Every fixture checks it, so a regression in any
	// one of the new conditional branches is caught where it happens.
	assert.NotContains(t, html, "@web.", "a templ component call leaked into the rendered HTML as literal text")
	return html
}

// stagedBootcState is a State that would render the reboot-required card and
// its reboot form on a host with systemd. Every host-image fixture below is
// driven with it, so the "no reboot card, no reboot form when Systemd is
// absent" assertions are not vacuously true — the state that would render them
// is present in the fixture and only the capability gate suppresses it.
func stagedBootcState() State {
	return State{OSVersion: "42.20260101", RebootRequired: true, RebootReasons: []string{stagedHostImageReason}, SoftRebootCapable: boolPtr(true)}
}

// fullHostImageStatus is a populated host-image report: a booted deployment
// with an image reference and digest, plus staged and rollback deployments.
// Per docs/agents/skills/canned-fixtures-need-populated-data-for-what-they-
// assert.md, every slot whose rendering these tests assert on is populated, so
// an "absent" assertion under a gated fixture cannot pass merely because the
// data was empty everywhere.
func fullHostImageStatus(softReboot *bool) HostImageStatus {
	return HostImageStatus{
		BootcAvailable:    true,
		Booted:            &Deployment{Image: "quay.io/example/os:stable", Digest: "sha256:bootedbootedbooted"},
		Staged:            &Deployment{Image: "quay.io/example/os:next", Digest: "sha256:stagedstagedstaged"},
		Rollback:          &Deployment{Image: "quay.io/example/os:previous", Digest: "sha256:rollbackrollback"},
		SoftRebootCapable: softReboot,
	}
}

// sourceErrorIndicator isolates one per-source unavailable indicator inside an
// already-isolated host-image section, so the bootc and rpm-ostree indicators
// can be asserted on independently of each other and of the section's heading.
func sourceErrorIndicator(t *testing.T, section, source string) string {
	t.Helper()
	return fragment(t, section, `data-source-error="`+source+`"`, "</div>")
}

// assertHostImageDeployments asserts the booted/staged/rollback rendering of
// fullHostImageStatus inside the host-image section, and nowhere relies on the
// surrounding page.
func assertHostImageDeployments(t *testing.T, section string) {
	t.Helper()
	assert.Contains(t, section, `data-deployment="booted"`)
	assert.Contains(t, section, "quay.io/example/os:stable")
	assert.Contains(t, section, "sha256:bootedbootedbooted")
	assert.Contains(t, section, `data-deployment="staged"`)
	assert.Contains(t, section, "quay.io/example/os:next")
	assert.Contains(t, section, `data-deployment="rollback"`)
	assert.Contains(t, section, "quay.io/example/os:previous")
}

// softRebootFixtures is the three-state matrix the acceptance criteria pin:
// non-nil true, non-nil false, and nil (unknown / not exposed by this bootc).
// It is run identically under a Systemd-absent and a Systemd-present fixture,
// which is the whole point of sourcing the indicator from HostImageStatus
// rather than from the Systemd-gated State.
var softRebootFixtures = []struct {
	name    string
	capable *bool
	// state is the data-soft-reboot value expected on the indicator, or ""
	// when no indicator may render at all.
	state string
	// text is a distinctive substring of the indicator's prose.
	text string
}{
	{name: "capable", capable: boolPtr(true), state: "capable", text: "soft reboot may be sufficient"},
	{name: "not capable", capable: boolPtr(false), state: "required", text: "full reboot is required"},
	{name: "unknown", capable: nil},
}

// TestPageRendersHostImageSectionOnBootcOnlyHostWithoutSystemd covers the
// combination the module's HasAny gate makes real and the previous UI design
// could not serve: bootc advertised, systemd absent. The host-image section
// renders in full — booted image reference and digest, plus the staged and
// rollback deployments — and the soft-reboot-eligibility indicator renders
// with it, proving the indicator is not withheld merely because Systemd is
// absent. The Systemd-gated reboot-posture area is correspondingly absent:
// no reboot-required card and no reboot form, even though the fixture's State
// says a reboot is required (the handler never reads it without Systemd).
func TestPageRendersHostImageSectionOnBootcOnlyHostWithoutSystemd(t *testing.T) {
	for _, fixture := range softRebootFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			html := renderMaintenancePage(t, capability.New(capability.Bootc), stagedBootcState(), fullHostImageStatus(fixture.capable))

			section := fragment(t, html, hostImageOpenTag, "</article>")
			heading := fragment(t, section, `<div class="card-heading">`, "</div>")
			assert.Contains(t, section, "<h2>Host image</h2>")
			// AGENTS.md's templ composition rule has two halves: the
			// literal-call check renderMaintenancePage already makes, and this
			// one — the component's actual output must be present. The section's
			// heading invokes @web.Icon("server") on its own template node, so
			// its rendered <svg> must appear inside the heading itself.
			assert.Contains(t, heading, "<svg", "@web.Icon must render its SVG output in the host-image heading")
			assertHostImageDeployments(t, section)

			assert.NotContains(t, html, rebootCardOpenTag, "no reboot-required card may render without systemd")
			assert.NotContains(t, html, "/maintenance/reboot", "no reboot form may render without systemd")

			if fixture.capable == nil {
				assert.NotContains(t, html, softRebootMarker, "an unknown soft-reboot eligibility must render nothing at all")
				return
			}
			assert.Contains(t, section, softRebootMarker+`"`+fixture.state+`"`)
			assert.Contains(t, section, fixture.text)
			assert.Equal(t, 1, strings.Count(html, softRebootMarker), "the soft-reboot indicator must render exactly once on the page")
		})
	}
}

// TestPageRendersSoftRebootIndicatorIdenticallyWithSystemd is the other half of
// the same claim: adding Systemd changes nothing about the soft-reboot
// indicator. It renders in the same host-image section, with the same
// non-nil-true / non-nil-false / nil semantics, and the reboot-posture area
// that Systemd does switch on carries only its pre-existing reboot-required
// card and reboot form — never a second copy of the indicator.
func TestPageRendersSoftRebootIndicatorIdenticallyWithSystemd(t *testing.T) {
	for _, fixture := range softRebootFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			bootcOnly := renderMaintenancePage(t, capability.New(capability.Bootc), stagedBootcState(), fullHostImageStatus(fixture.capable))
			withSystemd := renderMaintenancePage(t, capability.New(capability.Systemd, capability.Bootc), stagedBootcState(), fullHostImageStatus(fixture.capable))

			section := fragment(t, withSystemd, hostImageOpenTag, "</article>")
			rebootCard := fragment(t, withSystemd, rebootCardOpenTag, "</article>")

			// The reboot-posture area is genuinely present here, so the
			// "never a second indicator" assertion below is not vacuous.
			assert.Contains(t, rebootCard, "<h2>Reboot required</h2>")
			assert.Contains(t, rebootCard, `action="/maintenance/reboot"`)
			assert.NotContains(t, rebootCard, softRebootMarker, "the reboot-posture area must not render its own soft-reboot indicator")
			assert.NotContains(t, rebootCard, "soft reboot")

			// Byte-for-byte identical host-image sections across the two
			// fixtures is the strongest form of "Systemd changes nothing here".
			assert.Equal(t, fragment(t, bootcOnly, hostImageOpenTag, "</article>"), section)

			if fixture.capable == nil {
				assert.NotContains(t, withSystemd, softRebootMarker, "an unknown soft-reboot eligibility must render nothing at all")
				return
			}
			assert.Contains(t, section, softRebootMarker+`"`+fixture.state+`"`)
			assert.Contains(t, section, fixture.text)
			assert.Equal(t, 1, strings.Count(withSystemd, softRebootMarker), "the soft-reboot indicator must render exactly once on the page")
		})
	}
}

// bootcStatusFixture and rpmOStreeStatusFixture are real command payloads. The
// merged HostImageStatus the bootc-plus-rpm-ostree test renders is produced by
// running this package's actual production parsers and MergeHostImage over
// them, rather than by hand-writing the merged struct — so the rendering test
// is checked against c5's real merge contract, not against a restatement of it.
//
// The rpm-ostree payload deliberately spells the booted image with an ostree
// transport prefix (which bootc does not use) and reports a *conflicting*
// digest for the staged deployment, so the merge's "bootc wins outright, the
// conflicting entry is dropped whole" rule has something to actually do.
const bootcStatusFixture = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "status": {
    "booted": {"image": {"image": {"image": "quay.io/example/os:stable"}, "imageDigest": "sha256:bootedbootedbooted"}, "softRebootCapable": false},
    "staged": {"image": {"image": {"image": "quay.io/example/os:next"}, "imageDigest": "sha256:stagedstagedstaged"}, "softRebootCapable": true}
  }
}`

const rpmOStreeStatusFixture = `{
  "deployments": [
    {"booted": true, "version": "42.20260101.0", "checksum": "e1c9bootedchecksum", "container-image-reference": "ostree-unverified-registry:quay.io/example/os:stable", "container-image-reference-digest": "sha256:bootedbootedbooted"},
    {"staged": true, "version": "99.rpmostreeonly", "checksum": "ff00conflictingchecksum", "container-image-reference": "quay.io/example/os:mismatch", "container-image-reference-digest": "sha256:somethingelseentirely"}
  ]
}`

// TestPageRendersBootcAuthoritativeFieldsWithRPMOStreeSupplement covers the
// bootc-plus-rpm-ostree fixture: bootc's authoritative image reference and
// digest render alongside rpm-ostree's supplementary version and checksum, and
// nowhere does rpm-ostree's conflicting spelling or its conflicting staged
// entry appear in place of bootc's.
func TestPageRendersBootcAuthoritativeFieldsWithRPMOStreeSupplement(t *testing.T) {
	bootc, err := ParseBootcStatus([]byte(bootcStatusFixture))
	require.NoError(t, err)
	supplement, err := ParseRPMOStreeStatus([]byte(rpmOStreeStatusFixture))
	require.NoError(t, err)
	merged := MergeHostImage(bootc, supplement)
	merged.RPMOStreeAvailable = true

	html := renderMaintenancePage(t, capability.New(capability.Bootc, capability.RPMOStree), State{}, merged)
	section := fragment(t, html, hostImageOpenTag, "</article>")

	// bootc's authoritative identity for the booted slot.
	assert.Contains(t, section, "quay.io/example/os:stable")
	assert.Contains(t, section, "sha256:bootedbootedbooted")
	// rpm-ostree's supplementary detail for that same slot.
	assert.Contains(t, section, "42.20260101.0")
	assert.Contains(t, section, "e1c9bootedchecksum")
	// The staged slot exists (bootc reported it) but carries no rpm-ostree
	// detail, because rpm-ostree described a different deployment there.
	assert.Contains(t, section, `data-deployment="staged"`)
	assert.Contains(t, section, "quay.io/example/os:next")
	assert.NotContains(t, section, "99.rpmostreeonly", "a conflicting rpm-ostree version must never render")
	assert.NotContains(t, section, "ff00conflictingchecksum", "a conflicting rpm-ostree checksum must never render")
	// rpm-ostree's own spelling of the image reference is never authoritative.
	assert.NotContains(t, section, "ostree-unverified-registry:")
	assert.NotContains(t, section, "quay.io/example/os:mismatch")
	assert.NotContains(t, section, "sha256:somethingelseentirely")

	// bootc reported no rollback slot, so none renders — rpm-ostree cannot
	// invent one.
	assert.NotContains(t, section, `data-deployment="rollback"`)

	// The staged entry's softRebootCapable wins over the booted entry's, per
	// the parser, and still renders exactly once from HostImageStatus.
	assert.Contains(t, section, softRebootMarker+`"capable"`)
	assert.Equal(t, 1, strings.Count(html, softRebootMarker))
}

// TestPageRendersBootcUnavailableIndicator covers a host whose bootc
// capability is advertised but whose bootc read failed: a bootc-specific
// unavailable indicator renders in the host-image section, and rpm-ostree's
// data (which did answer) still renders alongside it.
func TestPageRendersBootcUnavailableIndicator(t *testing.T) {
	status := HostImageStatus{
		BootcError:         "run bootc status: exit status 1",
		RPMOStreeAvailable: true,
	}
	html := renderMaintenancePage(t, capability.New(capability.Bootc, capability.RPMOStree), State{}, status)
	section := fragment(t, html, hostImageOpenTag, "</article>")

	indicator := sourceErrorIndicator(t, section, "bootc")
	assert.Contains(t, indicator, "bootc status is unavailable")
	assert.Contains(t, indicator, "run bootc status: exit status 1")
	assert.Contains(t, indicator, "<svg", "@web.Icon must render its SVG output in the bootc unavailable indicator")
	assert.NotContains(t, section, `data-source-error="rpm-ostree"`, "a bootc failure must not be reported against rpm-ostree")
	assert.NotContains(t, html, softRebootMarker, "a bootc that never answered exposes no soft-reboot eligibility")
}

// TestPageRendersRPMOStreeUnavailableIndicator is the exact symmetric case:
// rpm-ostree's capability is present but its read failed, so an
// rpm-ostree-specific unavailable indicator renders while bootc's successfully
// reported deployments still render in full. It is a separate test from the
// bootc-error case on purpose — the two sources' availability/error pairs are
// independent, and collapsing them into one fixture would let a regression in
// either indicator hide behind the other.
func TestPageRendersRPMOStreeUnavailableIndicator(t *testing.T) {
	status := fullHostImageStatus(boolPtr(true))
	status.RPMOStreeError = "run rpm-ostree status: exit status 1"

	html := renderMaintenancePage(t, capability.New(capability.Bootc, capability.RPMOStree), State{}, status)
	section := fragment(t, html, hostImageOpenTag, "</article>")

	indicator := sourceErrorIndicator(t, section, "rpm-ostree")
	assert.Contains(t, indicator, "rpm-ostree status is unavailable")
	assert.Contains(t, indicator, "run rpm-ostree status: exit status 1")
	assert.Contains(t, indicator, "<svg", "@web.Icon must render its SVG output in the rpm-ostree unavailable indicator")
	assert.NotContains(t, section, `data-source-error="bootc"`, "an rpm-ostree failure must not be reported against bootc")
	// bootc answered, so its deployments and its soft-reboot fact still render.
	assertHostImageDeployments(t, section)
	assert.Contains(t, section, softRebootMarker+`"capable"`)
}

// TestPageOmitsHostImageSectionWithoutAnySource covers the neither-bootc-nor-
// rpm-ostree fixture: the host-image section is omitted outright — not rendered
// as an empty or errored placeholder — and no soft-reboot indicator appears
// anywhere. The module itself is still present (systemd is advertised), so this
// proves the section's own gate, not the whole-module gate.
func TestPageOmitsHostImageSectionWithoutAnySource(t *testing.T) {
	html := renderMaintenancePage(t, capability.New(capability.Systemd), stagedBootcState(), fullHostImageStatus(boolPtr(true)))

	assert.NotContains(t, html, hostImageOpenTag)
	assert.NotContains(t, html, "Host image")
	assert.NotContains(t, html, `data-deployment=`)
	assert.NotContains(t, html, `data-source-error=`)
	assert.NotContains(t, html, softRebootMarker)
	assert.NotContains(t, html, "soft reboot")
	// The rest of the module has not disappeared with it.
	assert.Contains(t, html, "Update availability, durable maintenance jobs, and host reboot posture.")
	assert.Contains(t, html, "<h2>Recent maintenance jobs</h2>")
	assert.Contains(t, html, rebootCardOpenTag)
}

// formActionPattern and interactivePattern find every element on the page that
// could act on the host: a submitting form, a button, or an HTMX-issued
// request. They back the "no lifecycle mutation control exists" assertion.
var (
	formActionPattern  = regexp.MustCompile(`<form[^>]*\saction="([^"]*)"`)
	interactivePattern = regexp.MustCompile(`<button|hx-post=|hx-put=|hx-patch=|hx-delete=`)
)

// TestPageExposesNoHostImageMutationControl pins the spec's "no lifecycle
// mutations" requirement as checkable behavior across every fixture that
// renders host-image content: the only form on the page is the pre-existing
// systemd-gated reboot form, the host-image section contains no interactive
// element at all, and no upgrade/switch/rebase/rollback/automatic-update
// wording appears as a control anywhere.
func TestPageExposesNoHostImageMutationControl(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		caps   capability.Set
		status HostImageStatus
	}{
		{name: "bootc only", caps: capability.New(capability.Bootc), status: fullHostImageStatus(boolPtr(true))},
		{name: "bootc and systemd", caps: capability.New(capability.Systemd, capability.Bootc), status: fullHostImageStatus(boolPtr(false))},
		{name: "bootc and rpm-ostree", caps: capability.New(capability.Bootc, capability.RPMOStree), status: fullHostImageStatus(nil)},
		{name: "rpm-ostree only", caps: capability.New(capability.RPMOStree), status: fullHostImageStatus(boolPtr(true))},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			html := renderMaintenancePage(t, fixture.caps, stagedBootcState(), fixture.status)

			for _, match := range formActionPattern.FindAllStringSubmatch(html, -1) {
				assert.Equal(t, "/maintenance/reboot", match[1], "the reboot form is the only form this page may render")
			}

			section := fragment(t, html, hostImageOpenTag, "</article>")
			assert.NotRegexp(t, interactivePattern, section, "the host-image section must contain no interactive control")
			assert.NotContains(t, section, "<form")
			assert.NotContains(t, section, "<a ")
			for _, verb := range []string{"upgrade", "Upgrade", "switch", "Switch", "rebase", "Rebase", "roll back", "Roll back", "automatic update", "Automatic update"} {
				assert.NotContains(t, html, verb, "no bootc/rpm-ostree lifecycle mutation wording may appear on the page")
			}
		})
	}
}
