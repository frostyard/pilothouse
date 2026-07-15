package incus

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

const localSocket = "/var/lib/incus/unix.socket"

type Instance struct {
	Image   string `json:"image"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Status  string `json:"status"`
	Type    string `json:"type"`
}

type Image struct {
	Fingerprint string `json:"fingerprint"`
	Instances   int    `json:"instances"`
	Name        string `json:"name"`
	Size        uint64 `json:"size"`
	Type        string `json:"type"`
}

type State struct {
	Images    []Image    `json:"images"`
	Instances []Instance `json:"instances"`
	Version   string     `json:"version"`
}

type Manager interface {
	Remove(context.Context, string) error
	Restart(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context) (State, error)
	Stop(context.Context, string) error
}

type Client interface {
	Images(context.Context) ([]api.Image, error)
	Instances(context.Context) ([]api.Instance, error)
	Remove(context.Context, string) error
	Restart(context.Context, string, int) error
	Server(context.Context) (*api.Server, error)
	Start(context.Context, string) error
	Stop(context.Context, string, int) error
}

type LocalClient struct{}

func NewLocalClient() *LocalClient {
	return &LocalClient{}
}

func (c *LocalClient) connect(ctx context.Context) (incusclient.InstanceServer, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	server, err := incusclient.ConnectIncusUnixWithContext(ctx, localSocket, &incusclient.ConnectionArgs{
		HTTPClient: httpClient, SkipGetEvents: true, SkipGetServer: true,
	})
	if err != nil {
		return nil, err
	}
	return server.UseProject("default"), nil
}

func (c *LocalClient) Images(ctx context.Context) ([]api.Image, error) {
	server, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	return server.GetImages()
}

func (c *LocalClient) Instances(ctx context.Context) ([]api.Instance, error) {
	server, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	return server.GetInstances(api.InstanceTypeAny)
}

func (c *LocalClient) Remove(ctx context.Context, name string) error {
	server, err := c.connect(ctx)
	if err != nil {
		return err
	}
	operation, err := server.DeleteInstance(name)
	if err != nil {
		return err
	}
	return operation.WaitContext(ctx)
}

func (c *LocalClient) Restart(ctx context.Context, name string, timeout int) error {
	return c.updateState(ctx, name, api.InstanceStatePut{Action: "restart", Timeout: timeout})
}

func (c *LocalClient) Server(ctx context.Context) (*api.Server, error) {
	server, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	value, _, err := server.GetServer()
	return value, err
}

func (c *LocalClient) Start(ctx context.Context, name string) error {
	return c.updateState(ctx, name, api.InstanceStatePut{Action: "start"})
}

func (c *LocalClient) Stop(ctx context.Context, name string, timeout int) error {
	return c.updateState(ctx, name, api.InstanceStatePut{Action: "stop", Timeout: timeout})
}

func (c *LocalClient) updateState(ctx context.Context, name string, state api.InstanceStatePut) error {
	server, err := c.connect(ctx)
	if err != nil {
		return err
	}
	operation, err := server.UpdateInstanceState(name, state, "")
	if err != nil {
		return err
	}
	return operation.WaitContext(ctx)
}

type SystemManager struct {
	client Client
}

func NewSystemManager(client Client) *SystemManager {
	return &SystemManager{client: client}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	server, err := m.client.Server(ctx)
	if err != nil {
		return State{}, err
	}
	instances, rawInstances, err := m.instances(ctx)
	if err != nil {
		return State{}, err
	}
	rawImages, err := m.client.Images(ctx)
	if err != nil {
		return State{}, err
	}
	counts := map[string]int{}
	for _, item := range rawInstances {
		fingerprint := item.ExpandedConfig["volatile.base_image"]
		if fingerprint == "" {
			fingerprint = item.Config["volatile.base_image"]
		}
		counts[fingerprint]++
	}
	images := make([]Image, 0, len(rawImages))
	for _, item := range rawImages {
		size := uint64(0)
		if item.Size > 0 {
			size = uint64(item.Size)
		}
		images = append(images, Image{
			Fingerprint: item.Fingerprint, Instances: counts[item.Fingerprint], Name: imageName(item),
			Size: size, Type: instanceType(item.Type),
		})
	}
	slices.SortFunc(images, func(a, b Image) int { return strings.Compare(a.Name, b.Name) })
	version := server.Environment.ServerVersion
	if version == "" {
		version = "installed"
	}
	return State{Images: images, Instances: instances, Version: version}, nil
}

func (m *SystemManager) Remove(ctx context.Context, name string) error {
	instance, err := m.instance(ctx, name)
	if err != nil {
		return err
	}
	if instance.Running {
		return errors.New("stop the instance before removing it")
	}
	return m.client.Remove(ctx, name)
}

func (m *SystemManager) Restart(ctx context.Context, name string) error {
	instance, err := m.instance(ctx, name)
	if err != nil {
		return err
	}
	if !instance.Running {
		return errors.New("instance is not running")
	}
	return m.client.Restart(ctx, name, 30)
}

func (m *SystemManager) Start(ctx context.Context, name string) error {
	instance, err := m.instance(ctx, name)
	if err != nil {
		return err
	}
	if instance.Running {
		return errors.New("instance is already running")
	}
	return m.client.Start(ctx, name)
}

func (m *SystemManager) Stop(ctx context.Context, name string) error {
	instance, err := m.instance(ctx, name)
	if err != nil {
		return err
	}
	if !instance.Running {
		return errors.New("instance is not running")
	}
	return m.client.Stop(ctx, name, 30)
}

func (m *SystemManager) instance(ctx context.Context, name string) (Instance, error) {
	if !validInstanceName(name) {
		return Instance{}, errors.New("invalid instance name")
	}
	instances, _, err := m.instances(ctx)
	if err != nil {
		return Instance{}, err
	}
	for _, instance := range instances {
		if instance.Name == name {
			return instance, nil
		}
	}
	return Instance{}, errors.New("instance no longer exists")
}

func (m *SystemManager) instances(ctx context.Context) ([]Instance, []api.Instance, error) {
	raw, err := m.client.Instances(ctx)
	if err != nil {
		return nil, nil, err
	}
	instances := make([]Instance, 0, len(raw))
	for _, item := range raw {
		fingerprint := item.ExpandedConfig["volatile.base_image"]
		if fingerprint == "" {
			fingerprint = item.Config["volatile.base_image"]
		}
		image := firstValue(item.ExpandedConfig["image.description"], item.Config["image.description"], shortID(fingerprint))
		instances = append(instances, Instance{
			Image: image, Name: item.Name, Running: item.StatusCode == api.Running,
			Status: item.Status, Type: instanceType(item.Type),
		})
	}
	slices.SortFunc(instances, func(a, b Instance) int { return strings.Compare(a.Name, b.Name) })
	return instances, raw, nil
}

func imageName(image api.Image) string {
	if len(image.Aliases) > 0 && image.Aliases[0].Name != "" {
		return image.Aliases[0].Name
	}
	return firstValue(image.Properties["description"], shortID(image.Fingerprint))
}

func instanceType(value string) string {
	if value == "virtual-machine" {
		return "Virtual machine"
	}
	return "Container"
}

func firstValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "Unknown"
}

func validInstanceName(name string) bool {
	if len(name) == 0 || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, character := range name {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}
