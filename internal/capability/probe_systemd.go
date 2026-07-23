package capability

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

// systemdMarkerPath is checked for existence, in addition to a successful
// system D-Bus connection, before the systemd capability is reported
// present. See the spec's probe definitions.
const systemdMarkerPath = "/run/systemd/system"

// dbusProbeTimeout bounds the system D-Bus connection attempt so a hung or
// misbehaving bus can never block daemon startup, mirroring the spec's
// 5-second timeout for command-based probes.
const dbusProbeTimeout = 5 * time.Second

// DBusConnector opens a system D-Bus connection. It is a plain func value
// rather than an interface so the exact same shape --
// func(context.Context) (*dbus.Conn, error) -- can be reused, verbatim, by
// the non-fatal manager-construction helper added in a later chunk
// (services/logs/backups all open the same kind of connection). Kept
// generic here rather than scoped tightly to this probe's needs.
type DBusConnector func(ctx context.Context) (*dbus.Conn, error)

// ConnectSystemDBus is the production DBusConnector: it opens a real
// system bus connection.
func ConnectSystemDBus(ctx context.Context) (*dbus.Conn, error) {
	return dbus.NewSystemConnectionContext(ctx)
}

// unitFileLister is the subset of *dbus.Conn's API the automatic-update
// pair probe needs: unit *file* listing only. It is deliberately narrow --
// exposing nothing that could report LoadState or ActiveState -- so a fake
// used in tests can only ever prove the probe checks file presence, never
// runtime unit state.
type unitFileLister interface {
	ListUnitFilesContext(ctx context.Context) ([]dbus.UnitFile, error)
}

// systemdSession is everything probeSystemd needs from a live connection:
// unit file listing, plus releasing the connection when done. *dbus.Conn
// satisfies it directly (ListUnitFilesContext and Close), so production
// code needs no adapter. Routing probeSystemd's whole post-connect success
// path through this interface -- rather than the concrete *dbus.Conn --
// means a test can fake the entire success path (systemd present, pair
// matching, and the Close call) end-to-end, without ever constructing a
// *dbus.Conn: that concrete struct cannot be faked safely, since calling
// any of its methods (including Close) on a zero-value or nil instance
// panics.
type systemdSession interface {
	unitFileLister
	Close()
}

// sessionOpener opens a systemd D-Bus session, reporting whether the
// connection succeeded. connectSession is the production sessionOpener,
// built from the reusable DBusConnector func value below (also reused
// verbatim by c7's non-fatal manager-construction helper).
type sessionOpener func(ctx context.Context) (systemdSession, bool)

// autoupdatePairs is the exact allowlist from the spec: each automatic-
// update capability is present only when BOTH its timer and its service
// unit file are known to systemd.
var autoupdatePairs = map[ID][2]string{
	AutoupdateRPMOStree: {"rpm-ostreed-automatic.timer", "rpm-ostreed-automatic.service"},
	AutoupdateBootc:     {"bootc-fetch-apply-updates.timer", "bootc-fetch-apply-updates.service"},
}

// ProbeSystemd probes systemd and, sharing that same D-Bus connection, the
// automatic-update capability pairs. It never returns an error: any
// failure (missing marker file, unreachable D-Bus, a ListUnitFilesContext
// error) simply narrows the result, matching every other probe in this
// package -- systemd absence is never fatal to daemon startup.
func ProbeSystemd(ctx context.Context) Set {
	return probeSystemd(ctx, connectSession, systemdMarkerExists)
}

func systemdMarkerExists() bool {
	_, err := os.Stat(systemdMarkerPath)
	return err == nil
}

// connectSession is the production sessionOpener: it dials the real system
// bus via ConnectSystemDBus/DBusConnector and hands back the resulting
// *dbus.Conn as a systemdSession.
func connectSession(ctx context.Context) (systemdSession, bool) {
	return dialSystemd(ctx, ConnectSystemDBus)
}

// probeSystemd is the testable core of ProbeSystemd: open and markerExists
// are injected so tests can exercise every branch -- including a
// successful connection and its resulting pair matching -- without a real
// system bus or a real /run/systemd/system.
func probeSystemd(ctx context.Context, open sessionOpener, markerExists func() bool) Set {
	if !markerExists() {
		return New()
	}

	probeCtx, cancel := context.WithTimeout(ctx, dbusProbeTimeout)
	defer cancel()

	session, ok := open(probeCtx)
	if !ok {
		return New()
	}
	defer session.Close()

	return New(append([]ID{Systemd}, probeAutoupdatePairs(probeCtx, session)...)...)
}

// dialSystemd attempts the connection and reports whether it succeeded,
// handing back the *dbus.Conn as a systemdSession on success. Split out so
// the connect-failure/connect-success decision is directly unit-testable
// independent of connectSession's use of the real ConnectSystemDBus.
func dialSystemd(ctx context.Context, connect DBusConnector) (systemdSession, bool) {
	conn, err := connect(ctx)
	if err != nil {
		return nil, false
	}
	return conn, true
}

// probeAutoupdatePairs reports which automatic-update capability pairs
// have both unit files known to systemd, per lister.ListUnitFilesContext.
// It never inspects LoadState or ActiveState -- unit file existence is all
// the spec asks for -- and a ListUnitFilesContext error yields no pairs
// rather than propagating a fatal error.
func probeAutoupdatePairs(ctx context.Context, lister unitFileLister) []ID {
	files, err := lister.ListUnitFilesContext(ctx)
	if err != nil {
		return nil
	}

	known := make(map[string]struct{}, len(files))
	for _, f := range files {
		known[filepath.Base(f.Path)] = struct{}{}
	}

	ids := make([]ID, 0, len(autoupdatePairs))
	for id, pair := range autoupdatePairs {
		if _, ok := known[pair[0]]; !ok {
			continue
		}
		if _, ok := known[pair[1]]; !ok {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
