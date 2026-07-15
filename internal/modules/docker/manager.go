package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
)

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

type Client interface {
	ContainerInspect(context.Context, string) (containertypes.InspectResponse, error)
	ContainerList(context.Context, containertypes.ListOptions) ([]containertypes.Summary, error)
	ContainerRemove(context.Context, string, containertypes.RemoveOptions) error
	ContainerRestart(context.Context, string, containertypes.StopOptions) error
	ContainerStart(context.Context, string, containertypes.StartOptions) error
	ContainerStop(context.Context, string, containertypes.StopOptions) error
	ImageList(context.Context, imagetypes.ListOptions) ([]imagetypes.Summary, error)
	ServerVersion(context.Context) (types.Version, error)
}

type SystemManager struct {
	client Client
}

func NewSystemManager(client Client) *SystemManager {
	return &SystemManager{client: client}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	version, err := m.client.ServerVersion(ctx)
	if err != nil {
		return State{}, err
	}
	containers, inspected, err := m.containers(ctx)
	if err != nil {
		return State{}, err
	}
	images, err := m.images(ctx, inspected)
	if err != nil {
		return State{}, err
	}
	engineVersion := version.Version
	if engineVersion == "" {
		engineVersion = "installed"
	}
	return State{Containers: containers, Images: images, Version: engineVersion}, nil
}

func (m *SystemManager) Remove(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if container.Running {
		return errors.New("stop the container before removing it")
	}
	return m.client.ContainerRemove(ctx, id, containertypes.RemoveOptions{})
}

func (m *SystemManager) Restart(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if !container.Running {
		return errors.New("container is not running")
	}
	timeout := 10
	return m.client.ContainerRestart(ctx, id, containertypes.StopOptions{Timeout: &timeout})
}

func (m *SystemManager) Start(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if container.Running {
		return errors.New("container is already running")
	}
	return m.client.ContainerStart(ctx, id, containertypes.StartOptions{})
}

func (m *SystemManager) Stop(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if !container.Running {
		return errors.New("container is not running")
	}
	timeout := 10
	return m.client.ContainerStop(ctx, id, containertypes.StopOptions{Timeout: &timeout})
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

func (m *SystemManager) containers(ctx context.Context) ([]Container, []containertypes.InspectResponse, error) {
	summaries, err := m.client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return nil, nil, err
	}
	containers := make([]Container, 0, len(summaries))
	inspected := make([]containertypes.InspectResponse, 0, len(summaries))
	for _, summary := range summaries {
		item, err := m.client.ContainerInspect(ctx, summary.ID)
		if err != nil {
			return nil, nil, err
		}
		if item.ContainerJSONBase == nil || item.State == nil || item.Config == nil {
			return nil, nil, fmt.Errorf("docker returned incomplete inspect data for container %s", summary.ID)
		}
		status := string(item.State.Status)
		if !item.State.Running && item.State.Status == containertypes.StateExited {
			status = fmt.Sprintf("exited (%d)", item.State.ExitCode)
		}
		containers = append(containers, Container{
			ID: item.ID, Image: item.Config.Image, Name: strings.TrimPrefix(item.Name, "/"),
			Running: item.State.Running, State: string(item.State.Status), Status: status,
		})
		inspected = append(inspected, item)
	}
	slices.SortFunc(containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return containers, inspected, nil
}

func (m *SystemManager) images(ctx context.Context, containers []containertypes.InspectResponse) ([]Image, error) {
	raw, err := m.client.ImageList(ctx, imagetypes.ListOptions{})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, container := range containers {
		counts[container.Image]++
	}
	images := make([]Image, 0, len(raw))
	for _, item := range raw {
		size := uint64(0)
		if item.Size > 0 {
			size = uint64(item.Size)
		}
		images = append(images, Image{
			Containers: counts[item.ID], ID: item.ID, Name: imageName(item), Size: size,
		})
	}
	slices.SortFunc(images, func(a, b Image) int { return strings.Compare(a.Name, b.Name) })
	return images, nil
}

func imageName(image imagetypes.Summary) string {
	if len(image.RepoTags) > 0 {
		return image.RepoTags[0]
	}
	if len(image.RepoDigests) > 0 {
		return image.RepoDigests[0]
	}
	return shortID(image.ID)
}

func validContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
