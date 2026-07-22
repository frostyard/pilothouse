package main

import (
	"context"
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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/auth/pam"
	"github.com/frostyard/pilothouse/internal/broker"
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
	backupManager, err := backups.NewSystemManager(backupTimers, *backupMaxAge)
	if err != nil {
		return err
	}
	defer backupManager.Close()
	if err := registerBackups(queries, backupManager); err != nil {
		return err
	}
	servicesManager, err := services.NewSystemManager(servicejournal.New())
	if err != nil {
		return err
	}
	if err := registerServices(actions, queries, servicesManager); err != nil {
		return err
	}
	logsManager, err := logs.NewSystemManager(logjournal.New())
	if err != nil {
		return err
	}
	if err := registerLogs(queries, logsManager); err != nil {
		return err
	}
	sysextManager := sysext.NewSystemManager(sysext.ExecRunner{}, *definitionsRoot, *updex)
	if err := registerSysextActions(actions, sysextManager); err != nil {
		return err
	}
	maintenanceManager := maintenance.NewSystemManager(sysextManager, jobStore, sysext.ExecRunner{}, "/")
	if err := registerMaintenance(actions, queries, maintenanceManager); err != nil {
		return err
	}
	podmanClient := podman.NewAPIClient(*podmanSocket)
	defer podmanClient.Close()
	if err := registerPodman(actions, queries, podman.NewSystemManager(podmanClient)); err != nil {
		return err
	}
	dockerClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return fmt.Errorf("create Docker client: %w", err)
	}
	defer func() { _ = dockerClient.Close() }()
	if err := registerDocker(actions, queries, docker.NewSystemManager(dockerClient)); err != nil {
		return err
	}
	incusClient := incus.NewLocalClient()
	if err := registerIncus(actions, queries, incus.NewSystemManager(incusClient)); err != nil {
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

func registerBackups(queries *broker.QueryRegistry, manager backups.Manager) error {
	return queries.Register(broker.QueryBackupsState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	})
}

func registerLogs(queries *broker.QueryRegistry, manager logs.Manager) error {
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
			return broker.StreamResult{Body: download.Body, Filename: download.Name, MediaType: "application/octet-stream", Size: download.Size}, nil
		},
	}); err != nil {
		return err
	}
	return streamActions.Register(broker.StreamActionDefinition{
		ID: broker.ActionFilesUpload, Admin: true, Parameters: []string{"root", "directory", "name"}, Limit: files.MaxTransferBytes, Timeout: 15 * time.Minute,
		Resource: func(parameters map[string]string) (string, error) {
			return filesResource(parameters), nil
		},
		LockResource: func(parameters map[string]string) (string, error) {
			return filesResource(parameters), nil
		},
		Handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string, body io.Reader) error {
			return filesPublicError(manager.Upload(ctx, parameters["root"], parameters["directory"], parameters["name"], body))
		},
	})
}

func filesResource(parameters map[string]string) string {
	return "files/" + parameters["root"] + "/" + parameters["directory"] + "/" + parameters["name"]
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

func registerMaintenance(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager maintenance.Manager) error {
	if err := queries.Register(broker.QueryMaintenanceState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	}); err != nil {
		return err
	}
	return actions.RegisterDefinition(broker.ActionDefinition{
		ID: broker.ActionMaintenanceReboot, Admin: true, ConfirmationRequired: true, NonBlocking: true,
		Resource:     func(map[string]string) (string, error) { return "maintenance/reboot", nil },
		LockResource: func(map[string]string) (string, error) { return "sysext/global", nil },
		Handler:      func(ctx context.Context, _ auth.Identity, _ map[string]string) error { return manager.Reboot(ctx) },
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

func registerSysextActions(registry *broker.ActionRegistry, manager sysext.Manager) error {
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
	for _, action := range []struct {
		background   bool
		confirmation bool
		handler      broker.ActionHandler
		id           string
		reboot       bool
	}{
		{id: broker.ActionSysextRefresh, background: true, confirmation: true, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Refresh(ctx)
		}},
		{id: broker.ActionSysextUpdate, background: true, confirmation: true, reboot: true, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Update(ctx)
		}},
	} {
		if err := registry.RegisterDefinition(broker.ActionDefinition{ID: action.id, Admin: true, Background: action.background, ConfirmationRequired: action.confirmation, RebootRequired: action.reboot, Timeout: 20 * time.Minute, Resource: func(map[string]string) (string, error) { return "sysext/global", nil }, Handler: action.handler}); err != nil {
			return err
		}
	}
	return nil
}

func registerServices(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager services.Manager) error {
	if err := queries.Register(broker.QueryServicesJournal, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		return manager.Journal(ctx, parameters["unit"])
	}); err != nil {
		return err
	}
	if err := queries.Register(broker.QueryServicesState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	}); err != nil {
		return err
	}
	return registerNamedActions(actions, "unit", []actionRegistration{
		{id: broker.ActionServicesDisable, resource: "services/unit", confirmation: true, handler: manager.Disable},
		{id: broker.ActionServicesEnable, resource: "services/unit", handler: manager.Enable},
		{id: broker.ActionServicesResetFailed, resource: "services/unit", handler: manager.ResetFailed},
		{id: broker.ActionServicesRestart, resource: "services/unit", handler: manager.Restart},
		{id: broker.ActionServicesStart, resource: "services/unit", handler: manager.Start},
		{id: broker.ActionServicesStop, resource: "services/unit", confirmation: true, handler: manager.Stop},
	})
}

func registerPodman(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager podman.Manager) error {
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

func registerDocker(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager docker.Manager) error {
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

func registerIncus(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager incus.Manager) error {
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
