package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
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
	RemoveImage(context.Context, string) error
	Restart(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context) (State, error)
	Stop(context.Context, string) error
}

type Client interface {
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerRestart(context.Context, string, client.ContainerRestartOptions) (client.ContainerRestartResult, error)
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error)
	ImageList(context.Context, client.ImageListOptions) (client.ImageListResult, error)
	ImageRemove(context.Context, string, client.ImageRemoveOptions) (client.ImageRemoveResult, error)
	ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error)
}

type SystemManager struct {
	client Client
}

func NewSystemManager(client Client) *SystemManager {
	return &SystemManager{client: client}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	version, err := m.client.ServerVersion(ctx, client.ServerVersionOptions{})
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
	_, err = m.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{})
	return err
}

func (m *SystemManager) RemoveImage(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("invalid image identifier")
	}
	_, containers, err := m.containers(ctx)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if container.Image == id {
			return errors.New("remove containers using this image before deleting it")
		}
	}
	images, err := m.images(ctx, containers)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(images, func(image Image) bool { return image.ID == id }) {
		return errors.New("image no longer exists")
	}
	_, err = m.client.ImageRemove(ctx, id, client.ImageRemoveOptions{})
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
	timeout := 10
	_, err = m.client.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &timeout})
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
	_, err = m.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
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
	timeout := 10
	_, err = m.client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
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

func (m *SystemManager) containers(ctx context.Context) ([]Container, []containertypes.InspectResponse, error) {
	result, err := m.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, nil, err
	}
	containers := make([]Container, 0, len(result.Items))
	inspected := make([]containertypes.InspectResponse, 0, len(result.Items))
	for _, summary := range result.Items {
		inspect, err := m.client.ContainerInspect(ctx, summary.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, nil, err
		}
		item := inspect.Container
		if item.ID == "" || item.State == nil || item.Config == nil {
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
	result, err := m.client.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, container := range containers {
		counts[container.Image]++
	}
	images := make([]Image, 0, len(result.Items))
	for _, item := range result.Items {
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
