package podman

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
	Pod     string `json:"pod,omitempty"`
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

type Pod struct {
	Containers int    `json:"containers"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
}

type State struct {
	Containers []Container `json:"containers"`
	Images     []Image     `json:"images"`
	Pods       []Pod       `json:"pods"`
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
		binary = "podman"
	}
	return &SystemManager{binary: binary, runner: runner}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	versionOutput, err := m.runner.Run(ctx, m.binary, "version", "--format", "json")
	if err != nil {
		return State{}, err
	}
	containers, err := m.containers(ctx)
	if err != nil {
		return State{}, err
	}
	podsOutput, err := m.runner.Run(ctx, m.binary, "pod", "ps", "--format", "json")
	if err != nil {
		return State{}, err
	}
	imagesOutput, err := m.runner.Run(ctx, m.binary, "images", "--format", "json")
	if err != nil {
		return State{}, err
	}
	pods, err := parsePods(podsOutput)
	if err != nil {
		return State{}, fmt.Errorf("parse podman pods: %w", err)
	}
	images, err := parseImages(imagesOutput)
	if err != nil {
		return State{}, fmt.Errorf("parse podman images: %w", err)
	}
	return State{Containers: containers, Images: images, Pods: pods, Version: parseVersion(versionOutput)}, nil
}

func (m *SystemManager) Remove(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if container.Running {
		return errors.New("stop the container before removing it")
	}
	_, err = m.runner.Run(ctx, m.binary, "rm", id)
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
	_, err = m.runner.Run(ctx, m.binary, "restart", "--time", "10", id)
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
	_, err = m.runner.Run(ctx, m.binary, "start", id)
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
	_, err = m.runner.Run(ctx, m.binary, "stop", "--time", "10", id)
	return err
}

func (m *SystemManager) container(ctx context.Context, id string) (Container, error) {
	if !validContainerID(id) {
		return Container{}, errors.New("invalid container identifier")
	}
	containers, err := m.containers(ctx)
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

func (m *SystemManager) containers(ctx context.Context) ([]Container, error) {
	output, err := m.runner.Run(ctx, m.binary, "ps", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	containers, err := parseContainers(output)
	if err != nil {
		return nil, fmt.Errorf("parse podman containers: %w", err)
	}
	return containers, nil
}

type rawContainer struct {
	ID     string     `json:"Id"`
	Image  string     `json:"Image"`
	Names  stringList `json:"Names"`
	Pod    string     `json:"Pod"`
	State  string     `json:"State"`
	Status string     `json:"Status"`
}

type rawImage struct {
	Containers int        `json:"Containers"`
	ID         string     `json:"Id"`
	Names      stringList `json:"Names"`
	Size       uint64     `json:"Size"`
}

type rawPod struct {
	ID            string `json:"Id"`
	Name          string `json:"Name"`
	NumContainers int    `json:"NumContainers"`
	Status        string `json:"Status"`
}

type stringList []string

func (values *stringList) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*values = list
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value != "" {
		*values = []string{value}
	}
	return nil
}

func parseContainers(output []byte) ([]Container, error) {
	var raw []rawContainer
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, err
	}
	containers := make([]Container, 0, len(raw))
	for _, item := range raw {
		state := strings.ToLower(item.State)
		running := state == "running" || strings.HasPrefix(strings.ToLower(item.Status), "up ")
		containers = append(containers, Container{
			ID: item.ID, Image: item.Image, Name: firstName(item.Names, item.ID), Pod: item.Pod,
			Running: running, State: state, Status: item.Status,
		})
	}
	slices.SortFunc(containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return containers, nil
}

func parseImages(output []byte) ([]Image, error) {
	var raw []rawImage
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, err
	}
	images := make([]Image, 0, len(raw))
	for _, item := range raw {
		images = append(images, Image{Containers: item.Containers, ID: item.ID, Name: firstName(item.Names, item.ID), Size: item.Size})
	}
	slices.SortFunc(images, func(a, b Image) int { return strings.Compare(a.Name, b.Name) })
	return images, nil
}

func parsePods(output []byte) ([]Pod, error) {
	var raw []rawPod
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, err
	}
	pods := make([]Pod, 0, len(raw))
	for _, item := range raw {
		pods = append(pods, Pod{Containers: item.NumContainers, ID: item.ID, Name: item.Name, Status: item.Status})
	}
	slices.SortFunc(pods, func(a, b Pod) int { return strings.Compare(a.Name, b.Name) })
	return pods, nil
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

func firstName(names []string, id string) string {
	if len(names) > 0 && names[0] != "" {
		return names[0]
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
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
