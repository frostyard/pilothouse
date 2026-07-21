package sysext

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

type Feature struct {
	Definition  string
	Description string
	Enabled     bool
	Installed   bool
	Merged      bool
	Name        string
	Path        string
	Version     string
}

type AvailableUpdate struct {
	Feature   string
	Component string
	Current   string
	Newest    string
}

type Manager interface {
	Check(context.Context) ([]AvailableUpdate, error)
	Disable(context.Context, string) error
	Enable(context.Context, string) error
	List(context.Context) ([]Feature, error)
	Refresh(context.Context) error
	Update(context.Context) error
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
	if definitionsRoot == "" {
		definitionsRoot = "/usr/lib"
	}
	if updex == "" {
		updex = "updex"
	}
	return &SystemManager{definitionsRoot: definitionsRoot, runner: runner, updex: updex}
}

func (m *SystemManager) Check(ctx context.Context) ([]AvailableUpdate, error) {
	directories, err := m.definitionDirectories()
	if err != nil {
		return nil, err
	}
	var updates []AvailableUpdate
	for _, directory := range directories {
		output, runErr := m.runner.Run(ctx, m.updex, "-C", directory, "--json", "features", "check")
		if runErr != nil {
			return nil, runErr
		}
		parsed, parseErr := parseUpdexCheck(output)
		if parseErr != nil {
			return nil, fmt.Errorf("parse update check in %s: %w", directory, parseErr)
		}
		updates = append(updates, parsed...)
	}
	slices.SortFunc(updates, func(a, b AvailableUpdate) int {
		if order := strings.Compare(a.Feature, b.Feature); order != 0 {
			return order
		}
		return strings.Compare(a.Component, b.Component)
	})
	return updates, nil
}

func (m *SystemManager) Disable(ctx context.Context, name string) error {
	feature, err := m.featureFor(ctx, name)
	if err != nil {
		return err
	}
	args := []string{"-C", feature.Definition, "--json", "features", "disable", name, "--now"}
	if feature.Merged {
		args = append(args, "--force")
	}
	_, err = m.runner.Run(ctx, m.updex, args...)
	return err
}

func (m *SystemManager) Enable(ctx context.Context, name string) error {
	feature, err := m.featureFor(ctx, name)
	if err != nil {
		return err
	}
	_, err = m.runner.Run(ctx, m.updex, "-C", feature.Definition, "--json", "features", "enable", name, "--now")
	return err
}

func (m *SystemManager) List(ctx context.Context) ([]Feature, error) {
	directories, err := m.definitionDirectories()
	if err != nil {
		return nil, err
	}
	featuresByName := map[string]Feature{}
	for _, directory := range directories {
		output, runErr := m.runner.Run(ctx, m.updex, "-C", directory, "--json", "features", "list")
		if runErr != nil {
			return nil, runErr
		}
		parsed, parseErr := parseUpdexFeatures(output)
		if parseErr != nil {
			return nil, fmt.Errorf("parse features in %s: %w", directory, parseErr)
		}
		for _, feature := range parsed {
			featuresByName[feature.Name] = Feature{
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
	features := make([]Feature, 0, len(featuresByName))
	for name, feature := range featuresByName {
		if extension, ok := installed[name]; ok {
			feature.Installed = true
			feature.Path = extension.Path
			feature.Version = extensionVersion(name, extension.Path)
		}
		feature.Merged = merged[name]
		features = append(features, feature)
	}
	slices.SortFunc(features, func(a, b Feature) int { return strings.Compare(a.Name, b.Name) })
	return features, nil
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
		if _, err := m.runner.Run(ctx, m.updex, "-C", directory, "--json", "features", "update"); err != nil {
			return err
		}
	}
	return nil
}

func (m *SystemManager) definitionDirectories() ([]string, error) {
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

func (m *SystemManager) featureFor(ctx context.Context, name string) (Feature, error) {
	features, err := m.List(ctx)
	if err != nil {
		return Feature{}, err
	}
	for _, feature := range features {
		if feature.Name == name {
			return feature, nil
		}
	}
	return Feature{}, fmt.Errorf("unknown extension %q", name)
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

func parseUpdexCheck(output []byte) ([]AvailableUpdate, error) {
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
		var updates []AvailableUpdate
		for _, check := range checks {
			for _, result := range check.Results {
				if result.UpdateAvailable {
					updates = append(updates, AvailableUpdate{
						Feature:   check.Feature,
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
