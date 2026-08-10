package k3s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/k3sconfig"
)

const (
	commandOutputLimit = 8 * 1024 * 1024
	stateTimeout       = 10 * time.Second
)

type Node struct {
	Name    string   `json:"name"`
	Ready   bool     `json:"ready"`
	Roles   []string `json:"roles"`
	Runtime string   `json:"runtime"`
	Version string   `json:"version"`
}

type Namespace struct {
	Failed    int    `json:"failed"`
	Name      string `json:"name"`
	NotReady  int    `json:"not_ready"`
	Pending   int    `json:"pending"`
	Ready     int    `json:"ready"`
	Succeeded int    `json:"succeeded"`
	Total     int    `json:"total"`
}

type State struct {
	Namespaces []Namespace `json:"namespaces"`
	Nodes      []Node      `json:"nodes"`
}

type Manager interface {
	State(context.Context) (State, error)
}

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	output := &boundedOutput{limit: commandOutputLimit}
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if output.truncated {
		return output.Bytes(), fmt.Errorf("command output exceeds %d bytes", commandOutputLimit)
	}
	return output.Bytes(), err
}

type boundedOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	length := len(value)
	remaining := output.limit - output.Len()
	if remaining <= 0 {
		output.truncated = output.truncated || length > 0
		return length, nil
	}
	if length > remaining {
		_, _ = output.Buffer.Write(value[:remaining])
		output.truncated = true
		return length, nil
	}
	_, _ = output.Buffer.Write(value)
	return length, nil
}

type SystemManager struct {
	k3s    string
	runner Runner
}

func NewSystemManager(runner Runner, k3s string) *SystemManager {
	return &SystemManager{runner: runner, k3s: k3s}
}

func (manager *SystemManager) State(ctx context.Context) (State, error) {
	queryCtx, cancel := context.WithTimeout(ctx, stateTimeout)
	defer cancel()

	var nodes nodeList
	if err := manager.runJSON(queryCtx, &nodes, "get", "nodes", "-o", "json"); err != nil {
		return State{}, err
	}
	canonicalNodes, err := canonicalNodes(nodes)
	if err != nil {
		return State{}, err
	}
	var pods podList
	if err := manager.runJSON(queryCtx, &pods, "get", "pods", "--all-namespaces", "-o", "json"); err != nil {
		return State{}, err
	}
	namespaces, err := aggregateNamespaces(pods)
	if err != nil {
		return State{}, err
	}
	return State{Nodes: canonicalNodes, Namespaces: namespaces}, nil
}

func (manager *SystemManager) runJSON(ctx context.Context, target any, args ...string) error {
	commandArgs := append([]string{"kubectl", "--kubeconfig=" + k3sconfig.KubeconfigPath}, args...)
	output, err := manager.runner.Run(ctx, manager.k3s, commandArgs...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("k3s kubectl: %w: %s", err, detail)
		}
		return fmt.Errorf("k3s kubectl: %w", err)
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("decode k3s kubectl response: %w", err)
	}
	return nil
}

type objectMeta struct {
	Labels    map[string]string `json:"labels"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
}

type condition struct {
	Status string `json:"status"`
	Type   string `json:"type"`
}

type nodeList struct {
	APIVersion string `json:"apiVersion"`
	Items      []struct {
		Metadata objectMeta `json:"metadata"`
		Status   struct {
			Conditions []condition `json:"conditions"`
			NodeInfo   struct {
				ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
				KubeletVersion          string `json:"kubeletVersion"`
			} `json:"nodeInfo"`
		} `json:"status"`
	} `json:"items"`
	Kind string `json:"kind"`
}

type podList struct {
	APIVersion string `json:"apiVersion"`
	Items      []struct {
		Metadata objectMeta `json:"metadata"`
		Status   struct {
			Conditions []condition `json:"conditions"`
			Phase      string      `json:"phase"`
		} `json:"status"`
	} `json:"items"`
	Kind string `json:"kind"`
}

func canonicalNodes(raw nodeList) ([]Node, error) {
	if raw.APIVersion != "v1" || raw.Kind != "NodeList" {
		return nil, errors.New("unexpected k3s node response")
	}
	nodes := make([]Node, 0, len(raw.Items))
	for _, item := range raw.Items {
		if item.Metadata.Name == "" {
			return nil, errors.New("k3s node response contains an unnamed node")
		}
		nodes = append(nodes, Node{
			Name: item.Metadata.Name, Ready: conditionTrue(item.Status.Conditions, "Ready"),
			Roles: nodeRoles(item.Metadata.Labels), Runtime: item.Status.NodeInfo.ContainerRuntimeVersion,
			Version: item.Status.NodeInfo.KubeletVersion,
		})
	}
	slices.SortFunc(nodes, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	return nodes, nil
}

func aggregateNamespaces(raw podList) ([]Namespace, error) {
	if raw.APIVersion != "v1" || raw.Kind != "PodList" {
		return nil, errors.New("unexpected k3s pod response")
	}
	byName := make(map[string]*Namespace)
	for _, item := range raw.Items {
		if item.Metadata.Name == "" || item.Metadata.Namespace == "" {
			return nil, errors.New("k3s pod response contains an unnamed pod or namespace")
		}
		namespace := byName[item.Metadata.Namespace]
		if namespace == nil {
			namespace = &Namespace{Name: item.Metadata.Namespace}
			byName[item.Metadata.Namespace] = namespace
		}
		namespace.Total++
		switch item.Status.Phase {
		case "Running":
			if conditionTrue(item.Status.Conditions, "Ready") {
				namespace.Ready++
			} else {
				namespace.NotReady++
			}
		case "Pending":
			namespace.Pending++
		case "Failed":
			namespace.Failed++
		case "Succeeded":
			namespace.Succeeded++
		case "Unknown":
			namespace.NotReady++
		default:
			return nil, fmt.Errorf("k3s pod %s/%s has invalid phase %q", item.Metadata.Namespace, item.Metadata.Name, item.Status.Phase)
		}
	}
	namespaces := make([]Namespace, 0, len(byName))
	for _, namespace := range byName {
		namespaces = append(namespaces, *namespace)
	}
	slices.SortFunc(namespaces, func(a, b Namespace) int { return strings.Compare(a.Name, b.Name) })
	return namespaces, nil
}

func conditionTrue(conditions []condition, name string) bool {
	for _, value := range conditions {
		if value.Type == name {
			return value.Status == "True"
		}
	}
	return false
}

func nodeRoles(labels map[string]string) []string {
	const prefix = "node-role.kubernetes.io/"
	roles := make([]string, 0)
	for key := range labels {
		if role := strings.TrimPrefix(key, prefix); role != key && role != "" {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return []string{"worker"}
	}
	slices.Sort(roles)
	return roles
}
