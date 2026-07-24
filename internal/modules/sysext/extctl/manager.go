// Package extctl is the privileged, exec-backed implementation of the
// Extensions module's two seams: sysext.Manager (the four lifecycle
// mutations) and sysext.ExtensionsSource (the aggregate inventory read).
// Everything here shells out to `updex` and `systemd-sysext`, so it is
// linked only by cmd/pilothoused, the process that legitimately runs those
// tools as root.
//
// It is a separate package from internal/modules/sysext on purpose. The web
// module in the parent package used to construct a SystemManager of its own
// and run these commands inside the unprivileged web process; #52 moved
// every read behind broker.QueryExtensionsState, and moving the exec code
// down here is what makes that structural rather than conventional --
// package sysext now imports neither os/exec nor any CommandRunner, so the
// web binary has no compiled-in path to a host tool even by accident. The
// dependency runs one way only: extctl imports sysext for the shared types,
// never the reverse.
package extctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/frostyard/pilothouse/internal/modules/sysext"
)

// The concrete manager must keep satisfying both seams: registerSysextActions
// depends on the mutating one and registerExtensions on the read-only one.
var (
	_ sysext.ExtensionsSource = (*SystemManager)(nil)
	_ sysext.Manager          = (*SystemManager)(nil)
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "SYSTEMD_PAGER=cat")
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return output, fmt.Errorf("%s: %s", filepath.Base(name), message)
	}
	return output, nil
}

// managedExtension is one updex-managed extension as `updex features list`
// reports it, enriched with the systemd-sysext facts list() also collects.
// It is unexported and internal to the enable/disable path: the exported
// inventory type is sysext.Extension, which State builds, so this package
// keeps no second exported extension vocabulary of its own.
//
// Definition is the definition directory the extension was found in, so a
// follow-up updex invocation can be scoped to the same `-C` root.
type managedExtension struct {
	Definition  string
	Description string
	Enabled     bool
	Installed   bool
	Merged      bool
	Name        string
	Path        string
	Version     string
}

type SystemManager struct {
	definitionsRoot string
	runner          CommandRunner
	updex           string
}

type updexFeature struct {
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Name        string `json:"name"`
	Source      string `json:"source"`
}

type updexCheck struct {
	Feature string `json:"feature"`
	Results []struct {
		Component       string `json:"component"`
		CurrentVersion  string `json:"current_version"`
		NewestVersion   string `json:"newest_version"`
		UpdateAvailable bool   `json:"update_available"`
	} `json:"results"`
}

type installedExtension struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type mergeStatus struct {
	Extensions json.RawMessage `json:"extensions"`
	Hierarchy  string          `json:"hierarchy"`
}

func NewSystemManager(runner CommandRunner, definitionsRoot, updex string) *SystemManager {
	if updex == "" {
		updex = "updex"
	}
	return &SystemManager{definitionsRoot: definitionsRoot, runner: runner, updex: updex}
}

// Check reports every pending component update updex knows about, across
// every definition directory. It is updex-only by construction, which is why
// sysext.Extension.Updates is empty on a host without updex and the
// Extensions page's update surfaces need no capability flag of their own.
func (m *SystemManager) Check(ctx context.Context) ([]sysext.AvailableUpdate, error) {
	directories, err := m.definitionDirectories()
	if err != nil {
		return nil, err
	}
	var updates []sysext.AvailableUpdate
	for _, directory := range directories {
		output, runErr := m.runner.Run(ctx, m.updex, m.updexArgs(directory, "--json", "features", "check")...)
		if runErr != nil {
			return nil, runErr
		}
		parsed, parseErr := parseUpdexCheck(output)
		if parseErr != nil {
			return nil, fmt.Errorf("parse update check in %s: %w", definitionScope(directory), parseErr)
		}
		updates = append(updates, parsed...)
	}
	slices.SortFunc(updates, func(a, b sysext.AvailableUpdate) int {
		if order := strings.Compare(a.Extension, b.Extension); order != 0 {
			return order
		}
		return strings.Compare(a.Component, b.Component)
	})
	return updates, nil
}

func (m *SystemManager) Disable(ctx context.Context, name string) error {
	extension, err := m.managedExtensionFor(ctx, name)
	if err != nil {
		return err
	}
	args := m.updexArgs(extension.Definition, "--json", "features", "disable", name, "--now")
	if extension.Merged {
		args = append(args, "--force")
	}
	_, err = m.runner.Run(ctx, m.updex, args...)
	return err
}

func (m *SystemManager) Enable(ctx context.Context, name string) error {
	extension, err := m.managedExtensionFor(ctx, name)
	if err != nil {
		return err
	}
	_, err = m.runner.Run(ctx, m.updex, m.updexArgs(extension.Definition, "--json", "features", "enable", name, "--now")...)
	return err
}

// list enumerates the updex-managed extensions across every definition
// directory and enriches each with its systemd-sysext installed/merged state.
// It is unexported because its only caller is managedExtensionFor: the
// broker-facing inventory read is State (state.go), which builds the union
// including extensions updex knows nothing about.
func (m *SystemManager) list(ctx context.Context) ([]managedExtension, error) {
	directories, err := m.definitionDirectories()
	if err != nil {
		return nil, err
	}
	byName := map[string]managedExtension{}
	for _, directory := range directories {
		output, runErr := m.runner.Run(ctx, m.updex, m.updexArgs(directory, "--json", "features", "list")...)
		if runErr != nil {
			return nil, runErr
		}
		parsed, parseErr := parseUpdexFeatures(output)
		if parseErr != nil {
			return nil, fmt.Errorf("parse features in %s: %w", definitionScope(directory), parseErr)
		}
		for _, feature := range parsed {
			byName[feature.Name] = managedExtension{
				Definition:  directory,
				Description: feature.Description,
				Enabled:     feature.Enabled,
				Name:        feature.Name,
			}
		}
	}
	installed, err := m.installed(ctx)
	if err != nil {
		return nil, err
	}
	merged, err := m.merged(ctx)
	if err != nil {
		return nil, err
	}
	extensions := make([]managedExtension, 0, len(byName))
	for name, extension := range byName {
		if image, ok := installed[name]; ok {
			extension.Installed = true
			extension.Path = image.Path
			extension.Version = extensionVersion(name, image.Path)
		}
		extension.Merged = merged[name]
		extensions = append(extensions, extension)
	}
	slices.SortFunc(extensions, func(a, b managedExtension) int { return strings.Compare(a.Name, b.Name) })
	return extensions, nil
}

func (m *SystemManager) Refresh(ctx context.Context) error {
	_, err := m.runner.Run(ctx, "systemd-sysext", "refresh", "--no-pager")
	return err
}

func (m *SystemManager) Update(ctx context.Context) error {
	directories, err := m.definitionDirectories()
	if err != nil {
		return err
	}
	for _, directory := range directories {
		if _, err := m.runner.Run(ctx, m.updex, m.updexArgs(directory, "--json", "features", "update")...); err != nil {
			return err
		}
	}
	return nil
}

func (m *SystemManager) definitionDirectories() ([]string, error) {
	if m.definitionsRoot == "" {
		// Let updex apply the standard /etc, /run, /usr/local, and /usr
		// precedence across both legacy and component-scoped definitions.
		return []string{""}, nil
	}
	patterns := []string{
		filepath.Join(m.definitionsRoot, "sysupdate.d"),
		filepath.Join(m.definitionsRoot, "sysupdate.*.d"),
	}
	seen := map[string]bool{}
	var directories []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("discover sysupdate definitions: %w", err)
		}
		for _, match := range matches {
			featureFiles, globErr := filepath.Glob(filepath.Join(match, "*.feature"))
			if globErr != nil {
				return nil, fmt.Errorf("discover feature definitions: %w", globErr)
			}
			if len(featureFiles) > 0 && !seen[match] {
				seen[match] = true
				directories = append(directories, match)
			}
		}
	}
	slices.Sort(directories)
	return directories, nil
}

func (m *SystemManager) updexArgs(directory string, args ...string) []string {
	if directory == "" {
		return args
	}
	return append([]string{"-C", directory}, args...)
}

func definitionScope(directory string) string {
	if directory == "" {
		return "standard search paths"
	}
	return directory
}

func (m *SystemManager) managedExtensionFor(ctx context.Context, name string) (managedExtension, error) {
	extensions, err := m.list(ctx)
	if err != nil {
		return managedExtension{}, err
	}
	for _, extension := range extensions {
		if extension.Name == name {
			return extension, nil
		}
	}
	return managedExtension{}, fmt.Errorf("unknown extension %q", name)
}

func (m *SystemManager) installed(ctx context.Context) (map[string]installedExtension, error) {
	output, err := m.runner.Run(ctx, "systemd-sysext", "list", "--json=short", "--no-pager")
	if err != nil {
		return nil, err
	}
	var extensions []installedExtension
	if err := json.Unmarshal(output, &extensions); err != nil {
		return nil, fmt.Errorf("parse systemd-sysext list: %w", err)
	}
	result := make(map[string]installedExtension, len(extensions))
	for _, extension := range extensions {
		result[extension.Name] = extension
	}
	return result, nil
}

func (m *SystemManager) merged(ctx context.Context) (map[string]bool, error) {
	output, err := m.runner.Run(ctx, "systemd-sysext", "status", "--json=short", "--no-pager")
	if err != nil {
		return nil, err
	}
	var statuses []mergeStatus
	if err := json.Unmarshal(output, &statuses); err != nil {
		return nil, fmt.Errorf("parse systemd-sysext status: %w", err)
	}
	result := map[string]bool{}
	for _, status := range statuses {
		var names []string
		if err := json.Unmarshal(status.Extensions, &names); err == nil {
			for _, name := range names {
				result[name] = true
			}
		}
	}
	return result, nil
}

func extensionVersion(name, path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		target = path
	}
	base := filepath.Base(target)
	value, ok := strings.CutPrefix(base, name+"_")
	if !ok {
		return "installed"
	}
	for _, suffix := range []string{".raw.zst", ".raw.xz", ".raw.gz", ".raw"} {
		value = strings.TrimSuffix(value, suffix)
	}
	parts := strings.Split(value, "_")
	if len(parts) > 2 {
		parts = parts[:len(parts)-2]
	}
	value = strings.Join(parts, "_")
	if value == "" {
		return "installed"
	}
	return value
}

func parseUpdexFeatures(output []byte) ([]updexFeature, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	for decoder.More() {
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var features []updexFeature
			if err := json.Unmarshal(trimmed, &features); err != nil {
				return nil, err
			}
			return features, nil
		}
	}
	return nil, fmt.Errorf("feature array missing from updex output")
}

func parseUpdexCheck(output []byte) ([]sysext.AvailableUpdate, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("update check array missing from updex output")
			}
			return nil, err
		}
		trimmed := bytes.TrimSpace(value)
		if bytes.Equal(trimmed, []byte("null")) {
			return nil, nil
		}
		if len(trimmed) == 0 || trimmed[0] != '[' {
			continue
		}
		var checks []updexCheck
		if err := json.Unmarshal(trimmed, &checks); err != nil {
			return nil, err
		}
		var updates []sysext.AvailableUpdate
		for _, check := range checks {
			for _, result := range check.Results {
				if result.UpdateAvailable {
					updates = append(updates, sysext.AvailableUpdate{
						Extension: check.Feature,
						Component: result.Component,
						Current:   result.CurrentVersion,
						Newest:    result.NewestVersion,
					})
				}
			}
		}
		return updates, nil
	}
}
