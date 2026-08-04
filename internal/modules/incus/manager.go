package incus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

const localSocket = "/var/lib/incus/unix.socket"

// Instance is the list-level presentation model. Its live fields
// (Addresses, CPUTime, Memory, Processes, StartedAt) come from the
// instance's runtime state and are left at their zero value for an
// instance that is not running, which has no runtime state to report.
type Instance struct {
	Addresses []string `json:"addresses,omitempty"`
	CPUTime   int64    `json:"cpu_time"`
	Image     string   `json:"image"`
	Memory    uint64   `json:"memory"`
	Name      string   `json:"name"`
	Processes int64    `json:"processes"`
	Running   bool     `json:"running"`
	Snapshots int      `json:"snapshots"`
	StartedAt string   `json:"started_at,omitempty"`
	Status    string   `json:"status"`
	Type      string   `json:"type"`
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
	// Warnings names each storage read that degraded, so a single
	// unreadable pool is reported without hiding the rest of the
	// inventory.
	Warnings []string `json:"warnings,omitempty"`
}

type Manager interface {
	CreateSnapshot(context.Context, string, string, string) error
	DeleteSnapshot(context.Context, string, string, string) error
	Detail(context.Context, string, string) (Detail, error)
	Logs(context.Context, string, string, string) (Logs, error)
	Remove(context.Context, string, string) error
	RemoveImage(context.Context, string, string) error
	RestoreSnapshot(context.Context, string, string, string) error
	Restart(context.Context, string, string) error
	Start(context.Context, string, string) error
	State(context.Context, string) (State, error)
	Stop(context.Context, string, string) error
	StopForce(context.Context, string, string) error
}

type Client interface {
	ConsoleLog(context.Context, string, string) (io.ReadCloser, error)
	CreateSnapshot(context.Context, string, string, string) error
	DeleteSnapshot(context.Context, string, string, string) error
	Images(context.Context, string) ([]api.Image, error)
	Instance(context.Context, string, string) (*api.InstanceFull, error)
	Instances(context.Context, string) ([]api.InstanceFull, error)
	Logfile(context.Context, string, string, string) (io.ReadCloser, error)
	Projects(context.Context) ([]api.Project, error)
	Remove(context.Context, string, string) error
	RemoveImage(context.Context, string, string) error
	RestoreSnapshot(context.Context, string, string, string) error
	Restart(context.Context, string, string, int) error
	Server(context.Context) (*api.Server, error)
	Start(context.Context, string, string) error
	StorageBuckets(context.Context, string, string) ([]api.StorageBucket, error)
	StoragePools(context.Context) ([]api.StoragePool, error)
	StorageVolumes(context.Context, string, string) ([]api.StorageVolume, error)
	Stop(context.Context, string, string, int, bool) error
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

func (c *LocalClient) ConsoleLog(ctx context.Context, project, name string) (io.ReadCloser, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetInstanceConsoleLog(name, nil)
}

func (c *LocalClient) Instance(ctx context.Context, project, name string) (*api.InstanceFull, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	instance, _, err := server.GetInstanceFull(name)
	return instance, err
}

// Logfile reads one supervisor logfile. The filename is never
// caller-supplied: SystemManager resolves it from the instance type
// through logfileName.
func (c *LocalClient) Logfile(ctx context.Context, project, name, filename string) (io.ReadCloser, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetInstanceLogfile(name, filename)
}

func (c *LocalClient) Images(ctx context.Context, project string) ([]api.Image, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetImages()
}

// Instances uses GetInstancesFull so one round trip carries each instance's
// runtime state and snapshot list alongside its configuration; the list page
// renders live addresses and memory, and the detail page needs no second
// call to count snapshots.
func (c *LocalClient) Instances(ctx context.Context, project string) ([]api.InstanceFull, error) {
	server, err := c.connect(ctx, project)
	if err != nil {
		return nil, err
	}
	return server.GetInstancesFull(api.InstanceTypeAny)
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

// Stop asks the instance to shut down, or kills it outright when force is
// set. Incus ignores Timeout when Force is set, so the graceful timeout is
// passed either way and simply does not apply to a forced stop.
func (c *LocalClient) Stop(ctx context.Context, project, name string, timeout int, force bool) error {
	return c.updateState(ctx, project, name, api.InstanceStatePut{Action: "stop", Force: force, Timeout: timeout})
}

// CreateSnapshot always creates a non-stateful snapshot: a stateful one
// requires CRIU on the host, and Pilothouse exposes no way to ask for it.
func (c *LocalClient) CreateSnapshot(ctx context.Context, project, instance, name string) error {
	server, err := c.connect(ctx, project)
	if err != nil {
		return err
	}
	operation, err := server.CreateInstanceSnapshot(instance, api.InstanceSnapshotsPost{Name: name, Stateful: false})
	if err != nil {
		return err
	}
	return operation.WaitContext(ctx)
}

func (c *LocalClient) DeleteSnapshot(ctx context.Context, project, instance, name string) error {
	server, err := c.connect(ctx, project)
	if err != nil {
		return err
	}
	operation, err := server.DeleteInstanceSnapshot(instance, name)
	if err != nil {
		return err
	}
	return operation.WaitContext(ctx)
}

// RestoreSnapshot rolls the instance back through the instance-update path,
// which is how the Incus API expresses a restore: InstancePut.Restore names
// the snapshot. DiskOnly stays false so the whole instance is restored.
func (c *LocalClient) RestoreSnapshot(ctx context.Context, project, instance, name string) error {
	server, err := c.connect(ctx, project)
	if err != nil {
		return err
	}
	current, etag, err := server.GetInstance(instance)
	if err != nil {
		return err
	}
	update := current.Writable()
	update.Restore = name
	operation, err := server.UpdateInstance(instance, update, etag)
	if err != nil {
		return err
	}
	return operation.WaitContext(ctx)
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
		counts[baseImage(item.Instance)]++
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
	pools, volumes, buckets, warnings, err := m.storage(ctx, project)
	if err != nil {
		return State{}, err
	}
	version := server.Environment.ServerVersion
	if version == "" {
		version = "installed"
	}
	return State{
		Buckets: buckets, Images: images, Instances: instances, Pools: pools, Project: project,
		Projects: projects, Version: version, Volumes: volumes, Warnings: warnings,
	}, nil
}

// storage collects pools, custom volumes and buckets. Each pool's volume
// and bucket reads are independent: one pool that cannot be read degrades
// to a warning and leaves every other pool's inventory intact, rather than
// failing the whole page. Only the pool list itself is fatal, since without
// it there is nothing to enumerate.
func (m *SystemManager) storage(ctx context.Context, project string) ([]StoragePool, []StorageVolume, []StorageBucket, []string, error) {
	rawPools, err := m.client.StoragePools(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	pools := make([]StoragePool, 0, len(rawPools))
	volumes := []StorageVolume{}
	buckets := []StorageBucket{}
	warnings := []string{}
	for _, pool := range rawPools {
		status := pool.Status
		if status == "" {
			status = "created"
		}
		pools = append(pools, StoragePool{Driver: pool.Driver, Name: pool.Name, Status: status, UsedBy: len(pool.UsedBy)})
		// A failed volume read contributes a warning and no rows: a
		// partial result from a failed call must not be presented as
		// this pool's inventory. The bucket read is independent and is
		// still attempted.
		if rawVolumes, err := m.client.StorageVolumes(ctx, project, pool.Name); err != nil {
			warnings = append(warnings, "Storage volumes for pool "+pool.Name+" are unavailable.")
		} else {
			for _, volume := range rawVolumes {
				if volume.Type != "custom" {
					continue
				}
				volumes = append(volumes, StorageVolume{ContentType: volume.ContentType, Name: volume.Name, Pool: pool.Name, Type: volume.Type, UsedBy: len(volume.UsedBy)})
			}
		}
		rawBuckets, err := m.client.StorageBuckets(ctx, project, pool.Name)
		if err != nil {
			// A driver without bucket support is an ordinary capability
			// gap, not a degraded read, so it stays silent.
			if !bucketsUnsupported(err) {
				warnings = append(warnings, "Storage buckets for pool "+pool.Name+" are unavailable.")
			}
			continue
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
	return pools, volumes, buckets, warnings, nil
}

func bucketsUnsupported(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not supported") || strings.Contains(message, "does not support") || strings.Contains(message, "storage_buckets")
}

// Detail returns one instance's allowlisted configuration, devices,
// interfaces and snapshots. It resolves the instance directly rather than
// scanning the project's whole inventory.
func (m *SystemManager) Detail(ctx context.Context, requestedProject, name string) (Detail, error) {
	instance, project, err := m.fetch(ctx, requestedProject, name)
	if err != nil {
		return Detail{}, err
	}
	return detail(*instance, project), nil
}

// Logs returns the bounded tail of one of the two supported log sources.
// The caller chooses a source, never a filename: for SourceLog the
// supervisor logfile is derived from the resolved instance's own type.
func (m *SystemManager) Logs(ctx context.Context, requestedProject, name, source string) (Logs, error) {
	if !validSource(source) {
		return Logs{}, errors.New("unsupported log source")
	}
	instance, project, err := m.fetch(ctx, requestedProject, name)
	if err != nil {
		return Logs{}, err
	}
	stream, err := m.readLog(ctx, project, name, source, instance.Type)
	if err != nil {
		return Logs{}, err
	}
	defer func() { _ = stream.Close() }()
	return Logs{Lines: readLines(stream), Name: name, Project: project, Source: source}, nil
}

func (m *SystemManager) readLog(ctx context.Context, project, name, source, instanceType string) (io.ReadCloser, error) {
	if source == SourceConsole {
		return m.client.ConsoleLog(ctx, project, name)
	}
	return m.client.Logfile(ctx, project, name, logfileName(instanceType))
}

// fetch validates the instance name, resolves the project against the live
// project list, and returns the named instance. Both validations happen
// broker-side on every call, so a crafted request cannot reach the Incus
// API with an unvalidated name or an unavailable project.
func (m *SystemManager) fetch(ctx context.Context, requestedProject, name string) (*api.InstanceFull, string, error) {
	if !validInstanceName(name) {
		return nil, "", errors.New("invalid instance name")
	}
	project, _, err := m.project(ctx, requestedProject)
	if err != nil {
		return nil, "", err
	}
	instance, err := m.client.Instance(ctx, project, name)
	if err != nil {
		return nil, "", err
	}
	if instance == nil {
		return nil, "", errors.New("instance no longer exists")
	}
	return instance, project, nil
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
		if baseImage(instance.Instance) == fingerprint {
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
	return m.client.Stop(ctx, project, name, 30, false)
}

// StopForce kills an instance outright. It exists because the graceful path
// gives an instance 30 seconds and then fails, leaving a wedged instance
// with no way to stop it from the console at all.
func (m *SystemManager) StopForce(ctx context.Context, project, name string) error {
	instance, project, err := m.instance(ctx, project, name)
	if err != nil {
		return err
	}
	if !instance.Running {
		return errors.New("instance is not running")
	}
	return m.client.Stop(ctx, project, name, 30, true)
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

func (m *SystemManager) instances(ctx context.Context, project string) ([]Instance, []api.InstanceFull, error) {
	raw, err := m.client.Instances(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	instances := make([]Instance, 0, len(raw))
	for _, item := range raw {
		instances = append(instances, listInstance(item))
	}
	slices.SortFunc(instances, func(a, b Instance) int { return strings.Compare(a.Name, b.Name) })
	return instances, raw, nil
}

// listInstance projects one API instance onto the list model. State is nil
// for a stopped instance, so every live field stays at its zero value
// rather than being reported as a measured zero.
func listInstance(item api.InstanceFull) Instance {
	instance := Instance{
		Image: instanceImage(item.Instance), Name: item.Name, Running: item.StatusCode == api.Running,
		Snapshots: len(item.Snapshots), Status: item.Status, Type: instanceType(item.Type),
	}
	if item.State == nil {
		return instance
	}
	instance.Addresses = globalAddresses(item.State)
	instance.CPUTime = item.State.CPU.Usage
	instance.Processes = item.State.Processes
	if item.State.Memory.Usage > 0 {
		instance.Memory = uint64(item.State.Memory.Usage)
	}
	if !item.State.StartedAt.IsZero() {
		instance.StartedAt = item.State.StartedAt.UTC().Format(time.RFC3339)
	}
	return instance
}

// globalAddresses returns every globally-scoped address across the
// instance's interfaces, sorted so a map's iteration order cannot reorder
// the rendered result. Incus scopes loopback as "local" and link-local as
// "link", so filtering on "global" drops both without matching on
// interface names.
func globalAddresses(state *api.InstanceState) []string {
	var addresses []string
	for _, network := range state.Network {
		for _, address := range network.Addresses {
			if address.Scope == "global" && address.Address != "" {
				addresses = append(addresses, address.Address)
			}
		}
	}
	slices.Sort(addresses)
	return slices.Compact(addresses)
}

func instanceImage(item api.Instance) string {
	return firstValue(item.ExpandedConfig["image.description"], item.Config["image.description"], shortID(baseImage(item)))
}

func baseImage(item api.Instance) string {
	if fingerprint := item.ExpandedConfig["volatile.base_image"]; fingerprint != "" {
		return fingerprint
	}
	return item.Config["volatile.base_image"]
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
