package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/auth/pam"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/modules/docker"
	"github.com/frostyard/pilothouse/internal/modules/incus"
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
	definitionsRoot := flag.String("definitions-root", "/usr/lib", "directory containing sysupdate definition directories")
	loginGroup := flag.String("login-group", "", "optional system group allowed to log in")
	pamService := flag.String("pam-service", "pilothouse", "PAM service name")
	podmanSocket := flag.String("podman-socket", "/run/podman/podman.sock", "Podman API Unix socket path")
	socket := flag.String("socket", "/run/pilothouse/broker.sock", "Unix socket path")
	socketGroup := flag.String("socket-group", "pilothouse", "group allowed to connect to the broker")
	updex := flag.String("updex", "updex", "path to the updex executable")
	flag.Parse()
	if os.Geteuid() != 0 {
		return fmt.Errorf("pilothoused must run as root")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	actions := broker.NewActionRegistry()
	queries := broker.NewQueryRegistry()
	servicesManager, err := services.NewSystemManager(servicejournal.New())
	if err != nil {
		return err
	}
	if err := registerServices(actions, queries, servicesManager); err != nil {
		return err
	}
	if err := registerSysextActions(actions, sysext.NewSystemManager(sysext.ExecRunner{}, *definitionsRoot, *updex)); err != nil {
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
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve broker: %w", err)
	}
	return nil
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
	id      string
	handler func(context.Context, string) error
}

func registerNamedActions(registry *broker.ActionRegistry, parameter string, registrations []actionRegistration) error {
	for _, registration := range registrations {
		handler := registration.handler
		if err := registry.Register(registration.id, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
			return handler(ctx, parameters[parameter])
		}); err != nil {
			return err
		}
	}
	return nil
}

func registerSysextActions(registry *broker.ActionRegistry, manager sysext.Manager) error {
	if err := registerNamedActions(registry, "name", []actionRegistration{
		{id: broker.ActionSysextDisable, handler: func(ctx context.Context, name string) error {
			return manager.Disable(ctx, name)
		}},
		{id: broker.ActionSysextEnable, handler: func(ctx context.Context, name string) error {
			return manager.Enable(ctx, name)
		}},
	}); err != nil {
		return err
	}
	for _, action := range []struct {
		handler broker.ActionHandler
		id      string
	}{
		{id: broker.ActionSysextRefresh, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Refresh(ctx)
		}},
		{id: broker.ActionSysextUpdate, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Update(ctx)
		}},
	} {
		if err := registry.Register(action.id, true, action.handler); err != nil {
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
		{id: broker.ActionServicesDisable, handler: manager.Disable},
		{id: broker.ActionServicesEnable, handler: manager.Enable},
		{id: broker.ActionServicesResetFailed, handler: manager.ResetFailed},
		{id: broker.ActionServicesRestart, handler: manager.Restart},
		{id: broker.ActionServicesStart, handler: manager.Start},
		{id: broker.ActionServicesStop, handler: manager.Stop},
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
		{id: broker.ActionPodmanRemove, handler: manager.Remove},
		{id: broker.ActionPodmanRemoveImage, handler: manager.RemoveImage},
		{id: broker.ActionPodmanRestart, handler: manager.Restart},
		{id: broker.ActionPodmanStart, handler: manager.Start},
		{id: broker.ActionPodmanStop, handler: manager.Stop},
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
		{id: broker.ActionDockerRemove, handler: manager.Remove},
		{id: broker.ActionDockerRemoveImage, handler: manager.RemoveImage},
		{id: broker.ActionDockerRestart, handler: manager.Restart},
		{id: broker.ActionDockerStart, handler: manager.Start},
		{id: broker.ActionDockerStop, handler: manager.Stop},
	})
}

func registerIncus(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager incus.Manager) error {
	if err := queries.Register(broker.QueryIncusState, false, func(ctx context.Context, _ auth.Identity, parameters map[string]string) (any, error) {
		return manager.State(ctx, parameters["project"])
	}); err != nil {
		return err
	}
	return registerProjectActions(actions, []projectActionRegistration{
		{id: broker.ActionIncusRemove, handler: manager.Remove, parameter: "name"},
		{id: broker.ActionIncusRemoveImage, handler: manager.RemoveImage, parameter: "fingerprint"},
		{id: broker.ActionIncusRestart, handler: manager.Restart, parameter: "name"},
		{id: broker.ActionIncusStart, handler: manager.Start, parameter: "name"},
		{id: broker.ActionIncusStop, handler: manager.Stop, parameter: "name"},
	})
}

type projectActionRegistration struct {
	id        string
	handler   func(context.Context, string, string) error
	parameter string
}

func registerProjectActions(registry *broker.ActionRegistry, registrations []projectActionRegistration) error {
	for _, registration := range registrations {
		handler := registration.handler
		parameter := registration.parameter
		if err := registry.Register(registration.id, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
			return handler(ctx, parameters["project"], parameters[parameter])
		}); err != nil {
			return err
		}
	}
	return nil
}
