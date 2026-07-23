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
	return probeSystemd(ctx, ConnectSystemDBus, systemdMarkerExists)
}

func systemdMarkerExists() bool {
	_, err := os.Stat(systemdMarkerPath)
	return err == nil
}

// probeSystemd is the testable core of ProbeSystemd: connect and
// markerExists are injected so tests can exercise every branch without a
// real system bus or a real /run/systemd/system.
func probeSystemd(ctx context.Context, connect DBusConnector, markerExists func() bool) Set {
	if !markerExists() {
		return New()
	}

	probeCtx, cancel := context.WithTimeout(ctx, dbusProbeTimeout)
	defer cancel()

	conn, ok := dialSystemd(probeCtx, connect)
	if !ok {
		return New()
	}
	defer conn.Close()

	return New(append([]ID{Systemd}, probeAutoupdatePairs(probeCtx, conn)...)...)
}

// dialSystemd attempts the connection and reports whether it succeeded.
// Split out from probeSystemd so the connect-failure and connect-success
// decision is directly unit-testable: a test can hand it a fake connector
// that returns a non-nil *dbus.Conn without that connection ever needing
// to be real enough to answer subsequent D-Bus calls (which do need a live
// bus, and so are exercised only by probeAutoupdatePairs' own fake, and by
// the daemon at real startup).
func dialSystemd(ctx context.Context, connect DBusConnector) (*dbus.Conn, bool) {
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
