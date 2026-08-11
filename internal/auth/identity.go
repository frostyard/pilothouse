package auth

import (
	"fmt"
	"os/user"
	"slices"
	"strconv"
)

type Authenticator interface {
	Authenticate(username, password string) error
}

type Identity struct {
	Admin    bool     `json:"admin"`
	Groups   []string `json:"groups"`
	UID      int      `json:"uid"`
	Username string   `json:"username"`
}

type Resolver interface {
	Resolve(string) (Identity, error)
}

type SystemResolver struct {
	adminGroup    string
	loginGroup    string
	lookupAccount func(string) (accountRecord, error)
	lookupGroup   func(string) (string, error)
}

type accountRecord struct {
	uid      string
	username string
	groupIDs func() ([]string, error)
}

func NewSystemResolver(adminGroup, loginGroup string) *SystemResolver {
	return &SystemResolver{
		adminGroup:    adminGroup,
		loginGroup:    loginGroup,
		lookupAccount: lookupSystemAccount,
		lookupGroup:   lookupSystemGroup,
	}
}

func lookupSystemAccount(username string) (accountRecord, error) {
	account, err := user.Lookup(username)
	if err != nil {
		return accountRecord{}, err
	}
	return accountRecord{
		uid:      account.Uid,
		username: account.Username,
		groupIDs: account.GroupIds,
	}, nil
}

func lookupSystemGroup(groupID string) (string, error) {
	group, err := user.LookupGroupId(groupID)
	if err != nil {
		return "", err
	}
	return group.Name, nil
}

func (r *SystemResolver) Resolve(username string) (Identity, error) {
	account, err := r.lookupAccount(username)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve account: %w", err)
	}
	uidValue, err := strconv.ParseUint(account.uid, 10, strconv.IntSize-1)
	if err != nil {
		return Identity{}, fmt.Errorf("parse account uid: %w", err)
	}
	uid := int(uidValue)
	if uid == 0 {
		return Identity{}, fmt.Errorf("direct root login is disabled")
	}
	groupIDs, err := account.groupIDs()
	if err != nil {
		return Identity{}, fmt.Errorf("resolve account groups: %w", err)
	}
	groups := make([]string, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		group, lookupErr := r.lookupGroup(groupID)
		if lookupErr == nil {
			groups = append(groups, group)
		}
	}
	slices.Sort(groups)
	if r.loginGroup != "" && !slices.Contains(groups, r.loginGroup) {
		return Identity{}, fmt.Errorf("account is not in the %s group", r.loginGroup)
	}
	return Identity{
		Admin:    r.adminGroup != "" && slices.Contains(groups, r.adminGroup),
		Groups:   groups,
		UID:      uid,
		Username: account.username,
	}, nil
}
