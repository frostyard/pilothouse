package sysext

import "context"

// ExtensionsSource is the read-only extensions seam: everything that needs the
// host's extension picture -- cmd/pilothoused's registerExtensions, which
// serves broker.QueryExtensionsState from it, and maintenance.SystemManager,
// which derives its merged-but-disabled reboot reason from it -- depends on
// this interface rather than on the concrete manager, exactly as the daemon
// depends on maintenance.HostImageSource for host-image reporting. It exposes
// no mutation of any kind, by construction: the four sysext lifecycle actions
// keep using the separate Manager interface, which this one deliberately does
// not embed.
//
// The interface (and every type it names) lives here, in the web module's own
// package, while its only production implementation lives in the
// extctl subpackage. That split is what keeps this package free of os/exec:
// the web binary links internal/modules/sysext and never links
// internal/modules/sysext/extctl, so the unprivileged process has no
// compiled-in path to updex or systemd-sysext at all.
//
// updexAvailable and sysextAvailable are the probed capability facts
// (capability.Updex / capability.Sysext) the caller threads in, so a source
// whose tool the host does not have is never attempted.
type ExtensionsSource interface {
	State(ctx context.Context, updexAvailable, sysextAvailable bool) (ExtensionsState, error)
}

// ExtensionsState is the aggregate extension inventory broker.QueryExtensionsState
// returns: the union of updex-managed extension definitions, extensions
// installed per `systemd-sysext list`, and extensions merged per
// `systemd-sysext status`, plus one availability/error pair per source.
//
// The availability/error pairs follow maintenance.HostImageStatus's flat
// per-source convention (BootcAvailable/BootcError beside
// RPMOStreeAvailable/RPMOStreeError) rather than AutoUpdateStatus's
// *_configured convention: the question here is "did this source answer",
// not "is this source configured". UpdexAvailable/UpdexError cover the updex
// side (extension definitions plus pending-update checks) and
// SysextAvailable/SysextError cover the systemd-sysext side (both `list` and
// `status`), because each pair is exactly one degradation unit.
//
// Extensions is never nil on a successful read: a host whose tools are
// present but which has no definitions, no installed images, and nothing
// merged reports both sources available and an empty slice -- the empty
// success state, which is a different fact from a source that failed.
type ExtensionsState struct {
	Extensions      []Extension `json:"extensions"`
	SysextAvailable bool        `json:"sysext_available"`
	SysextError     string      `json:"sysext_error,omitempty"`
	UpdexAvailable  bool        `json:"updex_available"`
	UpdexError      string      `json:"updex_error,omitempty"`
}

// Extension is one entry of the union inventory, keyed by extension name.
//
// Managed reports that updex enumerated a definition for this name, so the
// extension retains enable/disable/update/refresh; an extension that is
// installed or merged without a definition is read-only. Installed reports
// presence in `systemd-sysext list` and Merged presence in `systemd-sysext
// status`. Description and Enabled come only from updex; Path and Version come
// only from systemd-sysext, so a source that failed (or was never attempted)
// simply leaves its own fields zeroed.
//
// Updates carries the pending component updates updex's check reported for
// this extension, matched by AvailableUpdate.Extension. It is empty for an
// unmanaged extension in every case -- the check only ever reports on
// definitions updex itself enumerated -- and empty for a managed extension
// with nothing pending.
type Extension struct {
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	Installed   bool              `json:"installed"`
	Managed     bool              `json:"managed"`
	Merged      bool              `json:"merged"`
	Name        string            `json:"name"`
	Path        string            `json:"path,omitempty"`
	Updates     []AvailableUpdate `json:"updates,omitempty"`
	Version     string            `json:"version,omitempty"`
}

// AvailableUpdate is one pending component update updex reported for one
// extension. Extension names the owning extension (updex reports it under its
// own "feature" vocabulary), which is redundant with the enclosing
// Extension.Name inside Extension.Updates but is what keeps a row
// self-describing once the Extensions page flattens every extension's Updates
// into one table.
type AvailableUpdate struct {
	Extension string
	Component string
	Current   string
	Newest    string
}
