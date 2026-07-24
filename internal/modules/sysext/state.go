package sysext

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ExtensionsSource is the read-only extensions seam: everything that needs the
// host's extension picture -- today only cmd/pilothoused's registerExtensions,
// which serves broker.QueryExtensionsState from it -- depends on this
// interface rather than on the concrete manager, exactly as the daemon depends
// on maintenance.HostImageSource for host-image reporting. It exposes no
// mutation of any kind, by construction: the four sysext lifecycle actions
// keep using the separate Manager interface, which this one deliberately does
// not embed.
//
// updexAvailable and sysextAvailable are the probed capability facts
// (capability.Updex / capability.Sysext) the caller threads in, so a source
// whose tool the host does not have is never attempted.
type ExtensionsSource interface {
	State(ctx context.Context, updexAvailable, sysextAvailable bool) (ExtensionsState, error)
}

// ExtensionsState is the aggregate extension inventory broker.QueryExtensionsState
// returns: the union of updex-managed feature definitions, extensions
// installed per `systemd-sysext list`, and extensions merged per
// `systemd-sysext status`, plus one availability/error pair per source.
//
// The availability/error pairs follow maintenance.HostImageStatus's flat
// per-source convention (BootcAvailable/BootcError beside
// RPMOStreeAvailable/RPMOStreeError) rather than AutoUpdateStatus's
// *_configured convention: the question here is "did this source answer",
// not "is this source configured". UpdexAvailable/UpdexError cover the updex
// side (feature definitions plus pending-update checks) and
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
// Managed reports that updex enumerated a feature definition for this name, so
// the extension retains enable/disable/update/refresh; an extension that is
// installed or merged without a definition is read-only. Installed reports
// presence in `systemd-sysext list` and Merged presence in `systemd-sysext
// status`. Description and Enabled come only from updex; Path and Version come
// only from systemd-sysext, so a source that failed (or was never attempted)
// simply leaves its own fields zeroed.
//
// Updates carries the pending component updates updex's feature check reported
// for this extension, matched by AvailableUpdate.Feature. It is empty for an
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

// State reports the union extension inventory as two independently degrading
// sources.
//
// Each source is attempted only when its capability flag says the tool is
// present: "never attempted" leaves that source's Available false and its
// Error empty, which stays distinguishable from "attempted and failed". A
// source that is attempted runs all of its own sub-calls as one unit -- the
// updex side runs the per-definition-directory feature list and then the
// pending-update check, the systemd-sysext side runs `list` and then `status`
// -- and the first sub-call to fail sets that source's Available=false and
// Error to the failure's message, leaving the other source's data completely
// intact.
//
// Neither failure mode is propagated as State's own error, matching
// HostImageManager.Status and therefore QueryHostImageStatus's contract: a
// host where updex answers and systemd-sysext does not (or the reverse) still
// has a usable, honest report, and collapsing that into a method-level error
// would throw it away. State therefore never returns a non-nil error today --
// the error result exists for conditions outside per-source reporting, of
// which this design has none -- and callers must still check it rather than
// assume.
func (m *SystemManager) State(ctx context.Context, updexAvailable, sysextAvailable bool) (ExtensionsState, error) {
	state := ExtensionsState{Extensions: []Extension{}}
	extensions := map[string]Extension{}

	if updexAvailable {
		managed, updates, err := m.updexInventory(ctx)
		if err != nil {
			// Leave UpdexAvailable false: updex is present on the host but its
			// answer is unusable, so there is no updex-sourced data to report,
			// only a reason why.
			state.UpdexError = err.Error()
		} else {
			state.UpdexAvailable = true
			for name, extension := range managed {
				extension.Updates = updates[name]
				extensions[name] = extension
			}
		}
	}

	if sysextAvailable {
		installed, merged, err := m.sysextInventory(ctx)
		if err != nil {
			state.SysextError = err.Error()
		} else {
			state.SysextAvailable = true
			for name, image := range installed {
				entry := extensions[name]
				entry.Name = name
				entry.Installed = true
				entry.Path = image.Path
				entry.Version = extensionVersion(name, image.Path)
				extensions[name] = entry
			}
			for name, isMerged := range merged {
				if !isMerged {
					continue
				}
				entry := extensions[name]
				entry.Name = name
				entry.Merged = true
				extensions[name] = entry
			}
		}
	}

	for _, extension := range extensions {
		state.Extensions = append(state.Extensions, extension)
	}
	slices.SortFunc(state.Extensions, func(a, b Extension) int { return strings.Compare(a.Name, b.Name) })
	return state, nil
}

// updexInventory is the updex degradation unit: the feature definitions updex
// enumerates across every definition directory, plus the pending updates its
// feature check reports for those same definitions, indexed by feature name.
// Both sub-calls invoke updex and nothing else, which is what makes them one
// unit; either one failing fails the whole unit, and the returned error is the
// first failure's.
//
// It deliberately reuses SystemManager's existing private helpers
// (definitionDirectories, updexArgs, definitionScope, parseUpdexFeatures) and
// the existing Check method rather than re-deriving any of them, so there is
// exactly one place in this package that knows how updex is invoked.
func (m *SystemManager) updexInventory(ctx context.Context) (map[string]Extension, map[string][]AvailableUpdate, error) {
	directories, err := m.definitionDirectories()
	if err != nil {
		return nil, nil, err
	}
	managed := map[string]Extension{}
	for _, directory := range directories {
		output, runErr := m.runner.Run(ctx, m.updex, m.updexArgs(directory, "--json", "features", "list")...)
		if runErr != nil {
			return nil, nil, runErr
		}
		parsed, parseErr := parseUpdexFeatures(output)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("parse features in %s: %w", definitionScope(directory), parseErr)
		}
		for _, feature := range parsed {
			managed[feature.Name] = Extension{
				Description: feature.Description,
				Enabled:     feature.Enabled,
				Managed:     true,
				Name:        feature.Name,
			}
		}
	}
	available, err := m.Check(ctx)
	if err != nil {
		return nil, nil, err
	}
	updates := map[string][]AvailableUpdate{}
	for _, update := range available {
		if _, isManaged := managed[update.Feature]; !isManaged {
			continue
		}
		updates[update.Feature] = append(updates[update.Feature], update)
	}
	return managed, updates, nil
}

// sysextInventory is the systemd-sysext degradation unit: the installed
// extension images and the merged-extension set, read through the same private
// installed()/merged() helpers List already uses. Both sub-calls invoke
// systemd-sysext and nothing else, so they degrade together.
func (m *SystemManager) sysextInventory(ctx context.Context) (map[string]installedExtension, map[string]bool, error) {
	installed, err := m.installed(ctx)
	if err != nil {
		return nil, nil, err
	}
	merged, err := m.merged(ctx)
	if err != nil {
		return nil, nil, err
	}
	return installed, merged, nil
}
