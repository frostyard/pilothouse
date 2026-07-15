package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	command.Env = os.Environ()
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

type Container struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

type Image struct {
	Containers int    `json:"containers"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       uint64 `json:"size"`
}

type State struct {
	Containers []Container `json:"containers"`
	Images     []Image     `json:"images"`
	Version    string      `json:"version"`
}

type Manager interface {
	Remove(context.Context, string) error
	Restart(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context) (State, error)
	Stop(context.Context, string) error
}

type SystemManager struct {
	binary string
	runner CommandRunner
}

func NewSystemManager(runner CommandRunner, binary string) *SystemManager {
	if binary == "" {
		binary = "docker"
	}
	return &SystemManager{binary: binary, runner: runner}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	versionOutput, err := m.runner.Run(ctx, m.binary, "version", "--format", "{{json .}}")
	if err != nil {
		return State{}, err
	}
	containers, rawContainers, err := m.containers(ctx)
	if err != nil {
		return State{}, err
	}
	images, err := m.images(ctx, rawContainers)
	if err != nil {
		return State{}, err
	}
	return State{Containers: containers, Images: images, Version: parseVersion(versionOutput)}, nil
}

func (m *SystemManager) Remove(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if container.Running {
		return errors.New("stop the container before removing it")
	}
	_, err = m.runner.Run(ctx, m.binary, "container", "rm", id)
	return err
}

func (m *SystemManager) Restart(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if !container.Running {
		return errors.New("container is not running")
	}
	_, err = m.runner.Run(ctx, m.binary, "container", "restart", "--timeout", "10", id)
	return err
}

func (m *SystemManager) Start(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if container.Running {
		return errors.New("container is already running")
	}
	_, err = m.runner.Run(ctx, m.binary, "container", "start", id)
	return err
}

func (m *SystemManager) Stop(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if !container.Running {
		return errors.New("container is not running")
	}
	_, err = m.runner.Run(ctx, m.binary, "container", "stop", "--timeout", "10", id)
	return err
}

func (m *SystemManager) container(ctx context.Context, id string) (Container, error) {
	if !validContainerID(id) {
		return Container{}, errors.New("invalid container identifier")
	}
	containers, _, err := m.containers(ctx)
	if err != nil {
		return Container{}, err
	}
	for _, container := range containers {
		if container.ID == id {
			return container, nil
		}
	}
	return Container{}, errors.New("container no longer exists")
}

func (m *SystemManager) containers(ctx context.Context) ([]Container, []rawContainer, error) {
	idsOutput, err := m.runner.Run(ctx, m.binary, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return nil, nil, err
	}
	ids := uniqueLines(idsOutput)
	if len(ids) == 0 {
		return []Container{}, []rawContainer{}, nil
	}
	output, err := m.runner.Run(ctx, m.binary, append([]string{"container", "inspect"}, ids...)...)
	if err != nil {
		return nil, nil, err
	}
	var raw []rawContainer
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse docker containers: %w", err)
	}
	containers := make([]Container, 0, len(raw))
	for _, item := range raw {
		status := item.State.Status
		if !item.State.Running && status == "exited" {
			status = fmt.Sprintf("exited (%d)", item.State.ExitCode)
		}
		containers = append(containers, Container{
			ID: item.ID, Image: item.Config.Image, Name: strings.TrimPrefix(item.Name, "/"),
			Running: item.State.Running, State: item.State.Status, Status: status,
		})
	}
	slices.SortFunc(containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return containers, raw, nil
}

func (m *SystemManager) images(ctx context.Context, containers []rawContainer) ([]Image, error) {
	idsOutput, err := m.runner.Run(ctx, m.binary, "image", "ls", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := uniqueLines(idsOutput)
	if len(ids) == 0 {
		return []Image{}, nil
	}
	output, err := m.runner.Run(ctx, m.binary, append([]string{"image", "inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var raw []rawImage
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("parse docker images: %w", err)
	}
	counts := map[string]int{}
	for _, container := range containers {
		counts[container.Image]++
	}
	images := make([]Image, 0, len(raw))
	for _, item := range raw {
		images = append(images, Image{
			Containers: counts[item.ID], ID: item.ID, Name: imageName(item), Size: item.Size,
		})
	}
	slices.SortFunc(images, func(a, b Image) int { return strings.Compare(a.Name, b.Name) })
	return images, nil
}

type rawContainer struct {
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	ID    string `json:"Id"`
	Image string `json:"Image"`
	Name  string `json:"Name"`
	State struct {
		ExitCode int    `json:"ExitCode"`
		Running  bool   `json:"Running"`
		Status   string `json:"Status"`
	} `json:"State"`
}

type rawImage struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	RepoTags    []string `json:"RepoTags"`
	Size        uint64   `json:"Size"`
}

func parseVersion(output []byte) string {
	var value struct {
		Client struct {
			Version string `json:"Version"`
		} `json:"Client"`
		Server struct {
			Version string `json:"Version"`
		} `json:"Server"`
	}
	if json.Unmarshal(output, &value) != nil {
		return "installed"
	}
	if value.Server.Version != "" {
		return value.Server.Version
	}
	if value.Client.Version != "" {
		return value.Client.Version
	}
	return "installed"
}

func imageName(image rawImage) string {
	if len(image.RepoTags) > 0 {
		return image.RepoTags[0]
	}
	if len(image.RepoDigests) > 0 {
		return image.RepoDigests[0]
	}
	return shortID(image.ID)
}

func uniqueLines(output []byte) []string {
	seen := map[string]bool{}
	var values []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !seen[line] {
			seen[line] = true
			values = append(values, line)
		}
	}
	return values
}

func validContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, character := range id {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
