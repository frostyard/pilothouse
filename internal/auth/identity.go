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
	adminGroup string
	loginGroup string
}

func NewSystemResolver(adminGroup, loginGroup string) *SystemResolver {
	return &SystemResolver{adminGroup: adminGroup, loginGroup: loginGroup}
}

func (r *SystemResolver) Resolve(username string) (Identity, error) {
	account, err := user.Lookup(username)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve account: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return Identity{}, fmt.Errorf("parse account uid: %w", err)
	}
	if uid == 0 {
		return Identity{}, fmt.Errorf("direct root login is disabled")
	}
	groupIDs, err := account.GroupIds()
	if err != nil {
		return Identity{}, fmt.Errorf("resolve account groups: %w", err)
	}
	groups := make([]string, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		group, lookupErr := user.LookupGroupId(groupID)
		if lookupErr == nil {
			groups = append(groups, group.Name)
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
		Username: account.Username,
	}, nil
}
