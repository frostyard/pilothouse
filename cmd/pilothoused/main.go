package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/auth/pam"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/backups"
	"github.com/frostyard/pilothouse/internal/modules/docker"
	"github.com/frostyard/pilothouse/internal/modules/files"
	"github.com/frostyard/pilothouse/internal/modules/incus"
	"github.com/frostyard/pilothouse/internal/modules/logs"
	logjournal "github.com/frostyard/pilothouse/internal/modules/logs/journal"
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/podman"
	"github.com/frostyard/pilothouse/internal/modules/services"
	servicejournal "github.com/frostyard/pilothouse/internal/modules/services/journal"
	"github.com/frostyard/pilothouse/internal/modules/storage"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	dockerclient "github.com/moby/moby/client"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	adminGroup := flag.String("admin-group", "sudo", "system group allowed to perform privileged actions")
	auditDB := flag.String("audit-db", "/var/lib/pilothouse/audit.db", "durable action audit database")
	jobsDB := flag.String("jobs-db", "/var/lib/pilothouse/jobs.db", "durable maintenance job database")
	backupMaxAge := flag.Duration("backup-max-age", 48*time.Hour, "maximum acceptable age of a successful configured backup")
	var backupTimers stringListFlag
	flag.Var(&backupTimers, "backup-timer", "exact systemd backup timer to monitor; repeatable")
	definitionsRoot := flag.String("definitions-root", "", "custom root containing sysupdate definition directories")
	var filesRoots files.RootFlags
	flag.Var(filesRoots.Flag(false), "files-root", "read-only files root as id=absolute-path; repeatable")
	flag.Var(filesRoots.Flag(true), "files-write-root", "writable files root as id=absolute-path; repeatable")
	loginGroup := flag.String("login-group", "", "optional system group allowed to log in")
	pamService := flag.String("pam-service", "pilothouse", "PAM service name")
	podmanSocket := flag.String("podman-socket", "/run/podman/podman.sock", "Podman API Unix socket path")
	socket := flag.String("socket", "/run/pilothouse/broker.sock", "Unix socket path")
	socketGroup := flag.String("socket-group", "pilothouse", "group allowed to connect to the broker")
	updex := flag.String("updex", "updex", "path to the updex executable")
	flag.Parse()
	backupTimers.addCommaSeparated(os.Getenv("PILOTHOUSE_BACKUP_TIMERS"))
	if os.Geteuid() != 0 {
		return fmt.Errorf("pilothoused must run as root")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := os.MkdirAll(filepath.Dir(*auditDB), 0o750); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(*jobsDB), 0o750); err != nil {
		return fmt.Errorf("create jobs directory: %w", err)
	}
	auditStore, err := audit.Open(*auditDB, 10_000)
	if err != nil {
		return err
	}
	defer func() { _ = auditStore.Close() }()
	actions := broker.NewActionRegistry(auditStore)
	streamActions := broker.NewStreamActionRegistry(auditStore)
	jobStore, err := jobs.Open(*jobsDB, 1_000)
	if err != nil {
		return err
	}
	defer func() { _ = jobStore.Close() }()
	actions.UseJobs(jobStore)
	queries := broker.NewQueryRegistry()
	streamQueries := broker.NewStreamQueryRegistry()
	caps := capability.Probe(context.Background(), capability.Config{PodmanSocket: *podmanSocket, Updex: *updex})
	if err := registerCapabilities(queries, caps); err != nil {
		return err
	}
	filesManager, err := files.NewSystemManager(filesRoots.Specs())
	if err != nil {
		return err
	}
	defer func() { _ = filesManager.Close() }()
	if err := registerFiles(queries, streamQueries, streamActions, filesManager); err != nil {
		return err
	}
	if err := registerActivity(queries, auditStore); err != nil {
		return err
	}
	if err := registerJobs(queries, jobStore); err != nil {
		return err
	}
	storageManager, err := newStorageManager(storage.NewOptionalToolResolver(), "/")
	if err != nil {
		return fmt.Errorf("resolve storage tools: %w", err)
	}
	// QueryStorageState is registered against the plain storageManager, which
	// has no systemd dependency, so storage inventory reads work regardless
	// of whether systemd is present -- see buildSystemdManagers below for the
	// remote-mount controller, which does depend on systemd.
	if err := registerStorage(queries, storageManager); err != nil {
		return err
	}
	// Flag misconfiguration (bad timer name, non-positive max age) is a real
	// startup error and fails run() regardless of systemd's presence; only
	// the D-Bus reachability failure mode below is non-fatal.
	if err := backups.ValidateConfiguration(backupTimers, *backupMaxAge); err != nil {
		return err
	}
	systemdConnectCtx, systemdConnectCancel := context.WithTimeout(context.Background(), systemdConnectTimeout)
	defer systemdConnectCancel()
	systemdClient := connectSystemd(systemdConnectCtx, caps, func(ctx context.Context) (*dbus.Conn, error) {
		return dbus.NewSystemConnectionContext(ctx)
	}, logger)
	if systemdClient != nil {
		defer systemdClient.Close()
	}
	managers, err := buildSystemdManagers(systemdClient, storageManager, backupTimers, *backupMaxAge, servicejournal.New(), logjournal.New())
	if err != nil {
		return err
	}
	if err := registerStorageActions(actions, managers.remoteManager, caps); err != nil {
		return err
	}
	if err := registerBackups(queries, managers.backupManager, caps); err != nil {
		return err
	}
	if err := registerServices(actions, queries, managers.servicesManager, caps); err != nil {
		return err
	}
	if err := registerLogs(queries, managers.logsManager, caps); err != nil {
		return err
	}
	sysextManager := sysext.NewSystemManager(sysext.ExecRunner{}, *definitionsRoot, *updex)
	if err := registerSysextActions(actions, sysextManager, caps); err != nil {
		return err
	}
	maintenanceManager := maintenance.NewSystemManager(sysextManager, jobStore, sysext.ExecRunner{}, "/", caps.Has(capability.Updex), caps.Has(capability.Sysext))
	if err := registerMaintenance(actions, queries, maintenanceManager, caps); err != nil {
		return err
	}
	// The host-image reporter is constructed unconditionally (it runs nothing
	// until queried) and told which of its two sources the probe actually
	// found; registerHostImage's own guard decides whether the query is
	// exposed at all.
	hostImageManager := maintenance.NewHostImageManager(sysext.ExecRunner{}, caps.Has(capability.Bootc), caps.Has(capability.RPMOStree))
	if err := registerHostImage(queries, hostImageManager, caps); err != nil {
		return err
	}
	podmanClient := podman.NewAPIClient(*podmanSocket)
	defer podmanClient.Close()
	if err := registerPodman(actions, queries, podman.NewSystemManager(podmanClient), caps); err != nil {
		return err
	}
	dockerClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		// Construction failure (e.g. a malformed DOCKER_HOST) is treated the
		// same as an unreachable engine: the docker capability was already
		// probed above (capability.Probe never fails fatally), so this is
		// never a reason to abort daemon startup -- just leave docker
		// unregistered.
		logger.Warn("docker client unavailable; docker capability disabled", "error", err)
	} else {
		defer func() { _ = dockerClient.Close() }()
		if err := registerDocker(actions, queries, docker.NewSystemManager(dockerClient), caps); err != nil {
			return err
		}
	}
	incusClient := incus.NewLocalClient()
	if err := registerIncus(actions, queries, incus.NewSystemManager(incusClient), caps); err != nil {
		return err
	}
	sessions := broker.NewSessionStore(15*time.Minute, 8*time.Hour)
	handler := broker.NewServer(
		pam.NewAuthenticator(*pamService),
		auth.NewSystemResolver(*adminGroup, *loginGroup),
		sessions,
		actions,
		queries,
		streamActions,
		streamQueries,
		logger,
	)
	listener, err := listenUnix(*socket, *socketGroup)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(*socket)
	}()

	server := &http.Server{
		Handler:           handler.Handler(),
		IdleTimeout:       30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("privileged broker listening", "admin_group", *adminGroup, "login_group", *loginGroup, "socket", *socket)
	serveErr := server.Serve(listener)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer waitCancel()
	_ = actions.Shutdown(waitCtx)
	if serveErr != nil && serveErr != http.ErrServerClosed {
		return fmt.Errorf("serve broker: %w", serveErr)
	}
	return nil
}

func newStorageManager(resolve storage.ToolResolver, root string) (*storage.SystemManager, error) {
	tools, err := storage.NewToolsetWithResolver(resolve)
	if err != nil {
		return nil, err
	}
	optional := func(name string, candidates []string, newEnricher func(string) storage.Enricher) (storage.Enricher, error) {
		path, present, err := resolve(candidates)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", name, err)
		}
		if !present {
			return storage.NewUnsupportedEnricher(name), nil
		}
		return newEnricher(path), nil
	}
	all := func(name string, candidates [][]string, newEnricher func([]string) storage.Enricher) (storage.Enricher, error) {
		paths := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			path, present, err := resolve(candidate)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", name, err)
			}
			if !present {
				return storage.NewUnsupportedEnricher(name), nil
			}
			paths = append(paths, path)
		}
		return newEnricher(paths), nil
	}
	smart, err := optional("smart", []string{"/usr/sbin/smartctl", "/sbin/smartctl"}, storage.NewSMARTEnricher)
	if err != nil {
		return nil, err
	}
	mdraid, err := optional("mdraid", []string{"/usr/sbin/mdadm", "/sbin/mdadm"}, func(path string) storage.Enricher { return storage.NewMDRAIDEnricher(root, path) })
	if err != nil {
		return nil, err
	}
	lvm, err := all("lvm", [][]string{{"/usr/sbin/pvs", "/sbin/pvs"}, {"/usr/sbin/vgs", "/sbin/vgs"}, {"/usr/sbin/lvs", "/sbin/lvs"}}, func(paths []string) storage.Enricher {
		return storage.NewLVMEnricher(storage.LVMTools{PVS: paths[0], VGS: paths[1], LVS: paths[2]})
	})
	if err != nil {
		return nil, err
	}
	deviceMapper, err := optional("device-mapper", []string{"/usr/sbin/dmsetup", "/sbin/dmsetup", "/usr/bin/dmsetup", "/bin/dmsetup"}, storage.NewDeviceMapperEnricher)
	if err != nil {
		return nil, err
	}
	multipath, err := optional("multipath", []string{"/usr/sbin/multipathd", "/sbin/multipathd"}, storage.NewMultipathEnricher)
	if err != nil {
		return nil, err
	}
	zfs, err := all("zfs", [][]string{{"/usr/sbin/zpool", "/sbin/zpool"}, {"/usr/sbin/zfs", "/sbin/zfs"}}, func(paths []string) storage.Enricher {
		return storage.NewZFSEnricher(storage.ZFSTools{ZPool: paths[0], ZFS: paths[1]})
	})
	if err != nil {
		return nil, err
	}
	btrfs, err := optional("btrfs", []string{"/usr/bin/btrfs", "/bin/btrfs"}, storage.NewBtrfsEnricher)
	if err != nil {
		return nil, err
	}
	return storage.NewSystemManagerWithEnrichers([]storage.Adapter{storage.NewBlockAdapter(tools.LSBLK), storage.NewMountAdapter(tools.Findmnt)}, []storage.Enricher{smart, mdraid, lvm, deviceMapper, multipath, zfs, btrfs}), nil
}

// systemdConnectTimeout bounds the second, "real" system D-Bus dial that
// connectSystemd performs after capability.Probe has already reported
// Systemd present. It intentionally mirrors capability.Probe's own
// dbusProbeTimeout: the probe's bounded context only proves the bus was
// reachable at probe time, so reusing context.Background() here would
// reintroduce an unbounded-hang risk if the bus wedges between the probe
// and this call -- exactly the failure mode the probe's timeout exists to
// rule out.
const systemdConnectTimeout = 5 * time.Second

// connectSystemd opens the system D-Bus connection used by every
// systemd-backed manager, but only when the probed Systemd capability is
// present, and it never turns a connection failure into a fatal error: a
// missing capability skips calling connect at all, and a failed connect
// attempt (including one that times out per the caller-supplied, bounded
// ctx -- see systemdConnectTimeout) is logged as a warning and degrades
// exactly like an absent capability (nil is returned either way). This is
// what lets run() start on a host without systemd instead of aborting
// before it ever registers QueryCapabilities.
func connectSystemd(ctx context.Context, caps capability.Set, connect func(context.Context) (*dbus.Conn, error), logger *slog.Logger) *dbus.Conn {
	if !caps.Has(capability.Systemd) {
		return nil
	}
	client, err := connect(ctx)
	if err != nil {
		logger.Warn("systemd connection unavailable; systemd-backed managers disabled", "error", err)
		return nil
	}
	return client
}

// systemdManagers holds every daemon manager that depends on a live systemd
// D-Bus connection. buildSystemdManagers leaves every field nil when client
// is nil (systemd absent, or present but unreachable per connectSystemd),
// and registerStorageActions/registerBackups/registerServices/registerLogs
// each no-op on a nil manager -- so the daemon never fails to start over a
// missing systemd connection, only over a genuine flag misconfiguration
// (checked separately, before this function runs, via
// backups.ValidateConfiguration).
type systemdManagers struct {
	remoteManager   storage.RemoteManager
	backupManager   backups.Manager
	servicesManager services.Manager
	logsManager     logs.Manager
}

func buildSystemdManagers(client *dbus.Conn, storageManager storage.Manager, backupTimers []string, backupMaxAge time.Duration, servicesJournal services.JournalReader, logsJournal logs.JournalReader) (systemdManagers, error) {
	if client == nil {
		return systemdManagers{}, nil
	}
	backupManager, err := backups.NewSystemManager(client, backupTimers, backupMaxAge)
	if err != nil {
		return systemdManagers{}, err
	}
	servicesManager, err := services.NewSystemManager(client, servicesJournal)
	if err != nil {
		return systemdManagers{}, err
	}
	logsManager, err := logs.NewSystemManager(client, logsJournal)
	if err != nil {
		return systemdManagers{}, err
	}
	return systemdManagers{
		remoteManager:   storage.NewSystemRemoteManager(storageManager, storage.NewArtifactStore(), storageUnitController{client: client}),
		backupManager:   backupManager,
		servicesManager: servicesManager,
		logsManager:     logsManager,
	}, nil
}

type stringListFlag []string

func (values *stringListFlag) String() string { return fmt.Sprint([]string(*values)) }
func (values *stringListFlag) Set(value string) error {
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func (values *stringListFlag) addCommaSeparated(input string) {
	for _, value := range strings.Split(input, ",") {
		if value = strings.TrimSpace(value); value != "" {
			*values = append(*values, value)
		}
	}
}

// registerCapabilities registers QueryCapabilities unconditionally: capability
// discovery itself requires no capability, so unlike every registerX
// function below it is never guarded by caps.Has(...) -- it is what reports
// caps in the first place. It is non-admin (any authenticated identity may
// discover which capabilities the daemon advertises) and returns exactly
// the probed Set, whose MarshalJSON already produces the sorted,
// present-only {"capabilities": [...]} shape the query contract requires.
func registerCapabilities(queries *broker.QueryRegistry, caps capability.Set) error {
	return queries.Register(broker.QueryCapabilities, false, func(context.Context, auth.Identity, map[string]string) (any, error) {
		return caps, nil
	})
}

// registerBackups registers QueryBackupsState iff manager is non-nil and caps
// has Systemd. backups.Manager monitors systemd timers, so it requires
// Systemd uniformly -- there is no partial-capability case to guard
// per-call, unlike registerServices/registerLogs. manager is nil exactly
// when buildSystemdManagers skipped construction (systemd absent or
// unreachable), which per c7 already implies the Systemd capability was
// absent, so the manager check and the caps check agree in the real run()
// wiring; both are kept so a fake, non-nil manager passed directly in tests
// still respects the capability guard.
func registerBackups(queries *broker.QueryRegistry, manager backups.Manager, caps capability.Set) error {
	if manager == nil || !caps.Has(capability.Systemd) {
		return nil
	}
	return queries.Register(broker.QueryBackupsState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	})
}

func registerStorage(queries *broker.QueryRegistry, manager storage.Manager) error {
	return queries.Register(broker.QueryStorageState, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		if len(parameters) != 0 {
			return nil, fmt.Errorf("storage state query does not accept parameters")
		}
		return manager.State(ctx)
	})
}

const storageActionTimeout = 2 * time.Minute

// registerStorageActions registers the remote-mount lifecycle actions iff
// manager is non-nil and caps has Systemd. Every one of these actions
// generates and controls systemd units (mount/automount, or their
// enable/disable/start/stop lifecycle), so they require Systemd uniformly
// -- there is no partial-capability case to guard per-call. manager is nil
// exactly when buildSystemdManagers skipped constructing the remote-mount
// controller (systemd absent or unreachable), which per c7 already implies
// the Systemd capability was absent, so the manager check and the caps
// check agree in the real run() wiring; both are kept so a fake, non-nil
// manager passed directly in tests still respects the capability guard.
// QueryStorageState itself (registered separately via registerStorage
// against the plain, non-systemd storageManager) is unaffected -- storage
// inventory reads never depend on systemd, per docs/capabilities.md's
// documented exception.
func registerStorageActions(actions *broker.ActionRegistry, manager storage.RemoteManager, caps capability.Set) error {
	if manager == nil || !caps.Has(capability.Systemd) {
		return nil
	}
	for _, action := range []struct {
		id         string
		parameters []string
		request    func(map[string]string) (storage.CreateRequest, error)
	}{
		{id: broker.ActionStorageCreateNFS, parameters: []string{"host", "export", "target", "version", "read_only"}, request: func(parameters map[string]string) (storage.CreateRequest, error) {
			readOnly, err := storage.ParseReadOnly(parameters["read_only"])
			if err != nil {
				return storage.CreateRequest{}, errors.New("invalid remote mount parameter")
			}
			return storage.CreateRequest{ID: parameters["_id"], Protocol: "nfs", Host: parameters["host"], Export: parameters["export"], Target: parameters["target"], Version: parameters["version"], ReadOnly: readOnly}, nil
		}},
		{id: broker.ActionStorageCreateSMBGuest, parameters: []string{"server", "share", "target", "version", "read_only"}, request: smbCreateRequest(false, false)},
		{id: broker.ActionStorageCreateSMBCredentials, parameters: []string{"server", "share", "username", "password", "target", "version", "read_only"}, request: smbCreateRequest(true, false)},
		{id: broker.ActionStorageCreateSMBGuestOwned, parameters: []string{"server", "share", "target", "version", "read_only", "uid", "gid"}, request: smbCreateRequest(false, true)},
		{id: broker.ActionStorageCreateSMBCredentialsOwned, parameters: []string{"server", "share", "username", "password", "target", "version", "read_only", "uid", "gid"}, request: smbCreateRequest(true, true)},
	} {
		request := action.request
		if err := actions.RegisterDefinition(broker.ActionDefinition{
			ID: action.id, Admin: true, Parameters: action.parameters, Prepare: prepareStorageCreate,
			Resource: storageMountResource, LockResource: func(map[string]string) (string, error) { return "storage/mounts", nil },
			Handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
				request, err := request(parameters)
				if err != nil {
					return err
				}
				actionCtx, cancel := context.WithTimeout(ctx, storageActionTimeout)
				defer cancel()
				return manager.Create(actionCtx, request)
			},
		}); err != nil {
			return err
		}
	}
	for _, action := range []struct {
		confirmation bool
		id           string
		handler      func(context.Context, string) error
	}{
		{id: broker.ActionStorageMount, handler: manager.Mount},
		{id: broker.ActionStorageUnmount, confirmation: true, handler: manager.Unmount},
		{id: broker.ActionStorageDelete, confirmation: true, handler: manager.Delete},
	} {
		handler := action.handler
		if err := actions.RegisterDefinition(broker.ActionDefinition{
			ID: action.id, Admin: true, Parameters: []string{"id"}, ConfirmationRequired: action.confirmation,
			Resource: storageMountResource, LockResource: storageMountResource,
			Handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
				if storage.ValidateDefinitionID(parameters["id"]) != nil {
					return errors.New("invalid remote mount ID")
				}
				actionCtx, cancel := context.WithTimeout(ctx, storageActionTimeout)
				defer cancel()
				return handler(actionCtx, parameters["id"])
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func smbCreateRequest(credentials, owned bool) func(map[string]string) (storage.CreateRequest, error) {
	return func(parameters map[string]string) (storage.CreateRequest, error) {
		readOnly, err := storage.ParseReadOnly(parameters["read_only"])
		if err != nil {
			return storage.CreateRequest{}, errors.New("invalid remote mount parameter")
		}
		ownership := storage.SMBOwnership{}
		if owned {
			ownership, err = storage.ParseSMBOwnership(parameters["uid"], parameters["gid"])
			if err != nil || ownership == (storage.SMBOwnership{}) {
				return storage.CreateRequest{}, errors.New("invalid remote mount parameter")
			}
		}
		request := storage.CreateRequest{ID: parameters["_id"], Protocol: "smb", Server: parameters["server"], Share: parameters["share"], Target: parameters["target"], Version: parameters["version"], ReadOnly: readOnly, SMBOwnership: ownership}
		if credentials {
			request.Username, request.Password = parameters["username"], parameters["password"]
		}
		return request, nil
	}
}

func prepareStorageCreate(_ context.Context, _ auth.Identity, parameters map[string]string) (map[string]string, error) {
	id, err := storage.NewDefinitionID(rand.Reader)
	if err != nil {
		return nil, errors.New("allocate remote mount ID")
	}
	parameters["_id"] = id
	return parameters, nil
}

func storageMountResource(parameters map[string]string) (string, error) {
	id := parameters["_id"]
	if id == "" {
		id = parameters["id"]
	}
	if storage.ValidateDefinitionID(id) != nil {
		return "", errors.New("invalid remote mount ID")
	}
	return "storage/mount/" + id, nil
}

type storageUnitController struct{ client *dbus.Conn }

func (controller storageUnitController) DaemonReload(ctx context.Context) error {
	return controller.client.ReloadContext(ctx)
}

func (controller storageUnitController) Disable(ctx context.Context, unit string) error {
	_, err := controller.client.DisableUnitFilesContext(ctx, []string{unit}, false)
	return err
}

func (controller storageUnitController) Enable(ctx context.Context, unit string) error {
	_, _, err := controller.client.EnableUnitFilesContext(ctx, []string{unit}, false, false)
	return err
}

func (controller storageUnitController) Start(ctx context.Context, unit string) error {
	return waitForStorageUnitJob(ctx, unit, controller.client.StartUnitContext)
}

func (controller storageUnitController) Stop(ctx context.Context, unit string) error {
	return waitForStorageUnitJob(ctx, unit, controller.client.StopUnitContext)
}

// waitForStorageUnitJob waits for the queued systemd job to finish so lifecycle
// operations never remove units or targets while the job is still running.
func waitForStorageUnitJob(ctx context.Context, unit string, operation func(context.Context, string, string, chan<- string) (int, error)) error {
	results := make(chan string, 1)
	if _, err := operation(ctx, unit, "replace", results); err != nil {
		return err
	}
	select {
	case result := <-results:
		if result != "done" {
			return fmt.Errorf("systemd job for %s finished as %q", unit, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// registerLogs registers QueryLogs iff manager is non-nil and caps has both
// Systemd and Journald. logs.Manager.Logs() calls
// ListUnitsContext/ListUnitFilesContext on the systemd D-Bus client before
// filtering journal entries, so it genuinely requires both capabilities
// uniformly, not journald alone (docs/capabilities.md's exception #3).
// manager is nil exactly when construction skipped it -- systemd absent or
// unreachable per buildSystemdManagers, which per c7 already implies the
// Systemd capability was absent -- so the manager check and the caps check
// agree in the real run() wiring; both are kept so a fake, non-nil manager
// passed directly in tests still respects the capability guard.
func registerLogs(queries *broker.QueryRegistry, manager logs.Manager, caps capability.Set) error {
	if manager == nil || !caps.HasAll(capability.Systemd, capability.Journald) {
		return nil
	}
	return queries.Register(broker.QueryLogs, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		filters, err := logs.ParseBrokerFilters(parameters)
		if err != nil {
			return nil, err
		}
		return manager.Logs(ctx, filters)
	})
}

func registerFiles(queries *broker.QueryRegistry, streamQueries *broker.StreamQueryRegistry, streamActions *broker.StreamActionRegistry, manager files.Manager) error {
	if err := queries.Register(broker.QueryFilesList, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		request, err := files.ParseListParameters(parameters)
		if err != nil {
			return nil, filesPublicError(err)
		}
		state, err := manager.List(ctx, request)
		return state, filesPublicError(err)
	}); err != nil {
		return err
	}
	if err := streamQueries.Register(broker.StreamQueryDefinition{
		ID: broker.QueryFilesDownload, Admin: true, Parameters: []string{"root", "path"}, Limit: files.MaxTransferBytes,
		Handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string) (broker.StreamResult, error) {
			download, err := manager.Download(ctx, parameters["root"], parameters["path"])
			if err != nil {
				return broker.StreamResult{}, filesPublicError(err)
			}
			return broker.StreamResult{Body: download.Body, Filename: path.Base(download.Name), MediaType: "application/octet-stream", Size: download.Size}, nil
		},
	}); err != nil {
		return err
	}
	return streamActions.Register(broker.StreamActionDefinition{
		ID: broker.ActionFilesUpload, Admin: true, Parameters: []string{"root", "directory", "name"}, Limit: files.MaxTransferBytes, Timeout: 15 * time.Minute,
		Resource: func(parameters map[string]string) (string, error) {
			return filesResource(parameters)
		},
		LockResource: func(parameters map[string]string) (string, error) {
			return filesResource(parameters)
		},
		Handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string, body io.Reader) error {
			return filesPublicError(manager.Upload(ctx, parameters["root"], parameters["directory"], parameters["name"], body))
		},
	})
}

func filesResource(parameters map[string]string) (string, error) {
	destination := parameters["name"]
	if directory := parameters["directory"]; directory != "" {
		destination = directory + "/" + destination
	}
	if len(destination) > files.MaxPathBytes {
		return "", filesPublicError(files.ErrInvalid)
	}
	return "files/" + parameters["root"] + "/" + destination, nil
}

func filesPublicError(err error) error {
	if err == nil {
		return nil
	}
	for _, public := range []struct {
		err               error
		status            int
		message, category string
	}{
		{files.ErrInvalid, 400, "invalid files request", "invalid_request"},
		{files.ErrNotFound, 404, "files resource not found", "not_found"},
		{files.ErrReadOnly, 403, "files root is read-only", "read_only"},
		{files.ErrConflict, 409, "files conflict", "conflict"},
		{files.ErrTooLarge, 413, "file transfer is too large", "too_large"},
		{files.ErrUnavailable, 503, "files service unavailable", "unavailable"},
	} {
		if errors.Is(err, public.err) {
			return broker.NewPublicError(public.status, public.message, public.category, err)
		}
	}
	return broker.NewPublicError(503, "files service unavailable", "unavailable", err)
}

// maintenanceLockResource is the serialization key ActionMaintenanceReboot
// holds for the duration of a reboot attempt. It is deliberately distinct
// from the "sysext/global" key every sysext lifecycle action shares: reboot
// once reused that key, but the coupling was accidental rather than
// intentional, and the only thing the reboot action needs is that two
// concurrent reboots cannot overlap. Owning its own resource keeps that
// guarantee while letting an in-flight extension refresh/update and a reboot
// proceed independently.
const maintenanceLockResource = "maintenance/global"

// registerMaintenance registers QueryMaintenanceState and
// ActionMaintenanceReboot iff caps has Systemd -- both require Systemd
// uniformly, so the guard sits once at the top rather than per call.
// maintenance.NewSystemManager has no D-Bus dependency (it depends only on
// the sysext manager, job store, and command runner), so unlike
// backups/services/logs/storage there is no nil-manager backstop here: the
// manager is always non-nil regardless of systemd's presence, and this
// guard is the only thing withholding registration. Extension-derived
// fields (Updates, Feature/merged-derived reboot reasons) degrade inside
// SystemManager.State itself based on the probed updex/sysext capabilities
// passed into NewSystemManager, independent of this systemd guard.
func registerMaintenance(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager maintenance.Manager, caps capability.Set) error {
	if !caps.Has(capability.Systemd) {
		return nil
	}
	if err := queries.Register(broker.QueryMaintenanceState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	}); err != nil {
		return err
	}
	return actions.RegisterDefinition(broker.ActionDefinition{
		ID: broker.ActionMaintenanceReboot, Admin: true, ConfirmationRequired: true, NonBlocking: true,
		Resource:     func(map[string]string) (string, error) { return "maintenance/reboot", nil },
		LockResource: func(map[string]string) (string, error) { return maintenanceLockResource, nil },
		Handler:      func(ctx context.Context, _ auth.Identity, _ map[string]string) error { return manager.Reboot(ctx) },
	})
}

// registerHostImage registers QueryHostImageStatus iff the host advertises at
// least one host-image source -- caps.HasAny(capability.Bootc,
// capability.RPMOStree). The guard is deliberately an any-of, not an all-of:
// either source alone yields a usable (if partial) report, and the handler
// itself only ever runs the sources whose capability was probed present.
//
// It is also deliberately independent of registerMaintenance's Systemd guard.
// Reboot posture and the reboot action need systemd; reading which image the
// host booted does not, so a bootc host without systemd still gets this query
// while QueryMaintenanceState and ActionMaintenanceReboot stay withheld.
//
// The query is read-only in the strongest sense available here: it is served
// by a HostImageSource, an interface with no mutating method, and no
// bootc/rpm-ostree action exists in broker's ID vocabulary to pair it with.
// Like QueryMaintenanceState it is available to any authenticated identity
// (admin is not required), since it reports facts about the running image
// rather than privileged content.
func registerHostImage(queries *broker.QueryRegistry, manager maintenance.HostImageSource, caps capability.Set) error {
	if !caps.HasAny(capability.Bootc, capability.RPMOStree) {
		return nil
	}
	return queries.Register(broker.QueryHostImageStatus, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.Status(ctx)
	})
}

func registerJobs(queries *broker.QueryRegistry, store *jobs.Store) error {
	return queries.Register(broker.QueryJobs, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		for name := range parameters {
			if name != "action" && name != "status" && name != "limit" {
				return nil, fmt.Errorf("unsupported job filter %q", name)
			}
		}
		limit := 50
		if value := parameters["limit"]; value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				return nil, fmt.Errorf("job limit must be between 1 and 100")
			}
			limit = parsed
		}
		return store.List(ctx, jobs.Filter{Action: parameters["action"], Status: parameters["status"], Limit: limit})
	})
}

func registerActivity(queries *broker.QueryRegistry, store *audit.Store) error {
	return queries.Register(broker.QueryActivity, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		for name := range parameters {
			if name != "action" && name != "outcome" && name != "limit" {
				return nil, fmt.Errorf("unsupported activity filter %q", name)
			}
		}
		limit := 50
		if value := parameters["limit"]; value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				return nil, fmt.Errorf("activity limit must be between 1 and 100")
			}
			limit = parsed
		}
		return store.List(ctx, audit.Filter{Action: parameters["action"], Outcome: parameters["outcome"], Limit: limit})
	})
}

func listenUnix(path, groupName string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale broker socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect broker socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on broker socket: %w", err)
	}
	group, err := user.LookupGroup(groupName)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("resolve broker socket group: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("parse broker socket group: %w", err)
	}
	if err := os.Chown(path, os.Geteuid(), gid); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("set broker socket owner: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("set broker socket mode: %w", err)
	}
	return listener, nil
}

type actionRegistration struct {
	confirmation bool
	global       bool
	handler      func(context.Context, string) error
	id           string
	resource     string
}

func registerNamedActions(registry *broker.ActionRegistry, parameter string, registrations []actionRegistration) error {
	for _, registration := range registrations {
		handler := registration.handler
		prefix := registration.resource
		global := registration.global
		var lockResource func(map[string]string) (string, error)
		if global {
			lockResource = func(map[string]string) (string, error) { return "sysext/global", nil }
		}
		if err := registry.RegisterDefinition(broker.ActionDefinition{
			ID: registration.id, Admin: true, Parameters: []string{parameter}, ConfirmationRequired: registration.confirmation,
			Resource:     func(parameters map[string]string) (string, error) { return prefix + "/" + parameters[parameter], nil },
			LockResource: lockResource,
			Handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
				return handler(ctx, parameters[parameter])
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

// registerSysextActions registers the four sysext lifecycle actions guarded
// per-action against caps, per docs/capabilities.md: unlike every other
// registerX function in this file (which has a uniform per-call
// requirement), sysext's four actions split across three distinct
// requirements, so each is guarded individually rather than gating the whole
// function behind one check. ActionSysextDisable and ActionSysextEnable
// share the same "updex AND sysext" requirement and are registered together
// via registerNamedActions, so that pair is guarded as a single group.
// ActionSysextRefresh needs only sysext (it merges/refreshes installed
// systemd-sysext images) and ActionSysextUpdate needs only updex (it invokes
// updex directly); those two already live in a separate local loop, so the
// per-entry required capability is checked there without touching the
// shared registerNamedActions/registerProjectActions helpers used by every
// other module.
func registerSysextActions(registry *broker.ActionRegistry, manager sysext.Manager, caps capability.Set) error {
	if caps.HasAll(capability.Updex, capability.Sysext) {
		if err := registerNamedActions(registry, "name", []actionRegistration{
			{id: broker.ActionSysextDisable, resource: "sysext/feature", global: true, confirmation: true, handler: func(ctx context.Context, name string) error {
				return manager.Disable(ctx, name)
			}},
			{id: broker.ActionSysextEnable, resource: "sysext/feature", global: true, handler: func(ctx context.Context, name string) error {
				return manager.Enable(ctx, name)
			}},
		}); err != nil {
			return err
		}
	}
	for _, action := range []struct {
		background   bool
		confirmation bool
		handler      broker.ActionHandler
		id           string
		reboot       bool
		required     capability.ID
	}{
		{id: broker.ActionSysextRefresh, background: true, confirmation: true, required: capability.Sysext, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Refresh(ctx)
		}},
		{id: broker.ActionSysextUpdate, background: true, confirmation: true, reboot: true, required: capability.Updex, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Update(ctx)
		}},
	} {
		if !caps.Has(action.required) {
			continue
		}
		if err := registry.RegisterDefinition(broker.ActionDefinition{ID: action.id, Admin: true, Background: action.background, ConfirmationRequired: action.confirmation, RebootRequired: action.reboot, Timeout: 20 * time.Minute, Resource: func(map[string]string) (string, error) { return "sysext/global", nil }, Handler: action.handler}); err != nil {
			return err
		}
	}
	return nil
}

// registerServices registers Services queries/actions guarded by the
// capabilities their manager call paths actually depend on, per
// docs/capabilities.md: QueryServicesState and every services lifecycle
// action require only Systemd, while QueryServicesJournal additionally
// requires Journald because resolveUnit() needs the systemd client to
// validate/resolve the unit before journal entries are read (exception #2).
// Each group is guarded individually rather than the whole function behind
// one check, so a host with systemd but no journald still gets full service
// management with only the journal query withheld. manager is nil exactly
// when construction skipped it -- systemd absent or unreachable per
// buildSystemdManagers, which per c7 already implies Systemd was absent --
// so the manager check and the caps check agree in the real run() wiring;
// both are kept so a fake, non-nil manager passed directly in tests still
// respects the capability guard.
func registerServices(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager services.Manager, caps capability.Set) error {
	if manager != nil && caps.Has(capability.Systemd) {
		if err := queries.Register(broker.QueryServicesState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
			return manager.State(ctx)
		}); err != nil {
			return err
		}
		if err := registerNamedActions(actions, "unit", []actionRegistration{
			{id: broker.ActionServicesDisable, resource: "services/unit", confirmation: true, handler: manager.Disable},
			{id: broker.ActionServicesEnable, resource: "services/unit", handler: manager.Enable},
			{id: broker.ActionServicesResetFailed, resource: "services/unit", handler: manager.ResetFailed},
			{id: broker.ActionServicesRestart, resource: "services/unit", handler: manager.Restart},
			{id: broker.ActionServicesStart, resource: "services/unit", handler: manager.Start},
			{id: broker.ActionServicesStop, resource: "services/unit", confirmation: true, handler: manager.Stop},
		}); err != nil {
			return err
		}
	}
	if manager != nil && caps.HasAll(capability.Systemd, capability.Journald) {
		if err := queries.Register(broker.QueryServicesJournal, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
			return manager.Journal(ctx, parameters["unit"])
		}); err != nil {
			return err
		}
	}
	return nil
}

// registerPodman registers every Podman query and action iff the podman
// capability was probed present in caps; otherwise it registers nothing and
// returns nil. This lets a host with an unreachable or absent Podman
// socket start the daemon normally, with the Podman surface simply absent
// from every registry (and from QueryCapabilities) rather than aborting
// startup.
func registerPodman(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager podman.Manager, caps capability.Set) error {
	if !caps.Has(capability.Podman) {
		return nil
	}
	if err := queries.Register(broker.QueryPodmanLogs, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		return manager.Logs(ctx, parameters["id"])
	}); err != nil {
		return err
	}
	if err := queries.Register(broker.QueryPodmanState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	}); err != nil {
		return err
	}
	return registerNamedActions(actions, "id", []actionRegistration{
		{id: broker.ActionPodmanRemove, resource: "podman/container", confirmation: true, handler: manager.Remove},
		{id: broker.ActionPodmanRemoveImage, resource: "podman/image", confirmation: true, handler: manager.RemoveImage},
		{id: broker.ActionPodmanRestart, resource: "podman/container", handler: manager.Restart},
		{id: broker.ActionPodmanStart, resource: "podman/container", handler: manager.Start},
		{id: broker.ActionPodmanStop, resource: "podman/container", confirmation: true, handler: manager.Stop},
	})
}

// registerDocker registers every Docker query and action iff the docker
// capability was probed present in caps; otherwise it registers nothing and
// returns nil. See registerPodman for the rationale.
func registerDocker(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager docker.Manager, caps capability.Set) error {
	if !caps.Has(capability.Docker) {
		return nil
	}
	if err := queries.Register(broker.QueryDockerLogs, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		return manager.Logs(ctx, parameters["id"])
	}); err != nil {
		return err
	}
	if err := queries.Register(broker.QueryDockerState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	}); err != nil {
		return err
	}
	return registerNamedActions(actions, "id", []actionRegistration{
		{id: broker.ActionDockerRemove, resource: "docker/container", confirmation: true, handler: manager.Remove},
		{id: broker.ActionDockerRemoveImage, resource: "docker/image", confirmation: true, handler: manager.RemoveImage},
		{id: broker.ActionDockerRestart, resource: "docker/container", handler: manager.Restart},
		{id: broker.ActionDockerStart, resource: "docker/container", handler: manager.Start},
		{id: broker.ActionDockerStop, resource: "docker/container", confirmation: true, handler: manager.Stop},
	})
}

// registerIncus registers every Incus query and action iff the incus
// capability was probed present in caps; otherwise it registers nothing and
// returns nil. See registerPodman for the rationale.
func registerIncus(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager incus.Manager, caps capability.Set) error {
	if !caps.Has(capability.Incus) {
		return nil
	}
	if err := queries.Register(broker.QueryIncusState, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		return manager.State(ctx, parameters["project"])
	}); err != nil {
		return err
	}
	return registerProjectActions(actions, []projectActionRegistration{
		{id: broker.ActionIncusRemove, resource: "incus/instance", confirmation: true, handler: manager.Remove, parameter: "name"},
		{id: broker.ActionIncusRemoveImage, resource: "incus/image", confirmation: true, handler: manager.RemoveImage, parameter: "fingerprint"},
		{id: broker.ActionIncusRestart, resource: "incus/instance", handler: manager.Restart, parameter: "name"},
		{id: broker.ActionIncusStart, resource: "incus/instance", handler: manager.Start, parameter: "name"},
		{id: broker.ActionIncusStop, resource: "incus/instance", confirmation: true, handler: manager.Stop, parameter: "name"},
	})
}

type projectActionRegistration struct {
	confirmation bool
	id           string
	handler      func(context.Context, string, string) error
	parameter    string
	resource     string
}

func registerProjectActions(registry *broker.ActionRegistry, registrations []projectActionRegistration) error {
	for _, registration := range registrations {
		handler := registration.handler
		parameter := registration.parameter
		prefix := registration.resource
		if err := registry.RegisterDefinition(broker.ActionDefinition{
			ID: registration.id, Admin: true, Parameters: []string{"project", parameter}, ConfirmationRequired: registration.confirmation,
			Resource: func(parameters map[string]string) (string, error) {
				return prefix + "/" + parameters["project"] + "/" + parameters[parameter], nil
			},
			Handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
				return handler(ctx, parameters["project"], parameters[parameter])
			},
		}); err != nil {
			return err
		}
	}
	return nil
}
