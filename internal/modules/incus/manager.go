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

type Project struct {
	Description string `json:"description"`
	Name        string `json:"name"`
}

type StorageBucket struct {
	Name  string `json:"name"`
	Pool  string `json:"pool"`
	S3URL string `json:"s3_url"`
}

type StoragePool struct {
	Driver string `json:"driver"`
	Name   string `json:"name"`
	Status string `json:"status"`
	UsedBy int    `json:"used_by"`
}

type StorageVolume struct {
	ContentType string `json:"content_type"`
	Name        string `json:"name"`
	Pool        string `json:"pool"`
	Type        string `json:"type"`
	UsedBy      int    `json:"used_by"`
}

type State struct {
	Buckets   []StorageBucket `json:"buckets"`
	Images    []Image         `json:"images"`
	Instances []Instance      `json:"instances"`
	Pools     []StoragePool   `json:"pools"`
	Project   string          `json:"project"`
	Projects  []Project       `json:"projects"`
	Version   string          `json:"version"`
	Volumes   []StorageVolume `json:"volumes"`
}

type Manager interface {
	Remove(context.Context, string, string) error
	RemoveImage(context.Context, string, string) error
	Restart(context.Context, string, string) error
	Start(context.Context, string, string) error
	State(context.Context, string) (State, error)
	Stop(context.Context, string, string) error
}

type Client interface {
	Images(context.Context, string) ([]api.Image, error)
	Instances(context.Context, string) ([]api.Instance, error)
	Projects(context.Context) ([]api.Project, error)
	Remove(context.Context, string, string) error
	RemoveImage(context.Context, string, string) error
	Restart(context.Context, string, string, int) error
	Server(context.Context) (*api.Server, error)
	Start(context.Context, string, string) error
	StorageBuckets(context.Context, string, string) ([]api.StorageBucket, error)
	StoragePools(context.Context) ([]api.StoragePool, error)
	StorageVolumes(context.Context, string, string) ([]api.StorageVolume, error)
	Stop(context.Context, string, string, int) error
}

type LocalClient struct{}

func NewLocalClient() *LocalClient {
	return &LocalClient{}
}

func (c *LocalClient) connect(ctx context.Context, project string) (incusclient.InstanceServer, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	server, err := incusclient.ConnectIncusUnixWithContext(ctx, localSocket, &incusclient.ConnectionArgs{
		HTTPClient: httpClient, SkipGetEvents: true, SkipGetServer: true,
	})
	if err != nil {
		return nil, err
	}
	if project != "" {
		server = server.UseProject(project)
	}
	return server, nil
}

func (c *LocalClient) Images(ctx context.Context, project string) ([]api.Image, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetImages()
}

func (c *LocalClient) Instances(ctx context.Context, project string) ([]api.Instance, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetInstances(api.InstanceTypeAny)
}

func (c *LocalClient) Projects(ctx context.Context) ([]api.Project, error) {
	server, err := c.connect(ctx, "")
	if err != nil {
		return nil, err
	}
	return server.GetProjects()
}

func (c *LocalClient) Remove(ctx context.Context, project, name string) error {
	server, err := c.connect(ctx, project)
	if err != nil {
		return err
	}
	operation, err := server.DeleteInstance(name)
	if err != nil {
		return err
	}
	return operation.WaitContext(ctx)
}

func (c *LocalClient) RemoveImage(ctx context.Context, project, fingerprint string) error {
	server, err := c.connect(ctx, project)
	if err != nil {
		return err
	}
	operation, err := server.DeleteImage(fingerprint)
	if err != nil {
		return err
	}
	return operation.WaitContext(ctx)
}

func (c *LocalClient) Restart(ctx context.Context, project, name string, timeout int) error {
	return c.updateState(ctx, project, name, api.InstanceStatePut{Action: "restart", Timeout: timeout})
}

func (c *LocalClient) Server(ctx context.Context) (*api.Server, error) {
	server, err := c.connect(ctx, "")
	if err != nil {
		return nil, err
	}
	value, _, err := server.GetServer()
	return value, err
}

func (c *LocalClient) Start(ctx context.Context, project, name string) error {
	return c.updateState(ctx, project, name, api.InstanceStatePut{Action: "start"})
}

func (c *LocalClient) StorageBuckets(ctx context.Context, project, pool string) ([]api.StorageBucket, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetStoragePoolBuckets(pool)
}

func (c *LocalClient) StoragePools(ctx context.Context) ([]api.StoragePool, error) {
	server, err := c.connect(ctx, "")
	if err != nil {
		return nil, err
	}
	return server.GetStoragePools()
}

func (c *LocalClient) StorageVolumes(ctx context.Context, project, pool string) ([]api.StorageVolume, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetStoragePoolVolumes(pool)
}

func (c *LocalClient) Stop(ctx context.Context, project, name string, timeout int) error {
	return c.updateState(ctx, project, name, api.InstanceStatePut{Action: "stop", Timeout: timeout})
}

func (c *LocalClient) updateState(ctx context.Context, project, name string, state api.InstanceStatePut) error {
	server, err := c.connect(ctx, project)
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

func (m *SystemManager) State(ctx context.Context, requestedProject string) (State, error) {
	project, projects, err := m.project(ctx, requestedProject)
	if err != nil {
		return State{}, err
	}
	server, err := m.client.Server(ctx)
	if err != nil {
		return State{}, err
	}
	instances, rawInstances, err := m.instances(ctx, project)
	if err != nil {
		return State{}, err
	}
	rawImages, err := m.client.Images(ctx, project)
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
	pools, volumes, buckets, err := m.storage(ctx, project)
	if err != nil {
		return State{}, err
	}
	version := server.Environment.ServerVersion
	if version == "" {
		version = "installed"
	}
	return State{Buckets: buckets, Images: images, Instances: instances, Pools: pools, Project: project, Projects: projects, Version: version, Volumes: volumes}, nil
}

func (m *SystemManager) storage(ctx context.Context, project string) ([]StoragePool, []StorageVolume, []StorageBucket, error) {
	rawPools, err := m.client.StoragePools(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	pools := make([]StoragePool, 0, len(rawPools))
	volumes := []StorageVolume{}
	buckets := []StorageBucket{}
	for _, pool := range rawPools {
		status := pool.Status
		if status == "" {
			status = "created"
		}
		pools = append(pools, StoragePool{Driver: pool.Driver, Name: pool.Name, Status: status, UsedBy: len(pool.UsedBy)})
		rawVolumes, err := m.client.StorageVolumes(ctx, project, pool.Name)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, volume := range rawVolumes {
			if volume.Type != "custom" {
				continue
			}
			volumes = append(volumes, StorageVolume{ContentType: volume.ContentType, Name: volume.Name, Pool: pool.Name, Type: volume.Type, UsedBy: len(volume.UsedBy)})
		}
		rawBuckets, err := m.client.StorageBuckets(ctx, project, pool.Name)
		if err != nil {
			if bucketsUnsupported(err) {
				continue
			}
			return nil, nil, nil, err
		}
		for _, bucket := range rawBuckets {
			buckets = append(buckets, StorageBucket{Name: bucket.Name, Pool: pool.Name, S3URL: bucket.S3URL})
		}
	}
	slices.SortFunc(pools, func(a, b StoragePool) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(volumes, func(a, b StorageVolume) int {
		if result := strings.Compare(a.Pool, b.Pool); result != 0 {
			return result
		}
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(buckets, func(a, b StorageBucket) int {
		if result := strings.Compare(a.Pool, b.Pool); result != 0 {
			return result
		}
		return strings.Compare(a.Name, b.Name)
	})
	return pools, volumes, buckets, nil
}

func bucketsUnsupported(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not supported") || strings.Contains(message, "does not support") || strings.Contains(message, "storage_buckets")
}

func (m *SystemManager) Remove(ctx context.Context, project, name string) error {
	instance, project, err := m.instance(ctx, project, name)
	if err != nil {
		return err
	}
	if instance.Running {
		return errors.New("stop the instance before removing it")
	}
	return m.client.Remove(ctx, project, name)
}

func (m *SystemManager) RemoveImage(ctx context.Context, requestedProject, fingerprint string) error {
	if strings.TrimSpace(requestedProject) == "" || strings.TrimSpace(fingerprint) == "" {
		return errors.New("project and image fingerprint are required")
	}
	project, _, err := m.project(ctx, requestedProject)
	if err != nil {
		return err
	}
	_, instances, err := m.instances(ctx, project)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		baseImage := instance.ExpandedConfig["volatile.base_image"]
		if baseImage == "" {
			baseImage = instance.Config["volatile.base_image"]
		}
		if baseImage == fingerprint {
			return errors.New("remove instances using this image before deleting it")
		}
	}
	images, err := m.client.Images(ctx, project)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(images, func(image api.Image) bool { return image.Fingerprint == fingerprint }) {
		return errors.New("image no longer exists")
	}
	return m.client.RemoveImage(ctx, project, fingerprint)
}

func (m *SystemManager) Restart(ctx context.Context, project, name string) error {
	instance, project, err := m.instance(ctx, project, name)
	if err != nil {
		return err
	}
	if !instance.Running {
		return errors.New("instance is not running")
	}
	return m.client.Restart(ctx, project, name, 30)
}

func (m *SystemManager) Start(ctx context.Context, project, name string) error {
	instance, project, err := m.instance(ctx, project, name)
	if err != nil {
		return err
	}
	if instance.Running {
		return errors.New("instance is already running")
	}
	return m.client.Start(ctx, project, name)
}

func (m *SystemManager) Stop(ctx context.Context, project, name string) error {
	instance, project, err := m.instance(ctx, project, name)
	if err != nil {
		return err
	}
	if !instance.Running {
		return errors.New("instance is not running")
	}
	return m.client.Stop(ctx, project, name, 30)
}

func (m *SystemManager) instance(ctx context.Context, requestedProject, name string) (Instance, string, error) {
	if !validInstanceName(name) {
		return Instance{}, "", errors.New("invalid instance name")
	}
	project, _, err := m.project(ctx, requestedProject)
	if err != nil {
		return Instance{}, "", err
	}
	instances, _, err := m.instances(ctx, project)
	if err != nil {
		return Instance{}, "", err
	}
	for _, instance := range instances {
		if instance.Name == name {
			return instance, project, nil
		}
	}
	return Instance{}, "", errors.New("instance no longer exists")
}

func (m *SystemManager) instances(ctx context.Context, project string) ([]Instance, []api.Instance, error) {
	raw, err := m.client.Instances(ctx, project)
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

func (m *SystemManager) project(ctx context.Context, requested string) (string, []Project, error) {
	if requested == "" {
		requested = "default"
	}
	raw, err := m.client.Projects(ctx)
	if err != nil {
		return "", nil, err
	}
	projects := make([]Project, 0, len(raw))
	found := false
	for _, item := range raw {
		projects = append(projects, Project{Name: item.Name, Description: item.Description})
		if item.Name == requested {
			found = true
		}
	}
	slices.SortFunc(projects, func(a, b Project) int { return strings.Compare(a.Name, b.Name) })
	if !found {
		return "", nil, errors.New("project is not available")
	}
	return requested, projects, nil
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
