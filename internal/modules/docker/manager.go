package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const (
	defaultTimeout = 10
	logsTailLines  = 200
	logsMaxBytes   = 256 * 1024
)

type Container struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	State   string `json:"state"`
	Status  string `json:"status"`
	tty     bool
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

type State struct {
	Containers []Container `json:"containers"`
	Images     []Image     `json:"images"`
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
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error)
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

func (m *SystemManager) Logs(ctx context.Context, id string) (Logs, error) {
	container, err := m.container(ctx, id)
	if err != nil {
		return Logs{}, err
	}
	stream, err := m.client.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: fmt.Sprint(logsTailLines), Follow: false,
	})
	if err != nil {
		return Logs{}, err
	}
	defer func() { _ = stream.Close() }()

	result := Logs{ID: id, Name: container.Name}
	if container.tty {
		lines := newLogLines()
		lines.read(stream, "stdout")
		result.Lines = lines.lines
	} else {
		result.Lines = readMultiplexedLogLines(stream)
	}
	return result, nil
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
	timeout := defaultTimeout
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
	timeout := defaultTimeout
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
			Running: item.State.Running, State: string(item.State.Status), Status: status, tty: item.Config.Tty,
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
