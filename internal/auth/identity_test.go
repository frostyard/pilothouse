package auth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSystemResolver(t *testing.T) {
	resolver := NewSystemResolver("wheel", "operators")

	assert.Equal(t, "wheel", resolver.adminGroup)
	assert.Equal(t, "operators", resolver.loginGroup)
	assert.NotNil(t, resolver.lookupAccount)
	assert.NotNil(t, resolver.lookupGroup)
}

func TestSystemResolverResolve(t *testing.T) {
	var lookedUpUsername string
	var lookedUpGroupIDs []string
	resolver := &SystemResolver{
		adminGroup: "wheel",
		loginGroup: "operators",
		lookupAccount: func(username string) (accountRecord, error) {
			lookedUpUsername = username
			return accountRecord{
				uid:      "1001",
				username: "resolved-name",
				groupIDs: func() ([]string, error) {
					return []string{"30", "10", "20", "missing"}, nil
				},
			}, nil
		},
		lookupGroup: func(groupID string) (string, error) {
			lookedUpGroupIDs = append(lookedUpGroupIDs, groupID)
			groups := map[string]string{
				"10": "wheel",
				"20": "operators",
				"30": "audio",
			}
			name, ok := groups[groupID]
			if !ok {
				return "", errors.New("group not found")
			}
			return name, nil
		},
	}

	identity, err := resolver.Resolve("requested-name")

	require.NoError(t, err)
	assert.Equal(t, "requested-name", lookedUpUsername)
	assert.Equal(t, []string{"30", "10", "20", "missing"}, lookedUpGroupIDs)
	assert.Equal(t, Identity{
		Admin:    true,
		Groups:   []string{"audio", "operators", "wheel"},
		UID:      1001,
		Username: "resolved-name",
	}, identity)
}

func TestSystemResolverResolveWithoutConfiguredGroups(t *testing.T) {
	resolver := testSystemResolver(accountRecord{
		uid:      "1001",
		username: "snow",
		groupIDs: func() ([]string, error) {
			return []string{"10"}, nil
		},
	}, map[string]string{"10": "wheel"})

	identity, err := resolver.Resolve("snow")

	require.NoError(t, err)
	assert.False(t, identity.Admin)
	assert.Equal(t, []string{"wheel"}, identity.Groups)
}

func TestSystemResolverDoesNotGrantAdminWithoutMembership(t *testing.T) {
	resolver := testSystemResolver(accountRecord{
		uid:      "1001",
		username: "snow",
		groupIDs: func() ([]string, error) {
			return []string{"10"}, nil
		},
	}, map[string]string{"10": "users"})
	resolver.adminGroup = "wheel"

	identity, err := resolver.Resolve("snow")

	require.NoError(t, err)
	assert.False(t, identity.Admin)
	assert.Equal(t, []string{"users"}, identity.Groups)
}

func TestSystemResolverRejectsAccountOutsideLoginGroup(t *testing.T) {
	resolver := testSystemResolver(accountRecord{
		uid:      "1001",
		username: "snow",
		groupIDs: func() ([]string, error) {
			return []string{"10"}, nil
		},
	}, map[string]string{"10": "users"})
	resolver.loginGroup = "operators"

	identity, err := resolver.Resolve("snow")

	assert.EqualError(t, err, "account is not in the operators group")
	assert.Zero(t, identity)
}

func TestSystemResolverWrapsAccountLookupError(t *testing.T) {
	lookupErr := errors.New("identity service unavailable")
	resolver := &SystemResolver{
		lookupAccount: func(string) (accountRecord, error) {
			return accountRecord{}, lookupErr
		},
		lookupGroup: func(string) (string, error) {
			t.Fatal("group lookup must not run after account lookup fails")
			return "", nil
		},
	}

	identity, err := resolver.Resolve("snow")

	require.Error(t, err)
	assert.ErrorIs(t, err, lookupErr)
	assert.ErrorContains(t, err, "resolve account")
	assert.Zero(t, identity)
}

func TestSystemResolverRejectsInvalidUID(t *testing.T) {
	for _, uid := range []string{"not-a-uid", "-1", "18446744073709551616"} {
		t.Run(uid, func(t *testing.T) {
			groupIDsCalled := false
			resolver := testSystemResolver(accountRecord{
				uid:      uid,
				username: "snow",
				groupIDs: func() ([]string, error) {
					groupIDsCalled = true
					return nil, nil
				},
			}, nil)

			identity, err := resolver.Resolve("snow")

			require.Error(t, err)
			assert.ErrorContains(t, err, "parse account uid")
			assert.False(t, groupIDsCalled)
			assert.Zero(t, identity)
		})
	}
}

func TestSystemResolverRejectsRootBeforeResolvingGroups(t *testing.T) {
	groupIDsCalled := false
	resolver := testSystemResolver(accountRecord{
		uid:      "0",
		username: "root",
		groupIDs: func() ([]string, error) {
			groupIDsCalled = true
			return nil, nil
		},
	}, nil)

	identity, err := resolver.Resolve("root")

	assert.EqualError(t, err, "direct root login is disabled")
	assert.False(t, groupIDsCalled)
	assert.Zero(t, identity)
}

func TestSystemResolverWrapsGroupResolutionError(t *testing.T) {
	groupErr := errors.New("group service unavailable")
	groupLookupCalled := false
	resolver := testSystemResolver(accountRecord{
		uid:      "1001",
		username: "snow",
		groupIDs: func() ([]string, error) {
			return nil, groupErr
		},
	}, nil)
	resolver.lookupGroup = func(string) (string, error) {
		groupLookupCalled = true
		return "", nil
	}

	identity, err := resolver.Resolve("snow")

	require.Error(t, err)
	assert.ErrorIs(t, err, groupErr)
	assert.ErrorContains(t, err, "resolve account groups")
	assert.False(t, groupLookupCalled)
	assert.Zero(t, identity)
}

func testSystemResolver(account accountRecord, groups map[string]string) *SystemResolver {
	return &SystemResolver{
		lookupAccount: func(string) (accountRecord, error) {
			return account, nil
		},
		lookupGroup: func(groupID string) (string, error) {
			name, ok := groups[groupID]
			if !ok {
				return "", errors.New("group not found")
			}
			return name, nil
		},
	}
}
