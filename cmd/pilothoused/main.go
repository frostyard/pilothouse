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

	dockerclient "github.com/docker/docker/client"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/auth/pam"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/modules/docker"
	"github.com/frostyard/pilothouse/internal/modules/podman"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
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
	podmanBinary := flag.String("podman", "podman", "path to the Podman executable")
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
	if err := registerSysextActions(actions, sysext.NewSystemManager(sysext.ExecRunner{}, *definitionsRoot, *updex)); err != nil {
		return err
	}
	if err := registerPodman(actions, queries, podman.NewSystemManager(podman.ExecRunner{}, *podmanBinary)); err != nil {
		return err
	}
	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create Docker client: %w", err)
	}
	defer func() { _ = dockerClient.Close() }()
	if err := registerDocker(actions, queries, docker.NewSystemManager(dockerClient)); err != nil {
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

func registerSysextActions(registry *broker.ActionRegistry, manager sysext.Manager) error {
	actions := []struct {
		handler broker.ActionHandler
		id      string
	}{
		{id: broker.ActionSysextDisable, handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
			return manager.Disable(ctx, parameters["name"])
		}},
		{id: broker.ActionSysextEnable, handler: func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
			return manager.Enable(ctx, parameters["name"])
		}},
		{id: broker.ActionSysextRefresh, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Refresh(ctx)
		}},
		{id: broker.ActionSysextUpdate, handler: func(ctx context.Context, _ auth.Identity, _ map[string]string) error {
			return manager.Update(ctx)
		}},
	}
	for _, action := range actions {
		if err := registry.Register(action.id, true, action.handler); err != nil {
			return err
		}
	}
	return nil
}

func registerPodman(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager podman.Manager) error {
	if err := queries.Register(broker.QueryPodmanState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	}); err != nil {
		return err
	}
	registrations := []struct {
		handler func(context.Context, string) error
		id      string
	}{
		{id: broker.ActionPodmanRemove, handler: manager.Remove},
		{id: broker.ActionPodmanRestart, handler: manager.Restart},
		{id: broker.ActionPodmanStart, handler: manager.Start},
		{id: broker.ActionPodmanStop, handler: manager.Stop},
	}
	for _, registration := range registrations {
		handler := registration.handler
		if err := actions.Register(registration.id, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
			return handler(ctx, parameters["id"])
		}); err != nil {
			return err
		}
	}
	return nil
}

func registerDocker(actions *broker.ActionRegistry, queries *broker.QueryRegistry, manager docker.Manager) error {
	if err := queries.Register(broker.QueryDockerState, false, func(ctx context.Context, _ auth.Identity, _ map[string]string) (any, error) {
		return manager.State(ctx)
	}); err != nil {
		return err
	}
	registrations := []struct {
		handler func(context.Context, string) error
		id      string
	}{
		{id: broker.ActionDockerRemove, handler: manager.Remove},
		{id: broker.ActionDockerRestart, handler: manager.Restart},
		{id: broker.ActionDockerStart, handler: manager.Start},
		{id: broker.ActionDockerStop, handler: manager.Stop},
	}
	for _, registration := range registrations {
		handler := registration.handler
		if err := actions.Register(registration.id, true, func(ctx context.Context, _ auth.Identity, parameters map[string]string) error {
			return handler(ctx, parameters["id"])
		}); err != nil {
			return err
		}
	}
	return nil
}
