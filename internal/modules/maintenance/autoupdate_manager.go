package maintenance

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"time"
)

// AutoUpdateManager is the daemon-side reader that turns the two probed
// Autoupdate* capability booleans, live systemd unit reads, and one fixed
// configuration-file read into an AutoUpdateStatus. It is the only place in
// the tree where automatic-update reporting performs I/O; the classification
// math itself stays in this package's pure functions
// (NormalizeBootcAutoUpdatePolicy in autoupdate.go and
// ParseRPMOStreeAutomaticUpdatePolicy in autoupdate_rpmostree.go).
//
// Nothing here is cached. Status re-reads systemd and rpm-ostree's
// configuration file on every single call: there is no cached field, no
// sync.Once, no TTL, and no compute-once-at-construction step anywhere in this
// file. Two callers holding the same *AutoUpdateManager therefore each trigger
// an independent set of D-Bus reads and an independent os.ReadFile, and their
// two results are neither guaranteed identical nor atomic with each other.
// Documentation must describe it that way and must not claim the two share a
// single parse.
//
// The manager is read-only by construction. It reaches systemd through exactly
// two property getters and the host filesystem through exactly one os.ReadFile
// of one fixed path; it runs no command, invokes no bootc or rpm-ostree
// subcommand, and exposes no mutation of any kind. Mechanical tests in
// autoupdate_manager_test.go pin all three of those claims at the source-text
// and AST level.
type AutoUpdateManager struct {
	bootcConfigured     bool
	client              systemdClient
	root                string
	rpmOStreeConfigured bool
}

// AutoUpdateSource is the read-only automatic-update seam, exactly parallel to
// HostImageSource: everything that needs the host's automatic-update picture --
// today only cmd/pilothoused's registerAutoUpdate, which serves
// broker.QueryAutoUpdateStatus from it -- depends on this interface rather than
// on the concrete *AutoUpdateManager. It exposes no mutation of any kind, by
// construction, and *AutoUpdateManager satisfies it.
type AutoUpdateSource interface {
	Status(context.Context) (AutoUpdateStatus, error)
}

var _ AutoUpdateSource = (*AutoUpdateManager)(nil)

// The exact updater unit allowlist. These four unit names are the complete set
// of systemd units this package ever asks about, and they are the same four
// internal/capability's systemd probe keys AutoupdateBootc and
// AutoupdateRPMOStree on, so "the capability is present" and "these are the
// units we read" can never drift apart.
const (
	bootcAutoUpdateServiceUnit     = "bootc-fetch-apply-updates.service"
	bootcAutoUpdateTimerUnit       = "bootc-fetch-apply-updates.timer"
	rpmOStreeAutoUpdateServiceUnit = "rpm-ostreed-automatic.service"
	rpmOStreeAutoUpdateTimerUnit   = "rpm-ostreed-automatic.timer"
)

// rpmOStreeConfigFile is the one and only path this file reads, relative to
// the manager's root. It is rpm-ostree's daemon configuration, whose loader
// (src/daemon/rpmostreed-daemon.cxx) reads RPMOSTREED_CONF ==
// SYSCONFDIR "/rpm-ostreed.conf" and nothing else -- no conf.d merge
// directory, so one file is the complete input. See docs/autoupdate.md.
const rpmOStreeConfigFile = "etc/rpm-ostreed.conf"

// The systemd unit and unit-type property names read here. They are systemd's
// own spellings, and deliberately the same ones internal/modules/backups reads
// for its timers, so both surfaces name the same facts identically.
//
// backups also reads LastTriggerUSec for its last-run health math; this
// manager does not, because AutoUpdateStatus's payload types carry no last-run
// field for it to populate. Adding one is a schema change, not a read this
// file may quietly start making.
const (
	activeStateProperty     = "ActiveState"
	dropInPathsProperty     = "DropInPaths"
	nextElapseUSecProperty  = "NextElapseUSecRealtime"
	resultProperty          = "Result"
	serviceUnitTypeProperty = "Service"
	timerUnitTypeProperty   = "Timer"
	unitFileStateProperty   = "UnitFileState"
)

// systemdClient is the narrow read-only systemd seam this manager depends on.
// It is structurally identical to internal/modules/backups's own unexported
// systemdClient, on purpose: a real *dbus.Conn satisfies both, and a test fake
// satisfies both, without either package importing the other's interface type
// or growing a shared systemd package neither needs.
//
// Only property getters appear here. There is no start, stop, restart,
// enable, disable, reload, or job method on this interface, so no caller of
// this manager can reach one -- the read-only guarantee is a property of the
// type, not of reviewer diligence.
type systemdClient interface {
	GetUnitPropertiesContext(context.Context, string) (map[string]any, error)
	GetUnitTypePropertiesContext(context.Context, string, string) (map[string]any, error)
}

// NewAutoUpdateManager builds the automatic-update reporter.
//
// client may be nil: a host with no reachable systemd session still gets a
// manager, it simply reports every systemd-sourced field as zero (see Status).
// bootcConfigured and rpmOStreeConfigured are the probed capability facts
// (capability.AutoupdateBootc / capability.AutoupdateRPMOStree) threaded in
// from cmd/pilothoused's startup probe -- per the spec, per-updater gating is
// capability-driven, never a live re-check made here. root mirrors
// SystemManager's root/path() pattern and defaults to "/" when empty, so a
// test can point the rpm-ostreed.conf read at a temporary directory instead of
// the host's real /etc.
//
// Construction touches neither disk nor D-Bus: every read is deferred to
// Status, so a manager may always be built regardless of what the host has.
func NewAutoUpdateManager(client systemdClient, bootcConfigured, rpmOStreeConfigured bool, root string) *AutoUpdateManager {
	if root == "" {
		root = "/"
	}
	return &AutoUpdateManager{
		bootcConfigured:     bootcConfigured,
		client:              client,
		root:                root,
		rpmOStreeConfigured: rpmOStreeConfigured,
	}
}

// Status reports how each configured automatic updater is set up: its timer's
// active and unit-file state, its next scheduled trigger, its service's active
// state and last result, its normalized policy, and the two independent
// drop-in-presence booleans.
//
// Every call re-reads the host. There is no cache: see the type's own doc
// comment above.
//
// Configuration gating is capability-driven. An updater whose flag is false is
// never asked about at all -- no D-Bus call naming its units is made, and its
// payload pointer stays nil -- so the zero AutoUpdateStatus is the honest
// report for a host with no updater configured. An updater whose flag is true
// always gets a non-nil payload pointer, even when every read behind it fails:
// "configured but currently unreadable" and "not configured" are different
// facts and stay distinguishable, exactly as HostImageManager.Status keeps
// "never attempted" distinct from "attempted and failed."
//
// Failure handling is per-field. Any individual D-Bus or file read that fails
// degrades exactly the field or fields it feeds to their zero value; it never
// drops the whole payload and never becomes Status's own error. Policy falls
// back to custom/unknown whenever the input needed to classify it could not be
// read, so an unreadable host is never reported as running defaults. The two
// updaters differ in what that input is: bootc's policy is inferred from
// systemd drop-in presence, so an unreadable systemd session forces it to
// custom/unknown, whereas rpm-ostree's policy comes from rpm-ostreed.conf on
// disk and is independent of systemd entirely.
//
// A nil systemd client behaves precisely as though every systemd read failed:
// all systemd-sourced fields zero, and panics nowhere. Because bootc's policy
// depends on systemd, a nil client leaves BootcAutoUpdate.Policy at
// custom/unknown. rpm-ostree's policy is not systemd-sourced, so a nil client
// does not touch it -- RPMOStreeAutoUpdate.Policy still reflects
// rpm-ostreed.conf whenever that file is readable, and only falls back to
// custom/unknown when the config read itself fails.
//
// Status therefore never returns a non-nil error today. Like
// HostImageManager.Status, the error result exists for conditions outside
// per-updater reporting, and this design has none: every failure mode it can
// encounter is already representable inside the payload it returns. Callers
// must still check the error rather than assume that stays true.
func (m *AutoUpdateManager) Status(ctx context.Context) (AutoUpdateStatus, error) {
	status := AutoUpdateStatus{
		BootcConfigured:     m.bootcConfigured,
		RPMOStreeConfigured: m.rpmOStreeConfigured,
	}
	if m.bootcConfigured {
		status.Bootc = m.bootcAutoUpdate(ctx)
	}
	if m.rpmOStreeConfigured {
		status.RPMOStree = m.rpmOStreeAutoUpdate(ctx)
	}
	return status, nil
}

// bootcAutoUpdate reads bootc's timer/service pair and classifies its policy
// from drop-in presence alone, via c1's pure normalizer.
func (m *AutoUpdateManager) bootcAutoUpdate(ctx context.Context) *BootcAutoUpdate {
	pair := m.readUnitPair(ctx, bootcAutoUpdateTimerUnit, bootcAutoUpdateServiceUnit)
	update := &BootcAutoUpdate{
		NextTrigger:        pair.nextTrigger,
		Policy:             BootcPolicyCustomUnknown,
		ServiceActiveState: pair.serviceActiveState,
		ServiceResult:      pair.serviceResult,
		TimerActiveState:   pair.timerActiveState,
		TimerUnitFileState: pair.timerUnitFileState,
	}
	// Both drop-in lists must have been read for the classification to mean
	// anything: bootc's "no drop-in anywhere means the shipped apply default"
	// inference is only sound when the absence was actually observed on both
	// units. If either list is unknown, an unread drop-in could be sitting
	// there, so the answer is custom/unknown and both booleans stay false --
	// reporting "no drop-ins" for a unit nobody could read would be a claim,
	// not a fact.
	if pair.serviceDropInsKnown && pair.timerDropInsKnown {
		update.Policy, update.ServiceDropinsPresent, update.TimerDropinsPresent = NormalizeBootcAutoUpdatePolicy(pair.serviceDropInPaths, pair.timerDropInPaths)
	}
	return update
}

// rpmOStreeAutoUpdate reads rpm-ostree's timer/service pair for its state
// fields and rpm-ostree's own daemon configuration for its policy. The two are
// independent: rpm-ostree has a native, administrator-settable policy setting,
// so unlike bootc its policy is not inferred from drop-in presence, and its
// drop-in booleans are plain presence facts that feed nothing.
func (m *AutoUpdateManager) rpmOStreeAutoUpdate(ctx context.Context) *RPMOStreeAutoUpdate {
	pair := m.readUnitPair(ctx, rpmOStreeAutoUpdateTimerUnit, rpmOStreeAutoUpdateServiceUnit)
	return &RPMOStreeAutoUpdate{
		NextTrigger:           pair.nextTrigger,
		Policy:                ParseRPMOStreeAutomaticUpdatePolicy(m.rpmOStreeConfig()),
		ServiceActiveState:    pair.serviceActiveState,
		ServiceDropinsPresent: len(pair.serviceDropInPaths) > 0,
		ServiceResult:         pair.serviceResult,
		TimerActiveState:      pair.timerActiveState,
		TimerDropinsPresent:   len(pair.timerDropInPaths) > 0,
		TimerUnitFileState:    pair.timerUnitFileState,
	}
}

// rpmOStreeConfig reads rpm-ostree's daemon configuration off disk, returning
// nil for every failure mode -- absent (os.IsNotExist), unreadable,
// permission-denied, a directory in its place.
//
// Nil is the right answer for all of them because
// ParseRPMOStreeAutomaticUpdatePolicy maps nil input to custom/unknown, which
// is exactly what the spec mandates for an absent file: deliberately not
// rpm-ostree's own "absent means none" default, because Pilothouse cannot be
// certain it observed the value the running daemon actually loaded. Returning
// partial bytes alongside an error would risk classifying a truncated read as
// a real policy, so the bytes are dropped.
func (m *AutoUpdateManager) rpmOStreeConfig() []byte {
	config, err := os.ReadFile(m.path(rpmOStreeConfigFile))
	if err != nil {
		return nil
	}
	return config
}

// path resolves a host path under the manager's root, mirroring
// SystemManager.path so tests can redirect the one file read here at a
// temporary directory.
func (m *AutoUpdateManager) path(path string) string {
	return filepath.Join(m.root, path)
}

// autoUpdateUnitPair is the raw systemd picture of one updater's timer and
// service units, before any policy classification. Both updaters read exactly
// the same property set, so they share this shape and the one reader below
// rather than duplicating the D-Bus plumbing twice.
//
// The two *DropInsKnown booleans distinguish "read the drop-in list and it was
// empty" from "could not read the drop-in list," which the path slices alone
// cannot express -- bootc's classifier treats those two very differently.
type autoUpdateUnitPair struct {
	nextTrigger         time.Time
	serviceActiveState  string
	serviceDropInPaths  []string
	serviceDropInsKnown bool
	serviceResult       string
	timerActiveState    string
	timerDropInPaths    []string
	timerDropInsKnown   bool
	timerUnitFileState  string
}

// readUnitPair performs the four property reads one updater needs and is the
// only function in this file that talks to systemd:
//
//   - GetUnitPropertiesContext(timer)            -> ActiveState, UnitFileState, DropInPaths
//   - GetUnitTypePropertiesContext(timer, Timer) -> NextElapseUSecRealtime
//   - GetUnitPropertiesContext(service)          -> ActiveState, DropInPaths
//   - GetUnitTypePropertiesContext(service, Service) -> Result
//
// Each of the four is independent: one failing leaves the fields it would have
// filled at their zero values and the other three still run, so a partly
// answering systemd still produces a partly populated, honest report. A nil
// client short-circuits all four, producing the same all-zero pair a
// four-way failure would.
func (m *AutoUpdateManager) readUnitPair(ctx context.Context, timerUnit, serviceUnit string) autoUpdateUnitPair {
	var pair autoUpdateUnitPair
	if m.client == nil {
		return pair
	}

	if properties, err := m.client.GetUnitPropertiesContext(ctx, timerUnit); err == nil {
		pair.timerActiveState, _ = autoUpdateStringProperty(properties, activeStateProperty)
		pair.timerUnitFileState, _ = autoUpdateStringProperty(properties, unitFileStateProperty)
		pair.timerDropInPaths, pair.timerDropInsKnown = autoUpdateStringsProperty(properties, dropInPathsProperty)
	}
	if properties, err := m.client.GetUnitTypePropertiesContext(ctx, timerUnit, timerUnitTypeProperty); err == nil {
		if nextElapse, ok := autoUpdateUsecProperty(properties, nextElapseUSecProperty); ok {
			pair.nextTrigger = autoUpdateUsecTime(nextElapse)
		}
	}
	if properties, err := m.client.GetUnitPropertiesContext(ctx, serviceUnit); err == nil {
		pair.serviceActiveState, _ = autoUpdateStringProperty(properties, activeStateProperty)
		pair.serviceDropInPaths, pair.serviceDropInsKnown = autoUpdateStringsProperty(properties, dropInPathsProperty)
	}
	if properties, err := m.client.GetUnitTypePropertiesContext(ctx, serviceUnit, serviceUnitTypeProperty); err == nil {
		pair.serviceResult, _ = autoUpdateStringProperty(properties, resultProperty)
	}
	return pair
}

// autoUpdateStringProperty, autoUpdateStringsProperty, autoUpdateUsecProperty,
// and autoUpdateUsecTime mirror internal/modules/backups's stringProperty,
// usecProperty, and usecTime -- including the MaxInt64 guard that keeps a
// bogus microsecond count from wrapping negative, and the "zero microseconds
// means no time at all, not the Unix epoch" rule that makes an unscheduled
// timer report the zero time.
func autoUpdateStringProperty(properties map[string]any, name string) (string, bool) {
	value, ok := properties[name].(string)
	return value, ok
}

// autoUpdateStringsProperty reads a systemd string-array property. The bool is
// the caller's "was this actually observed" signal: a present-but-empty array
// reports (empty, true) -- systemd saying the unit has no drop-ins -- while a
// missing or wrongly-typed property reports (nil, false).
func autoUpdateStringsProperty(properties map[string]any, name string) ([]string, bool) {
	value, ok := properties[name].([]string)
	return value, ok
}

func autoUpdateUsecProperty(properties map[string]any, name string) (uint64, bool) {
	value, ok := properties[name].(uint64)
	return value, ok && value <= math.MaxInt64
}

func autoUpdateUsecTime(value uint64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMicro(int64(value)).UTC()
}
