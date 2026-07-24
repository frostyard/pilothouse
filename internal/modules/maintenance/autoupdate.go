package maintenance

import "time"

// The normalized bootc automatic-update policy vocabulary. These three values
// are the complete, closed set BootcAutoUpdate.Policy may ever hold; anything
// outside it is a bug.
//
// Only two of the three are reachable from real inputs today.
// NormalizeBootcAutoUpdatePolicy can return BootcPolicyApply or
// BootcPolicyCustomUnknown; it cannot return BootcPolicyStageOnly, because
// upstream bootc publishes no drop-in-presence signal that distinguishes
// "stage but do not apply" from any other local customization. That is a
// deliberate, documented limitation rather than an oversight -- see
// docs/autoupdate.md for the full derivation and the upstream citations.
// BootcPolicyStageOnly is defined, tested, and wire-representable so the
// reserved value exists the day a future bootc release ships a signal the
// classifier can key on.
const (
	// BootcPolicyApply means the host runs bootc's shipped
	// bootc-fetch-apply-updates units unmodified: updates are fetched and
	// applied on the timer's schedule.
	BootcPolicyApply = "apply"
	// BootcPolicyCustomUnknown means the effective units carry at least one
	// local drop-in, so what they actually do can no longer be asserted from
	// drop-in presence alone.
	BootcPolicyCustomUnknown = "custom/unknown"
	// BootcPolicyStageOnly means updates are fetched and staged but not
	// applied automatically. It is a reserved value: no input to
	// NormalizeBootcAutoUpdatePolicy produces it today.
	BootcPolicyStageOnly = "stage-only"
)

// BootcAutoUpdate is the read-only automatic-update picture for bootc's
// bootc-fetch-apply-updates.timer / bootc-fetch-apply-updates.service pair. It
// is purely descriptive: nothing here enables, disables, triggers, or
// reconfigures the updater, and no field is ever written back to the host.
//
// TimerActiveState / TimerUnitFileState and ServiceActiveState / ServiceResult
// reuse the vocabulary internal/modules/backups already reports for systemd
// timers (systemd's own ActiveState, UnitFileState, and Result property
// values), so the two surfaces name the same facts the same way. NextTrigger is
// the timer's next scheduled run and is the zero time when the host reports
// none.
//
// TimerDropinsPresent and ServiceDropinsPresent are two independent booleans,
// never a single merged flag and never a path list: a drop-in on the timer
// changes when the updater runs, while a drop-in on the service changes what it
// does, and callers need to tell those apart. Policy holds one of the three
// Bootc* policy constants above.
//
// Absent sub-values use the zero value rather than being omitted from the wire
// form, so a configured updater always reports the same field set.
type BootcAutoUpdate struct {
	NextTrigger           time.Time `json:"next_trigger"`
	Policy                string    `json:"policy"`
	ServiceActiveState    string    `json:"service_active_state"`
	ServiceDropinsPresent bool      `json:"service_dropins_present"`
	ServiceResult         string    `json:"service_result"`
	TimerActiveState      string    `json:"timer_active_state"`
	TimerDropinsPresent   bool      `json:"timer_dropins_present"`
	TimerUnitFileState    string    `json:"timer_unit_file_state"`
}

// RPMOStreeAutoUpdate is the same read-only picture for rpm-ostree's
// rpm-ostreed-automatic.timer / rpm-ostreed-automatic.service pair.
//
// It carries the same field set as BootcAutoUpdate and is deliberately a
// separate type rather than a shared one: rpm-ostree's Policy vocabulary is its
// own (it has a native automatic-update policy setting with values bootc has no
// equivalent for), so collapsing the two updaters into one struct would invite
// a shared policy enum the spec explicitly rejects. Keeping them apart makes it
// impossible to hand a bootc policy string to an rpm-ostree payload by
// accident.
//
// This chunk defines the type and its wire shape only. The normalizer that
// fills Policy for rpm-ostree, and the manager that reads the live host, are
// not part of this change.
type RPMOStreeAutoUpdate struct {
	NextTrigger           time.Time `json:"next_trigger"`
	Policy                string    `json:"policy"`
	ServiceActiveState    string    `json:"service_active_state"`
	ServiceDropinsPresent bool      `json:"service_dropins_present"`
	ServiceResult         string    `json:"service_result"`
	TimerActiveState      string    `json:"timer_active_state"`
	TimerDropinsPresent   bool      `json:"timer_dropins_present"`
	TimerUnitFileState    string    `json:"timer_unit_file_state"`
}

// AutoUpdateStatus is the whole automatic-update report: one availability bool
// plus one optional payload pointer per updater. It follows the convention
// HostImageStatus uses (a per-source availability bool beside its detail)
// rather than mirroring that type's shape, which is flat and per-source rather
// than keyed by updater.
//
// BootcConfigured / RPMOStreeConfigured are driven by the AutoupdateBootc /
// AutoupdateRPMOStree capabilities, so a payload pointer is non-nil exactly
// when its updater is configured. The zero AutoUpdateStatus -- both bools false
// and both pointers nil -- is the canonical "no automatic updater is
// configured" state, which is a valid, reportable answer on an image-based
// host, not an error and not an empty response.
//
// The payload pointers use omitempty exactly as HostImageStatus's deployment
// slots do: an unconfigured updater contributes no key at all, so the wire form
// can never be read as "configured but with everything zeroed."
type AutoUpdateStatus struct {
	Bootc               *BootcAutoUpdate     `json:"bootc,omitempty"`
	BootcConfigured     bool                 `json:"bootc_configured"`
	RPMOStree           *RPMOStreeAutoUpdate `json:"rpm_ostree,omitempty"`
	RPMOStreeConfigured bool                 `json:"rpm_ostree_configured"`
}

// NormalizeBootcAutoUpdatePolicy classifies bootc's automatic-update behavior
// from nothing but the drop-in *paths* systemd reports for the
// bootc-fetch-apply-updates service and timer units.
//
// It runs nothing and reads nothing: both arguments are supplied by the caller
// that talks to systemd, and only the length of each slice is consulted. The
// contents of the drop-in files are never opened, and the units' start command
// line is never read or inspected -- deriving policy from it is forbidden, and
// a mechanical test pins that ban at the source-text level for this file.
//
// The rule, in full:
//
//   - No drop-in on either unit -> BootcPolicyApply. Nothing local overrode the
//     units, so the host is provably running bootc's shipped defaults, whose
//     behavior upstream documents as fetch-and-apply. This is an inference from
//     the *absence* of any override, not from the shipped unit's contents.
//   - A drop-in on the service, the timer, or both -> BootcPolicyCustomUnknown.
//     An administrator changed something; what they changed cannot be
//     determined from a path.
//
// BootcPolicyStageOnly is never returned. See docs/autoupdate.md for why, and
// for the upstream citations behind the apply default.
//
// The returned booleans are exactly len(slice) > 0 for the service and the
// timer respectively, reported independently so callers can say which unit was
// customized. A nil slice and an empty slice mean the same thing: no drop-ins.
func NormalizeBootcAutoUpdatePolicy(serviceDropInPaths, timerDropInPaths []string) (policy string, serviceDropinsPresent, timerDropinsPresent bool) {
	serviceDropinsPresent = len(serviceDropInPaths) > 0
	timerDropinsPresent = len(timerDropInPaths) > 0
	if serviceDropinsPresent || timerDropinsPresent {
		return BootcPolicyCustomUnknown, serviceDropinsPresent, timerDropinsPresent
	}
	return BootcPolicyApply, serviceDropinsPresent, timerDropinsPresent
}
