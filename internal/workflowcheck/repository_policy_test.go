package workflowcheck

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type repositoryPolicy struct {
	Version       int                    `yaml:"version"`
	RequiredFiles []string               `yaml:"required_files"`
	RequiredGlobs []repositoryPolicyGlob `yaml:"required_globs"`
}

type repositoryPolicyGlob struct {
	Pattern string `yaml:"pattern"`
	Minimum int    `yaml:"minimum"`
}

func loadRepositoryPolicy(path string) (repositoryPolicy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return repositoryPolicy{}, fmt.Errorf("read policy: %w", err)
	}

	var policy repositoryPolicy
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return repositoryPolicy{}, fmt.Errorf("decode policy: %w", err)
	}
	return policy, nil
}

func validatePolicyPath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return fmt.Errorf("path must be non-empty and repository-relative: %q", path)
	}
	cleaned := filepath.Clean(path)
	if cleaned != path {
		return fmt.Errorf("path must be canonical: %q", path)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must stay within the repository: %q", path)
	}
	return nil
}

func evaluateRepositoryPolicy(root string, policy repositoryPolicy) ([]string, error) {
	if policy.Version != 1 {
		return nil, fmt.Errorf("unsupported policy version %d", policy.Version)
	}
	if len(policy.RequiredFiles) == 0 {
		return nil, fmt.Errorf("required_files must not be empty")
	}

	var violations []string
	seenFiles := make(map[string]struct{}, len(policy.RequiredFiles))
	for _, requiredFile := range policy.RequiredFiles {
		if err := validatePolicyPath(requiredFile); err != nil {
			return nil, fmt.Errorf("required_files: %w", err)
		}
		if _, exists := seenFiles[requiredFile]; exists {
			return nil, fmt.Errorf("required_files contains duplicate %q", requiredFile)
		}
		seenFiles[requiredFile] = struct{}{}

		info, err := os.Stat(filepath.Join(root, requiredFile))
		if err != nil {
			if os.IsNotExist(err) {
				violations = append(violations, fmt.Sprintf("required file is missing: %s", requiredFile))
				continue
			}
			return nil, fmt.Errorf("inspect required file %q: %w", requiredFile, err)
		}
		if !info.Mode().IsRegular() {
			violations = append(violations, fmt.Sprintf("required path is not a regular file: %s", requiredFile))
		}
	}

	for index, requirement := range policy.RequiredGlobs {
		if err := validatePolicyPath(requirement.Pattern); err != nil {
			return nil, fmt.Errorf("required_globs[%d]: %w", index, err)
		}
		if requirement.Minimum < 1 {
			return nil, fmt.Errorf("required_globs[%d].minimum must be positive", index)
		}

		matches, err := filepath.Glob(filepath.Join(root, requirement.Pattern))
		if err != nil {
			return nil, fmt.Errorf("required_globs[%d]: invalid pattern: %w", index, err)
		}
		regularFiles := 0
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return nil, fmt.Errorf("inspect glob match %q: %w", match, err)
			}
			if info.Mode().IsRegular() {
				regularFiles++
			}
		}
		if regularFiles < requirement.Minimum {
			violations = append(
				violations,
				fmt.Sprintf(
					"%s matched %d regular files; minimum is %d",
					requirement.Pattern,
					regularFiles,
					requirement.Minimum,
				),
			)
		}
	}

	return violations, nil
}

func TestRepositoryPolicy(t *testing.T) {
	root := filepath.Join("..", "..")
	policy, err := loadRepositoryPolicy(filepath.Join(root, "policies", "repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	violations, err := evaluateRepositoryPolicy(root, policy)
	if err != nil {
		t.Fatalf("evaluate repository policy: %v", err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}

func TestRepositoryPolicyEvaluatorReportsViolations(t *testing.T) {
	root := t.TempDir()
	policy := repositoryPolicy{
		Version:       1,
		RequiredFiles: []string{"AGENTS.md"},
		RequiredGlobs: []repositoryPolicyGlob{
			{Pattern: ".github/workflows/*.yml", Minimum: 1},
		},
	}

	violations, err := evaluateRepositoryPolicy(root, policy)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	want := []string{
		"required file is missing: AGENTS.md",
		".github/workflows/*.yml matched 0 regular files; minimum is 1",
	}
	if strings.Join(violations, "\n") != strings.Join(want, "\n") {
		t.Fatalf("violations = %q, want %q", violations, want)
	}
}

func TestRepositoryPolicyEvaluatorRejectsEscapingPaths(t *testing.T) {
	policy := repositoryPolicy{
		Version:       1,
		RequiredFiles: []string{"../outside"},
	}

	_, err := evaluateRepositoryPolicy(t.TempDir(), policy)
	if err == nil || !strings.Contains(err.Error(), "must stay within the repository") {
		t.Fatalf("error = %v, want repository-boundary error", err)
	}
}
