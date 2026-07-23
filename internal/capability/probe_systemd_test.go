package capability

import (
	"context"
	"errors"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/stretchr/testify/assert"
)

// fakeUnitFileLister implements unitFileLister and nothing else -- it
// cannot expose LoadState or ActiveState because it has no such fields or
// methods, which is what proves the pair probe only ever checks unit file
// presence.
type fakeUnitFileLister struct {
	files []dbus.UnitFile
	err   error
}

func (f fakeUnitFileLister) ListUnitFilesContext(context.Context) ([]dbus.UnitFile, error) {
	return f.files, f.err
}

func unitFile(name string) dbus.UnitFile {
	return dbus.UnitFile{Path: "/usr/lib/systemd/system/" + name, Type: "enabled"}
}

// fakeSystemdSession implements systemdSession end-to-end (unit file
// listing plus Close) entirely with fakes -- no *dbus.Conn is ever
// constructed, so probeSystemd's whole success path is reachable from a
// test, unlike a fake that only satisfies DBusConnector's
// func(context.Context) (*dbus.Conn, error) shape (a zero-value *dbus.Conn
// panics as soon as any of its methods, including Close, are called).
type fakeSystemdSession struct {
	fakeUnitFileLister
	closed *bool
}

func (f fakeSystemdSession) Close() {
	if f.closed != nil {
		*f.closed = true
	}
}

func TestSystemdAbsentWhenMarkerMissing(t *testing.T) {
	openCalled := false
	s := probeSystemd(context.Background(),
		func(context.Context) (systemdSession, bool) {
			openCalled = true
			return nil, false
		},
		func() bool { return false },
	)

	assert.False(t, s.Has(Systemd))
	assert.Empty(t, s.List())
	assert.False(t, openCalled, "connect must not be attempted when the marker file is absent")
}

func TestSystemdAbsentWhenConnectionFails(t *testing.T) {
	s := probeSystemd(context.Background(),
		func(context.Context) (systemdSession, bool) {
			return nil, false
		},
		func() bool { return true },
	)

	assert.False(t, s.Has(Systemd))
	assert.Empty(t, s.List())
}

func TestDialSystemdReportsConnectFailure(t *testing.T) {
	session, ok := dialSystemd(context.Background(), func(context.Context) (*dbus.Conn, error) {
		return nil, errors.New("dial unix @/run/dbus/system_bus_socket: connect: no such file or directory")
	})

	assert.False(t, ok)
	assert.Nil(t, session)
}

func TestSystemdPresentWhenMarkerExistsAndConnectionSucceeds(t *testing.T) {
	closed := false
	session := fakeSystemdSession{
		fakeUnitFileLister: fakeUnitFileLister{files: []dbus.UnitFile{
			unitFile("rpm-ostreed-automatic.timer"),
			unitFile("rpm-ostreed-automatic.service"),
		}},
		closed: &closed,
	}

	s := probeSystemd(context.Background(),
		func(context.Context) (systemdSession, bool) {
			return session, true
		},
		func() bool { return true },
	)

	assert.True(t, s.Has(Systemd))
	assert.True(t, s.Has(AutoupdateRPMOStree), "pair matching must run against the same successful session")
	assert.False(t, s.Has(AutoupdateBootc))
	assert.True(t, closed, "the session must be closed once the probe is done with it")
}

func TestAutoupdatePairPresentOnlyWhenBothUnitFilesKnown(t *testing.T) {
	tests := []struct {
		name  string
		files []dbus.UnitFile
		want  []ID
	}{
		{
			name:  "neither unit file present",
			files: nil,
			want:  nil,
		},
		{
			name:  "only the timer present",
			files: []dbus.UnitFile{unitFile("rpm-ostreed-automatic.timer")},
			want:  nil,
		},
		{
			name:  "only the service present",
			files: []dbus.UnitFile{unitFile("rpm-ostreed-automatic.service")},
			want:  nil,
		},
		{
			name: "both rpm-ostree unit files present",
			files: []dbus.UnitFile{
				unitFile("rpm-ostreed-automatic.timer"),
				unitFile("rpm-ostreed-automatic.service"),
			},
			want: []ID{AutoupdateRPMOStree},
		},
		{
			name: "both bootc unit files present",
			files: []dbus.UnitFile{
				unitFile("bootc-fetch-apply-updates.timer"),
				unitFile("bootc-fetch-apply-updates.service"),
			},
			want: []ID{AutoupdateBootc},
		},
		{
			name: "both pairs present independently, plus unrelated units",
			files: []dbus.UnitFile{
				unitFile("rpm-ostreed-automatic.timer"),
				unitFile("rpm-ostreed-automatic.service"),
				unitFile("bootc-fetch-apply-updates.timer"),
				unitFile("bootc-fetch-apply-updates.service"),
				unitFile("sshd.service"),
			},
			want: []ID{AutoupdateBootc, AutoupdateRPMOStree},
		},
		{
			name: "one pair complete, the other missing its service",
			files: []dbus.UnitFile{
				unitFile("rpm-ostreed-automatic.timer"),
				unitFile("rpm-ostreed-automatic.service"),
				unitFile("bootc-fetch-apply-updates.timer"),
			},
			want: []ID{AutoupdateRPMOStree},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := probeAutoupdatePairs(context.Background(), fakeUnitFileLister{files: tt.files})
			assert.ElementsMatch(t, tt.want, ids)
		})
	}
}

func TestAutoupdatePairsEmptyOnListUnitFilesError(t *testing.T) {
	ids := probeAutoupdatePairs(context.Background(), fakeUnitFileLister{
		err: errors.New("dbus: connection closed"),
	})
	assert.Empty(t, ids)
}
