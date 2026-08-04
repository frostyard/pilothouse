package incus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateInstanceSubmitsResolvedRequest(t *testing.T) {
	client := stateClient()
	require.NoError(t, NewSystemManager(client).CreateInstance(
		context.Background(), "production", "web-01", "debian/13", TypeContainer, "default"))
	assert.Equal(t, "create production web-01 debian/13 container profile=default", client.actions[len(client.actions)-1])

	// A profile is optional.
	client = stateClient()
	require.NoError(t, NewSystemManager(client).CreateInstance(
		context.Background(), "production", "web-01", "debian/13", TypeVirtualMachine, ""))
	assert.Equal(t, "create production web-01 debian/13 virtual-machine profile=", client.actions[len(client.actions)-1])
}

// TestCreateInstanceRejectsDuplicateName proves creation cannot collide
// with an existing instance; "api" is in the fixture.
func TestCreateInstanceRejectsDuplicateName(t *testing.T) {
	client := stateClient()
	err := NewSystemManager(client).CreateInstance(
		context.Background(), "production", "api", "debian/13", TypeContainer, "")
	assert.EqualError(t, err, "an instance with that name already exists")
	assertNoCreate(t, client)
}

func TestCreateInstanceValidatesName(t *testing.T) {
	for _, name := range []string{"", "-api", "api-", "API", "api/default", "api.local", "../api", strings.Repeat("a", 64)} {
		client := stateClient()
		err := NewSystemManager(client).CreateInstance(
			context.Background(), "production", name, "debian/13", TypeContainer, "")
		assert.EqualError(t, err, "invalid instance name", "name %q", name)
		assertNoCreate(t, client)
	}
}

// TestCreateInstanceValidatesType proves the type is a closed pair checked
// before anything reaches the network.
func TestCreateInstanceValidatesType(t *testing.T) {
	for _, value := range []string{"", "vm", "Container", "CONTAINER", "virtual machine", "any"} {
		client := stateClient()
		err := NewSystemManager(client).CreateInstance(
			context.Background(), "production", "web-01", "debian/13", value, "")
		assert.EqualError(t, err, "instance type must be a container or a virtual machine", "type %q", value)
		assertNoCreate(t, client)
	}
	assert.True(t, validInstanceType(TypeContainer))
	assert.True(t, validInstanceType(TypeVirtualMachine))
}

// TestCreateInstanceValidatesImageAlias proves a malformed alias is
// rejected before any network call. The alias is resolved against the fixed
// remote afterwards, so this is a shape check rather than the
// authorisation — but it must still reject path segments that have no
// meaning as an alias.
func TestCreateInstanceValidatesImageAlias(t *testing.T) {
	for _, alias := range []string{
		"", "/debian", "debian/", "debian//13", "debian/../../etc/passwd",
		"debian/./13", "debian 13", "debian:13", "debian?a=b", "debian#13",
		strings.Repeat("a", 129),
	} {
		client := stateClient()
		err := NewSystemManager(client).CreateInstance(
			context.Background(), "production", "web-01", alias, TypeContainer, "")
		assert.EqualError(t, err, "invalid image name", "alias %q", alias)
		assertNoCreate(t, client)
	}

	for _, alias := range []string{"debian/13", "ubuntu/24.04/cloud", "alpine/edge", "images_test-1"} {
		assert.True(t, validImageAlias(alias), alias)
	}
}

// TestCreateInstanceRediscoverstheProfile proves a named profile is
// resolved against the project's live profile list rather than trusted.
func TestCreateInstanceRediscoversTheProfile(t *testing.T) {
	client := stateClient()
	err := NewSystemManager(client).CreateInstance(
		context.Background(), "production", "web-01", "debian/13", TypeContainer, "missing-profile")
	assert.EqualError(t, err, "profile is not available")
	assertNoCreate(t, client)

	client = stateClient()
	err = NewSystemManager(client).CreateInstance(
		context.Background(), "production", "web-01", "debian/13", TypeContainer, "../default")
	assert.EqualError(t, err, "invalid profile name")
	assertNoCreate(t, client)
}

func TestCreateInstanceValidatesProject(t *testing.T) {
	client := stateClient()
	err := NewSystemManager(client).CreateInstance(
		context.Background(), "missing", "web-01", "debian/13", TypeContainer, "")
	assert.EqualError(t, err, "project is not available")
	assertNoCreate(t, client)
}

func TestCreateInstancePropagatesFailure(t *testing.T) {
	client := stateClient()
	client.createError = errors.New("no container image named debian/99 is published on the image server")
	err := NewSystemManager(client).CreateInstance(
		context.Background(), "production", "web-01", "debian/99", TypeContainer, "")
	assert.EqualError(t, err, "no container image named debian/99 is published on the image server")
}

// TestImageRemoteIsAConstant is the guard against the one change that would
// turn this action into a generic fetcher: the remote must never become a
// parameter. CreateInstance takes no remote argument at all, and the
// constant points at the public image server.
func TestImageRemoteIsAConstant(t *testing.T) {
	assert.Equal(t, "https://images.linuxcontainers.org", imageRemote)
	assert.True(t, strings.HasPrefix(imageRemote, "https://"), "the image remote must be HTTPS")
}

func assertNoCreate(t *testing.T, client *fakeClient) {
	t.Helper()
	for _, action := range client.actions {
		assert.False(t, strings.HasPrefix(action, "create "),
			"no create call may reach the client, got %q", action)
	}
}
