package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const apiPrefix = "/v5.0.0/libpod"

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
	RemoveImage(context.Context, string) error
	Restart(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context) (State, error)
	Stop(context.Context, string) error
}

type Client interface {
	Containers(context.Context) ([]apiContainer, error)
	Images(context.Context) ([]apiImage, error)
	Pods(context.Context) ([]apiPod, error)
	Remove(context.Context, string) error
	RemoveImage(context.Context, string) error
	Restart(context.Context, string, int) error
	Start(context.Context, string) error
	Stop(context.Context, string, int) error
	Version(context.Context) (string, error)
}

type APIClient struct {
	http *http.Client
}

func NewAPIClient(socket string) *APIClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &APIClient{http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

func (c *APIClient) Close() {
	c.http.CloseIdleConnections()
}

func (c *APIClient) Containers(ctx context.Context) ([]apiContainer, error) {
	var values []apiContainer
	err := c.do(ctx, http.MethodGet, "/containers/json?all=true", &values)
	return values, err
}

func (c *APIClient) Images(ctx context.Context) ([]apiImage, error) {
	var values []apiImage
	err := c.do(ctx, http.MethodGet, "/images/json", &values)
	return values, err
}

func (c *APIClient) Pods(ctx context.Context) ([]apiPod, error) {
	var values []apiPod
	err := c.do(ctx, http.MethodGet, "/pods/json", &values)
	return values, err
}

func (c *APIClient) Remove(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), nil)
}

func (c *APIClient) RemoveImage(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/images/"+url.PathEscape(id), nil)
}

func (c *APIClient) Restart(ctx context.Context, id string, timeout int) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/containers/%s/restart?t=%d", url.PathEscape(id), timeout), nil)
}

func (c *APIClient) Start(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil)
}

func (c *APIClient) Stop(ctx context.Context, id string, timeout int) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/containers/%s/stop?t=%d", url.PathEscape(id), timeout), nil)
}

func (c *APIClient) Version(ctx context.Context) (string, error) {
	var value struct {
		Version string
	}
	if err := c.do(ctx, http.MethodGet, "/version", &value); err != nil {
		return "", err
	}
	return value.Version, nil
}

func (c *APIClient) do(ctx context.Context, method, path string, result any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://podman"+apiPrefix+path, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if detail := strings.TrimSpace(string(message)); detail != "" {
			return fmt.Errorf("podman API %s: %s", response.Status, detail)
		}
		return fmt.Errorf("podman API %s", response.Status)
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		return fmt.Errorf("decode podman API response: %w", err)
	}
	return nil
}

type SystemManager struct {
	client Client
}

func NewSystemManager(client Client) *SystemManager {
	return &SystemManager{client: client}
}

func (m *SystemManager) State(ctx context.Context) (State, error) {
	version, err := m.client.Version(ctx)
	if err != nil {
		return State{}, err
	}
	containers, err := m.containers(ctx)
	if err != nil {
		return State{}, err
	}
	rawPods, err := m.client.Pods(ctx)
	if err != nil {
		return State{}, err
	}
	rawImages, err := m.client.Images(ctx)
	if err != nil {
		return State{}, err
	}
	if version == "" {
		version = "installed"
	}
	return State{Containers: containers, Images: images(rawImages), Pods: pods(rawPods), Version: version}, nil
}

func (m *SystemManager) Remove(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if container.Running {
		return errors.New("stop the container before removing it")
	}
	return m.client.Remove(ctx, id)
}

func (m *SystemManager) RemoveImage(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("invalid image identifier")
	}
	raw, err := m.client.Images(ctx)
	if err != nil {
		return err
	}
	for _, image := range raw {
		if image.ID != id {
			continue
		}
		if image.Containers > 0 {
			return errors.New("remove containers using this image before deleting it")
		}
		return m.client.RemoveImage(ctx, id)
	}
	return errors.New("image no longer exists")
}

func (m *SystemManager) Restart(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if !container.Running {
		return errors.New("container is not running")
	}
	return m.client.Restart(ctx, id, 10)
}

func (m *SystemManager) Start(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if container.Running {
		return errors.New("container is already running")
	}
	return m.client.Start(ctx, id)
}

func (m *SystemManager) Stop(ctx context.Context, id string) error {
	container, err := m.container(ctx, id)
	if err != nil {
		return err
	}
	if !container.Running {
		return errors.New("container is not running")
	}
	return m.client.Stop(ctx, id, 10)
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
	raw, err := m.client.Containers(ctx)
	if err != nil {
		return nil, err
	}
	containers := make([]Container, 0, len(raw))
	for _, item := range raw {
		state := strings.ToLower(item.State)
		running := state == "running" || strings.HasPrefix(strings.ToLower(item.Status), "up ")
		containers = append(containers, Container{
			ID: item.ID, Image: item.Image, Name: firstName(item.Names, item.ID), Pod: item.PodName,
			Running: running, State: state, Status: item.Status,
		})
	}
	slices.SortFunc(containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return containers, nil
}

type apiContainer struct {
	ID      string   `json:"Id"`
	Image   string   `json:"Image"`
	Names   []string `json:"Names"`
	PodName string   `json:"PodName"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
}

type apiImage struct {
	Containers  int      `json:"Containers"`
	ID          string   `json:"Id"`
	Names       []string `json:"Names"`
	RepoTags    []string `json:"RepoTags"`
	Size        int64    `json:"Size"`
	VirtualSize int64    `json:"VirtualSize"`
}

type apiPod struct {
	Containers []struct {
		ID string `json:"Id"`
	} `json:"Containers"`
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

func images(raw []apiImage) []Image {
	values := make([]Image, 0, len(raw))
	for _, item := range raw {
		size := uint64(0)
		if item.Size > 0 {
			size = uint64(item.Size)
		} else if item.VirtualSize > 0 {
			size = uint64(item.VirtualSize)
		}
		names := item.Names
		if len(names) == 0 {
			names = item.RepoTags
		}
		values = append(values, Image{Containers: item.Containers, ID: item.ID, Name: firstName(names, item.ID), Size: size})
	}
	slices.SortFunc(values, func(a, b Image) int { return strings.Compare(a.Name, b.Name) })
	return values
}

func pods(raw []apiPod) []Pod {
	values := make([]Pod, 0, len(raw))
	for _, item := range raw {
		values = append(values, Pod{Containers: len(item.Containers), ID: item.ID, Name: item.Name, Status: item.Status})
	}
	slices.SortFunc(values, func(a, b Pod) int { return strings.Compare(a.Name, b.Name) })
	return values
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
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
