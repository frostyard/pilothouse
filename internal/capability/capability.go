// Package capability defines the vocabulary of host capabilities that
// pilothoused probes at startup and advertises over the broker's
// QueryCapabilities query (see .mill/spec.md and docs/capabilities.md). It
// defines only the ID/Set model used to encode the query response and to
// gate handler registration in cmd/pilothoused; it contains no probing
// logic itself and is deliberately independent of internal/broker.
package capability

import (
	"encoding/json"
	"slices"
)

// ID is a canonical capability identifier. The eleven values below are the
// complete, fixed vocabulary from the spec; there is no mechanism to
// register additional IDs at runtime.
type ID string

const (
	Systemd             ID = "systemd"
	Journald            ID = "journald"
	Updex               ID = "updex"
	Sysext              ID = "sysext"
	Bootc               ID = "bootc"
	RPMOStree           ID = "rpm-ostree"
	AutoupdateRPMOStree ID = "autoupdate-rpm-ostree"
	AutoupdateBootc     ID = "autoupdate-bootc"
	Podman              ID = "podman"
	Docker              ID = "docker"
	Incus               ID = "incus"
)

// Set holds the capabilities present on a host. The zero value is a valid,
// empty Set: Has and HasAll on a nil or zero-value Set return false without
// panicking, and MarshalJSON on a zero-value Set encodes an empty list.
type Set struct {
	ids map[ID]struct{}
}

// New returns a Set containing the given IDs. Duplicate IDs collapse.
func New(ids ...ID) Set {
	s := Set{ids: make(map[ID]struct{}, len(ids))}
	for _, id := range ids {
		s.ids[id] = struct{}{}
	}
	return s
}

// Has reports whether id is present in the set.
func (s Set) Has(id ID) bool {
	if s.ids == nil {
		return false
	}
	_, ok := s.ids[id]
	return ok
}

// HasAll reports whether every given id is present in the set.
func (s Set) HasAll(ids ...ID) bool {
	for _, id := range ids {
		if !s.Has(id) {
			return false
		}
	}
	return true
}

// List returns the present capability IDs, sorted. It never returns a nil
// slice, so an empty Set produces an empty (not null) JSON array.
func (s Set) List() []ID {
	list := make([]ID, 0, len(s.ids))
	for id := range s.ids {
		list = append(list, id)
	}
	slices.Sort(list)
	return list
}

// MarshalJSON encodes the set as {"capabilities": [...]}: present IDs only,
// sorted, canonical string values. Absent capabilities are never emitted
// (e.g. never as false entries), and an empty Set encodes as
// {"capabilities": []}.
func (s Set) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Capabilities []ID `json:"capabilities"`
	}{Capabilities: s.List()})
}
