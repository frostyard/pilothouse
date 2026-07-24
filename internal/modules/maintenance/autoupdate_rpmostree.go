package maintenance

import "strings"

// The normalized rpm-ostree automatic-update policy vocabulary. These five
// values are the complete, closed set RPMOStreeAutoUpdate.Policy may ever hold.
//
// It is deliberately not bootc's vocabulary: rpm-ostree has a native,
// administrator-settable automatic-update policy with four states of its own,
// so the spec's "normalize per updater -- do not force a shared enum with
// bootc" applies directly. The first four values are rpm-ostree's own; the
// fifth is Pilothouse's "we could not establish which of them is in effect"
// answer. See docs/autoupdate.md for the upstream derivation.
const (
	// RPMOStreePolicyNone means rpm-ostree performs no automatic update work.
	// It is the normalized form of rpm-ostree's own "none" and its "off" alias.
	RPMOStreePolicyNone = "none"
	// RPMOStreePolicyCheck means rpm-ostree only checks for an available
	// update and reports it; nothing is downloaded or deployed.
	RPMOStreePolicyCheck = "check"
	// RPMOStreePolicyStage means rpm-ostree downloads and stages an update for
	// the next boot without applying it live. It is the normalized form of
	// rpm-ostree's "stage" and its "ex-stage" backwards-compatibility alias.
	RPMOStreePolicyStage = "stage"
	// RPMOStreePolicyApply means rpm-ostree applies updates automatically.
	RPMOStreePolicyApply = "apply"
	// RPMOStreePolicyCustomUnknown means Pilothouse could not establish which
	// policy the daemon is actually running: the configuration was absent,
	// unreadable, carried no AutomaticUpdatePolicy key, or carried a value
	// rpm-ostree itself does not recognize.
	RPMOStreePolicyCustomUnknown = "custom/unknown"
)

// rpm-ostree's daemon configuration coordinates. The daemon reads its policy
// from exactly one group and one key -- DAEMON_CONFIG_GROUP "Daemon" and
// "AutomaticUpdatePolicy" in src/daemon/rpmostreed-daemon.cxx -- so a key of
// the same name in any other group is not the daemon's policy and must not be
// read as one.
const (
	rpmOStreeDaemonConfigGroup   = "Daemon"
	rpmOStreeAutoUpdatePolicyKey = "AutomaticUpdatePolicy"
)

// ParseRPMOStreeAutomaticUpdatePolicy normalizes the *content* of rpm-ostree's
// daemon configuration file -- conventionally /etc/rpm-ostreed.conf -- into one
// of the five RPMOStreePolicy* constants above.
//
// It runs nothing and reads nothing: the caller supplies bytes it has already
// read, and this file imports nothing that could reach a command or the
// filesystem (a mechanical import-allowlist test pins that). The unit start
// command line is never consulted; the spec forbids deriving policy from it,
// and a source-text test pins that ban for this file too.
//
// Why the config file and not "rpm-ostree status --json": upstream's JSON
// builder in src/app/rpmostree-builtin-status.cxx emits only the members
// deployments, transaction, cached-update, and update-driver.
// AutomaticUpdatePolicy is never among them -- it is surfaced only by the
// text-mode printer, which reads it as a live D-Bus property on the Sysroot
// proxy. So the spec's "(or a stable config reader)" branch is the only
// implementable path, and the daemon's own loader
// (src/daemon/rpmostreed-daemon.cxx) reads exactly one fixed path,
// RPMOSTREED_CONF == SYSCONFDIR "/rpm-ostreed.conf", through a single
// g_key_file_load_from_file call with no conf.d merge directory. A single-file
// [Daemon] AutomaticUpdatePolicy= line scan is therefore complete, and needs
// no drop-in merging and no INI-parsing dependency.
//
// The value mapping is rpm-ostree's own, from
// rpmostree_str_to_auto_update_policy in src/libpriv/rpmostree-util.cxx:
//
//   - "none" or "off"       -> RPMOStreePolicyNone
//   - "check"               -> RPMOStreePolicyCheck
//   - "stage" or "ex-stage" -> RPMOStreePolicyStage
//   - "apply"               -> RPMOStreePolicyApply
//
// Anything else normalizes to RPMOStreePolicyCustomUnknown: a value rpm-ostree
// would itself reject, a configuration carrying no AutomaticUpdatePolicy key,
// an AutomaticUpdatePolicy key that sits outside the [Daemon] group, and empty
// or nil input (which is also how the caller reports an absent or unreadable
// file). Absent deliberately does *not* normalize to rpm-ostree's own "absent
// means none" default: Pilothouse cannot be certain it observed the value the
// running daemon actually loaded, so it reports "unknown" rather than
// asserting "no automatic updates" on the strength of a file it may not have
// seen.
func ParseRPMOStreeAutomaticUpdatePolicy(config []byte) string {
	value, found := rpmOStreeDaemonConfigValue(config, rpmOStreeAutoUpdatePolicyKey)
	if !found {
		return RPMOStreePolicyCustomUnknown
	}
	// rpm-ostree compares these with g_str_equal, so the match is exact and
	// case-sensitive; "None" is not "none" to the daemon and must not be to us.
	switch value {
	case "none", "off":
		return RPMOStreePolicyNone
	case "check":
		return RPMOStreePolicyCheck
	case "stage", "ex-stage":
		return RPMOStreePolicyStage
	case "apply":
		return RPMOStreePolicyApply
	default:
		return RPMOStreePolicyCustomUnknown
	}
}

// rpmOStreeDaemonConfigValue scans an rpm-ostreed.conf body for one key inside
// the [Daemon] group, mirroring SystemManager.osVersion()'s existing
// /etc/os-release line scan rather than pulling in an INI parser.
//
// It follows the key-file behavior rpm-ostree's loader inherits from GLib for
// the small surface a one-key lookup can encounter: blank lines and #-comments
// are skipped, [Group] headers switch the active group, whitespace around the
// key name and the value is ignored, a value may itself contain "=" because the
// split is on the first one only, and a key repeated within the group takes its
// last spelling (GLib's parser replaces the earlier entry in its lookup map).
// Keys appearing before any group header belong to no group and are ignored,
// exactly as GLib rejects them.
//
// The bool distinguishes "the key is absent" from "the key is present and
// empty" -- both normalize to custom/unknown above, but only the caller of this
// helper should decide that.
func rpmOStreeDaemonConfigValue(config []byte, key string) (string, bool) {
	var (
		inDaemonGroup bool
		value         string
		found         bool
	)
	for _, line := range strings.Split(string(config), "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inDaemonGroup = trimmed[1:len(trimmed)-1] == rpmOStreeDaemonConfigGroup
			continue
		}
		if !inDaemonGroup {
			continue
		}
		name, raw, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		value = strings.TrimSpace(raw)
		found = true
	}
	return value, found
}
