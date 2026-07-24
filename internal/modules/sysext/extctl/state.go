package extctl

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/frostyard/pilothouse/internal/modules/sysext"
)

// State reports the union extension inventory as two independently degrading
// sources. It is the production implementation of sysext.ExtensionsSource,
// which is what cmd/pilothoused's registerExtensions serves
// broker.QueryExtensionsState from.
//
// Each source is attempted only when its capability flag says the tool is
// present: "never attempted" leaves that source's Available false and its
// Error empty, which stays distinguishable from "attempted and failed". A
// source that is attempted runs all of its own sub-calls as one unit -- the
// updex side runs the per-definition-directory extension list and then the
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
func (m *SystemManager) State(ctx context.Context, updexAvailable, sysextAvailable bool) (sysext.ExtensionsState, error) {
	state := sysext.ExtensionsState{Extensions: []sysext.Extension{}}
	extensions := map[string]sysext.Extension{}

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
	slices.SortFunc(state.Extensions, func(a, b sysext.Extension) int { return strings.Compare(a.Name, b.Name) })
	return state, nil
}

// updexInventory is the updex degradation unit: the extension definitions
// updex enumerates across every definition directory, plus the pending updates
// its check reports for those same definitions, indexed by extension name.
// Both sub-calls invoke updex and nothing else, which is what makes them one
// unit; either one failing fails the whole unit, and the returned error is the
// first failure's.
//
// It deliberately reuses SystemManager's existing private helpers
// (definitionDirectories, updexArgs, definitionScope, parseUpdexFeatures) and
// the existing Check method rather than re-deriving any of them, so there is
// exactly one place in this package that knows how updex is invoked.
func (m *SystemManager) updexInventory(ctx context.Context) (map[string]sysext.Extension, map[string][]sysext.AvailableUpdate, error) {
	directories, err := m.definitionDirectories()
	if err != nil {
		return nil, nil, err
	}
	managed := map[string]sysext.Extension{}
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
			managed[feature.Name] = sysext.Extension{
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
	updates := map[string][]sysext.AvailableUpdate{}
	for _, update := range available {
		if _, isManaged := managed[update.Extension]; !isManaged {
			continue
		}
		updates[update.Extension] = append(updates[update.Extension], update)
	}
	return managed, updates, nil
}

// sysextInventory is the systemd-sysext degradation unit: the installed
// extension images and the merged-extension set, read through the same private
// installed()/merged() helpers the enable/disable path uses. Both sub-calls
// invoke systemd-sysext and nothing else, so they degrade together.
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
