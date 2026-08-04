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

// imageRemote is the one image server Pilothouse will pull from. It is a
// compile-time constant, never a parameter: the broker must not become a
// way to make the host fetch from an arbitrary URL. It is the same
// simplestreams server the `images:` remote points at.
const imageRemote = "https://images.linuxcontainers.org"

// imageRemoteTimeout bounds the whole remote conversation — alias
// resolution and image metadata. The image transfer itself is performed by
// the Incus daemon rather than by this client, and is bounded by the
// action's own background timeout.
const imageRemoteTimeout = 60 * time.Second

// Instance types Pilothouse will create. Incus accepts these two and the
// action rejects anything else before reaching the API.
const (
	TypeContainer      = "container"
	TypeVirtualMachine = "virtual-machine"
)

// imageAliasLimit bounds the alias an operator may submit. Real aliases
// look like "debian/13" or "ubuntu/24.04/cloud".
const imageAliasLimit = 128

// validImageAlias constrains the alias submitted for creation. The alias is
// resolved against the fixed remote afterwards, so this is a cheap shape
// check rather than the authorisation: it keeps obviously malformed input
// from reaching the network at all, and rejects the "." and ".." path
// segments that have no meaning as an image alias.
func validImageAlias(alias string) bool {
	if len(alias) == 0 || len(alias) > imageAliasLimit {
		return false
	}
	if strings.HasPrefix(alias, "/") || strings.HasSuffix(alias, "/") {
		return false
	}
	for _, segment := range strings.Split(alias, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	for _, character := range alias {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '/' || character == '.' || character == '-' || character == '_':
		default:
			return false
		}
	}
	return true
}

func validInstanceType(value string) bool {
	return value == TypeContainer || value == TypeVirtualMachine
}

// CreateInstance launches a new instance from the fixed image remote. It is
// registered as a background broker action because the daemon must download
// an image, which routinely takes minutes.
//
// Everything the operator supplies is validated here, broker-side, before
// anything reaches the network: the instance name against the same rule
// every other instance action uses, the type against a closed pair, the
// profile against the project's live profile list, and the alias against a
// shape check. The remote itself is never supplied — it is a constant.
func (m *SystemManager) CreateInstance(ctx context.Context, requestedProject, name, alias, instanceType, profile string) error {
	if !validInstanceName(name) {
		return errors.New("invalid instance name")
	}
	if !validInstanceType(instanceType) {
		return errors.New("instance type must be a container or a virtual machine")
	}
	if !validImageAlias(alias) {
		return errors.New("invalid image name")
	}
	project, _, err := m.project(ctx, requestedProject)
	if err != nil {
		return err
	}
	instances, _, err := m.instances(ctx, project)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(instances, func(existing Instance) bool { return existing.Name == name }) {
		return errors.New("an instance with that name already exists")
	}
	// A profile is optional, but a named one must exist: rediscover it
	// rather than trusting the form, exactly as the snapshot actions
	// rediscover a snapshot.
	if profile != "" {
		if !validResourceName(profile) {
			return errors.New("invalid profile name")
		}
		available, err := m.client.Profiles(ctx, project)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(available, func(existing api.Profile) bool { return existing.Name == profile }) {
			return errors.New("profile is not available")
		}
	}
	return m.client.CreateFromImage(ctx, project, name, alias, instanceType, profile)
}

// CreateFromImage resolves the alias on the fixed remote and asks the local
// daemon to create the instance from it.
//
// The alias is resolved explicitly rather than handed to the daemon
// unresolved (which is what the Incus CLI does for a simplestreams remote),
// so an alias that does not exist fails immediately with a clear message
// instead of surfacing later as a failed background operation.
func (c *LocalClient) CreateFromImage(ctx context.Context, project, name, alias, instanceType, profile string) error {
	images, err := incusclient.ConnectSimpleStreams(imageRemote, &incusclient.ConnectionArgs{
		HTTPClient: &http.Client{Timeout: imageRemoteTimeout},
	})
	if err != nil {
		return err
	}
	entry, _, err := images.GetImageAliasType(instanceType, alias)
	if err != nil {
		return errors.New("no " + instanceType + " image named " + alias + " is published on the image server")
	}
	image, _, err := images.GetImage(entry.Target)
	if err != nil {
		return err
	}
	server, err := c.connect(ctx, project)
	if err != nil {
		return err
	}
	request := api.InstancesPost{
		Name: name,
		Source: api.InstanceSource{
			Alias:    alias,
			Protocol: "simplestreams",
			Server:   imageRemote,
			Type:     "image",
		},
		Type: api.InstanceType(instanceType),
	}
	if profile != "" {
		request.Profiles = []string{profile}
	}
	operation, err := server.CreateInstanceFromImage(images, *image, request)
	if err != nil {
		return err
	}
	return waitRemote(ctx, operation)
}

// waitRemote bounds a RemoteOperation by ctx. Unlike Operation, a
// RemoteOperation's Wait takes no context, so a plain Wait would ignore the
// background action's timeout entirely and could hang until the daemon gave
// up on its own. Cancelling the target tells Incus to abandon the transfer
// rather than leaving it running unattended.
func waitRemote(ctx context.Context, operation incusclient.RemoteOperation) error {
	done := make(chan error, 1)
	go func() { done <- operation.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = operation.CancelTarget()
		return ctx.Err()
	}
}
