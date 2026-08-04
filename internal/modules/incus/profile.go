package incus

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
)

// Profile is the list-level model for one Incus profile.
type Profile struct {
	Description string `json:"description,omitempty"`
	Devices     int    `json:"devices"`
	Name        string `json:"name"`
	UsedBy      int    `json:"used_by"`
}

// ProfileDetail is the per-profile model behind broker.QueryIncusProfile.
//
// A profile carries exactly the same configuration and device shape as an
// instance — including the same `user.*`, `environment.*` and `raw.*`
// namespaces — so it reuses the instance allowlists (configKeys,
// configPrefixes, deviceProperties) rather than defining its own. A profile
// is arguably the more sensitive of the two: its cloud-init payload applies
// to every instance that inherits it.
type ProfileDetail struct {
	Config      []ConfigEntry `json:"config"`
	Description string        `json:"description,omitempty"`
	Devices     []Device      `json:"devices"`
	Name        string        `json:"name"`
	Project     string        `json:"project"`
	UsedBy      []string      `json:"used_by"`
}

func profiles(raw []api.Profile) []Profile {
	values := make([]Profile, 0, len(raw))
	for _, item := range raw {
		values = append(values, Profile{
			Description: item.Description, Devices: len(item.Devices),
			Name: item.Name, UsedBy: len(item.UsedBy),
		})
	}
	slices.SortFunc(values, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return values
}

// ProfileDetail returns one profile's allowlisted configuration and devices.
func (m *SystemManager) ProfileDetail(ctx context.Context, requestedProject, name string) (ProfileDetail, error) {
	if !validResourceName(name) {
		return ProfileDetail{}, errors.New("invalid profile name")
	}
	project, _, err := m.project(ctx, requestedProject)
	if err != nil {
		return ProfileDetail{}, err
	}
	item, err := m.client.Profile(ctx, project, name)
	if err != nil {
		return ProfileDetail{}, err
	}
	if item == nil {
		return ProfileDetail{}, errors.New("profile no longer exists")
	}
	detail := ProfileDetail{
		// allowedConfig merges an expanded and a local map; a profile has
		// only the one, so it is passed as both halves of the merge.
		Config: allowedConfig(item.Config, item.Config), Description: item.Description,
		Devices: allowedDevices(item.Devices, item.Devices), Name: item.Name,
		Project: project, UsedBy: item.UsedBy,
	}
	if detail.UsedBy == nil {
		detail.UsedBy = []string{}
	}
	return detail, nil
}
