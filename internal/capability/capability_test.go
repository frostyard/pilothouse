package capability

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIDConstantsMatchCanonicalStrings(t *testing.T) {
	// The eleven canonical string values from .mill/spec.md, verbatim.
	assert.Equal(t, ID("systemd"), Systemd)
	assert.Equal(t, ID("journald"), Journald)
	assert.Equal(t, ID("updex"), Updex)
	assert.Equal(t, ID("sysext"), Sysext)
	assert.Equal(t, ID("bootc"), Bootc)
	assert.Equal(t, ID("rpm-ostree"), RPMOStree)
	assert.Equal(t, ID("autoupdate-rpm-ostree"), AutoupdateRPMOStree)
	assert.Equal(t, ID("autoupdate-bootc"), AutoupdateBootc)
	assert.Equal(t, ID("podman"), Podman)
	assert.Equal(t, ID("docker"), Docker)
	assert.Equal(t, ID("incus"), Incus)
}

func TestZeroValueSetIsNilSafe(t *testing.T) {
	var zero Set
	assert.False(t, zero.Has(Systemd))
	assert.False(t, zero.HasAll(Systemd))
	assert.False(t, zero.HasAll(Systemd, Podman))
	assert.NotPanics(t, func() {
		zero.Has(Systemd)
		zero.HasAll(Systemd, Journald)
	})
	assert.Empty(t, zero.List())
}

func TestLiteralZeroValueSetIsNilSafe(t *testing.T) {
	// capability.Set{} specifically, per acceptance criteria wording.
	zero := Set{}
	assert.False(t, zero.Has(Podman))
	assert.False(t, zero.HasAll(Podman))
}

func TestSetHasAndHasAll(t *testing.T) {
	s := New(Systemd, Journald, Podman)
	assert.True(t, s.Has(Systemd))
	assert.True(t, s.Has(Journald))
	assert.True(t, s.Has(Podman))
	assert.False(t, s.Has(Docker))
	assert.False(t, s.Has(Incus))

	assert.True(t, s.HasAll(Systemd))
	assert.True(t, s.HasAll(Systemd, Journald))
	assert.True(t, s.HasAll(Systemd, Journald, Podman))
	assert.False(t, s.HasAll(Systemd, Docker))
	assert.False(t, s.HasAll(Docker, Incus))
}

func TestNewCollapsesDuplicates(t *testing.T) {
	s := New(Systemd, Systemd, Journald)
	assert.Equal(t, []ID{Journald, Systemd}, s.List())
}

func TestSetListIsSortedAndNeverNil(t *testing.T) {
	s := New(Podman, Systemd, Docker, Journald)
	assert.Equal(t, []ID{Docker, Journald, Podman, Systemd}, s.List())

	empty := New()
	assert.NotNil(t, empty.List())
	assert.Empty(t, empty.List())
}

func TestSetMarshalJSONShape(t *testing.T) {
	s := New(Sysext, Bootc, Updex)
	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"capabilities":["bootc","sysext","updex"]}`, string(out))
}

func TestEmptySetMarshalsToEmptyArray(t *testing.T) {
	var zero Set
	out, err := json.Marshal(zero)
	require.NoError(t, err)
	assert.JSONEq(t, `{"capabilities":[]}`, string(out))

	out, err = json.Marshal(New())
	require.NoError(t, err)
	assert.JSONEq(t, `{"capabilities":[]}`, string(out))
}

func TestSetRoundTripsThroughJSON(t *testing.T) {
	populated := New(Systemd, Podman, Incus)
	out, err := json.Marshal(populated)
	require.NoError(t, err)

	var decoded Set
	require.NoError(t, json.Unmarshal(out, &decoded))
	assert.Equal(t, populated.List(), decoded.List())

	var zero Set
	out, err = json.Marshal(zero)
	require.NoError(t, err)

	var decodedZero Set
	require.NoError(t, json.Unmarshal(out, &decodedZero))
	assert.Equal(t, zero.List(), decodedZero.List())
}

func TestSetMarshalJSONNeverEmitsFalseEntries(t *testing.T) {
	s := New(Systemd)
	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "false")

	var decoded map[string][]string
	require.NoError(t, json.Unmarshal(out, &decoded))
	assert.Equal(t, []string{"systemd"}, decoded["capabilities"])
}
