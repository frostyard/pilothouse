package podman

import (
	"bufio"
	"context"
	"encoding/binary"
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

const (
	defaultTimeout = 10
	logsTailLines  = 200
	logsMaxBytes   = 256 * 1024
)

type Container struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	Name    string `json:"name"`
	Pod     string `json:"pod,omitempty"`
	Running bool   `json:"running"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

type LogLine struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Message   string `json:"message"`
}

type Logs struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Lines []LogLine `json:"lines"`
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
	Logs(context.Context, string) (Logs, error)
	Remove(context.Context, string) error
	RemoveImage(context.Context, string) error
	Restart(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context) (State, error)
	Stop(context.Context, string) error
}

type Client interface {
	Containers(context.Context) ([]apiContainer, error)
	Logs(context.Context, string) (io.ReadCloser, error)
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

func (c *APIClient) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	path := "/containers/" + url.PathEscape(id) + "/logs?stdout=true&stderr=true&timestamps=true&tail=200&follow=false"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://podman"+apiPrefix+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = response.Body.Close() }()
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if detail := strings.TrimSpace(string(message)); detail != "" {
			return nil, fmt.Errorf("podman API %s: %s", response.Status, detail)
		}
		return nil, fmt.Errorf("podman API %s", response.Status)
	}
	return response.Body, nil
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

func (m *SystemManager) Logs(ctx context.Context, id string) (Logs, error) {
	container, err := m.container(ctx, id)
	if err != nil {
		return Logs{}, err
	}
	stream, err := m.client.Logs(ctx, id)
	if err != nil {
		return Logs{}, err
	}
	defer func() { _ = stream.Close() }()
	return Logs{ID: id, Name: container.Name, Lines: readLogLines(stream)}, nil
}

func readLogLines(reader io.Reader) []LogLine {
	buffered := bufio.NewReader(reader)
	header, err := buffered.Peek(8)
	if err == nil && (header[0] == 1 || header[0] == 2) && header[1] == 0 && header[2] == 0 && header[3] == 0 {
		return readMultiplexedLogLines(buffered)
	}
	lines := newLogLines()
	lines.read(buffered, "stdout")
	return lines.lines
}

func readMultiplexedLogLines(reader io.Reader) []LogLine {
	lines := newLogLines()
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			break
		}
		stream := "stdout"
		if header[0] == 2 {
			stream = "stderr"
		} else if header[0] != 1 {
			break
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		payload := &io.LimitedReader{R: reader, N: size}
		frameLines := newLogLines()
		frameLines.read(payload, stream)
		if payload.N != 0 {
			break
		}
		for _, line := range frameLines.lines {
			lines.add(line)
		}
	}
	return lines.lines
}

type boundedLogLines struct {
	lines []LogLine
	bytes int
}

func newLogLines() *boundedLogLines {
	return &boundedLogLines{lines: make([]LogLine, 0, logsTailLines)}
}

func (lines *boundedLogLines) read(reader io.Reader, stream string) {
	buffered := bufio.NewReader(reader)
	var raw strings.Builder
	discard := false
	for {
		fragment, err := buffered.ReadSlice('\n')
		if !discard {
			if raw.Len()+len(fragment) <= logsMaxBytes {
				_, _ = raw.Write(fragment)
			} else {
				raw.Reset()
				discard = true
			}
		}
		if len(fragment) > 0 && fragment[len(fragment)-1] == '\n' {
			if !discard {
				lines.add(parseLogLine(strings.TrimRight(raw.String(), "\r\n"), stream))
			}
			raw.Reset()
			discard = false
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) && !discard && raw.Len() > 0 {
				lines.add(parseLogLine(strings.TrimRight(raw.String(), "\r\n"), stream))
			}
			return
		}
	}
}

func (lines *boundedLogLines) add(line LogLine) {
	size := logLineSize(line)
	if size > logsMaxBytes {
		return
	}
	lines.lines = append(lines.lines, line)
	lines.bytes += size
	for len(lines.lines) > logsTailLines || lines.bytes > logsMaxBytes {
		lines.bytes -= logLineSize(lines.lines[0])
		lines.lines = lines.lines[1:]
	}
}

func logLineSize(line LogLine) int {
	return len(line.Timestamp) + len(line.Stream) + len(line.Message)
}

func parseLogLine(raw, stream string) LogLine {
	line := LogLine{Stream: stream, Message: raw}
	if timestamp, message, found := strings.Cut(raw, " "); found {
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			line.Timestamp, line.Message = timestamp, message
		}
	}
	return line
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
	return m.client.Restart(ctx, id, defaultTimeout)
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
	return m.client.Stop(ctx, id, defaultTimeout)
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
