package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/modules/activity"
	"github.com/frostyard/pilothouse/internal/modules/attention"
	"github.com/frostyard/pilothouse/internal/modules/docker"
	"github.com/frostyard/pilothouse/internal/modules/incus"
	"github.com/frostyard/pilothouse/internal/modules/podman"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	systemmodule "github.com/frostyard/pilothouse/internal/modules/system"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/frostyard/pilothouse/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:8888", "HTTP listen address")
	brokerSocket := flag.String("broker-socket", "/run/pilothouse/broker.sock", "privileged broker Unix socket")
	definitionsRoot := flag.String("definitions-root", "/usr/lib", "directory containing sysupdate definition directories")
	var allowedOrigins stringListFlag
	flag.Var(&allowedOrigins, "allowed-origin", "trusted public HTTP(S) origin when behind a reverse proxy; repeatable")
	secureCookie := flag.Bool("secure-cookie", false, "require HTTPS when sending the session cookie")
	updex := flag.String("updex", "updex", "path to the updex executable")
	flag.Parse()
	allowedOrigins.addCommaSeparated(os.Getenv("PILOTHOUSE_ALLOWED_ORIGINS"))

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	system := systemmodule.New(systemmodule.NewLinuxCollector("/"))
	serviceModule := services.New()
	registry, err := platform.NewRegistry(
		attention.New(system, serviceModule),
		activity.New(),
		system,
		sysext.New(sysext.NewSystemManager(sysext.ExecRunner{}, *definitionsRoot, *updex)),
		podman.New(),
		docker.New(),
		incus.New(),
		serviceModule,
	)
	if err != nil {
		return fmt.Errorf("register modules: %w", err)
	}
	handler, err := web.NewServer(registry, broker.NewClient(*brokerSocket), logger, *secureCookie, allowedOrigins...)
	if err != nil {
		return fmt.Errorf("create web server: %w", err)
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler.Handler(),
		IdleTimeout:       60 * time.Second,
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

	logger.Info("pilothouse listening", "address", server.Addr, "modules", len(registry.Modules()))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return fmt.Sprint([]string(*values))
}

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("origin cannot be empty")
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
