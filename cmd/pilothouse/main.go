package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/modules/activity"
	"github.com/frostyard/pilothouse/internal/modules/attention"
	"github.com/frostyard/pilothouse/internal/modules/backups"
	"github.com/frostyard/pilothouse/internal/modules/docker"
	"github.com/frostyard/pilothouse/internal/modules/files"
	"github.com/frostyard/pilothouse/internal/modules/fleet"
	"github.com/frostyard/pilothouse/internal/modules/incus"
	"github.com/frostyard/pilothouse/internal/modules/k3s"
	"github.com/frostyard/pilothouse/internal/modules/logs"
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/podman"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/frostyard/pilothouse/internal/modules/storage"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
	systemmodule "github.com/frostyard/pilothouse/internal/modules/system"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/frostyard/pilothouse/internal/tlscert"
	"github.com/frostyard/pilothouse/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:8888", "HTTP listen address (env PILOTHOUSE_LISTEN)")
	brokerSocket := flag.String("broker-socket", "/run/pilothouse/broker.sock", "privileged broker Unix socket")
	var allowedOrigins stringListFlag
	flag.Var(&allowedOrigins, "allowed-origin", "trusted public HTTP(S) origin when behind a reverse proxy; repeatable")
	secureCookie := flag.Bool("secure-cookie", false, "require HTTPS when sending the session cookie")
	tlsCert := flag.String("tls-cert", "", "PEM certificate (chain) enabling HTTPS; requires --tls-key (env PILOTHOUSE_TLS_CERT)")
	tlsKey := flag.String("tls-key", "", "PEM private key for --tls-cert (env PILOTHOUSE_TLS_KEY)")
	allowInsecureHTTP := flag.Bool("allow-insecure-http", false, "serve plaintext HTTP on a non-loopback address (env PILOTHOUSE_ALLOW_INSECURE_HTTP)")
	dev := flag.Bool("dev", false, "register in-development preview modules that are not backed by real functionality")
	flag.Parse()
	allowedOrigins.addCommaSeparated(os.Getenv("PILOTHOUSE_ALLOWED_ORIGINS"))

	// Flag-beats-environment precedence needs to know which flags were
	// actually passed: an explicit --listen 127.0.0.1:8888 must beat
	// PILOTHOUSE_LISTEN even though it equals the default.
	passed := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { passed[f.Name] = true })
	addr := resolveString(passed["listen"], *listen, os.Getenv("PILOTHOUSE_LISTEN"))
	certPath := resolveString(passed["tls-cert"], *tlsCert, os.Getenv("PILOTHOUSE_TLS_CERT"))
	keyPath := resolveString(passed["tls-key"], *tlsKey, os.Getenv("PILOTHOUSE_TLS_KEY"))
	allowInsecure, err := resolveBool(passed["allow-insecure-http"], *allowInsecureHTTP, "PILOTHOUSE_ALLOW_INSECURE_HTTP", os.Getenv("PILOTHOUSE_ALLOW_INSECURE_HTTP"))
	if err != nil {
		return err
	}
	if (certPath == "") != (keyPath == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be set together (got cert=%q key=%q; env: PILOTHOUSE_TLS_CERT/PILOTHOUSE_TLS_KEY)", certPath, keyPath)
	}
	loopback, err := isLoopbackListen(addr)
	if err != nil {
		return err
	}
	mode := decideServeMode(loopback, certPath != "", allowInsecure)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if mode == serveTLSSelfSigned {
		host, _, _ := net.SplitHostPort(addr)
		dir, dirErr := certStateDir()
		if dirErr != nil {
			return selfSignedUnavailable(addr, dirErr)
		}
		certPath, keyPath, err = tlscert.LoadOrCreate(dir, host, time.Now, logger)
		if err != nil {
			return selfSignedUnavailable(addr, err)
		}
	}
	tlsActive := mode != serveHTTP
	if !loopback && !tlsActive {
		logger.Warn("serving plaintext HTTP on a non-loopback address by explicit override; credentials and session cookies are not encrypted in transit", "address", addr)
	}

	registry, err := newRegistry(*dev)
	if err != nil {
		return fmt.Errorf("register modules: %w", err)
	}
	handler, err := web.NewServer(registry, broker.NewClient(*brokerSocket), logger, *secureCookie || tlsActive, allowedOrigins...)
	if err != nil {
		return fmt.Errorf("create web server: %w", err)
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           handler.Handler(),
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Minute,
	}
	if tlsActive {
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("pilothouse listening", "address", server.Addr, "modules", len(registry.Modules()), "tls", tlsActive, "cert", certPath)
	if tlsActive {
		err = server.ListenAndServeTLS(certPath, keyPath)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// newRegistry builds the production module registry: every management
// module instantiated and registered in the same order and configuration
// run() has always used. It is extracted from run() as a pure refactor (no
// behavior change) so cmd/pilothouse/capability_contract_test.go can build
// the real, production-wired registry directly instead of maintaining a
// second, hand-duplicated module list.
//
// Every module here is either self-contained or broker-backed. sysext was
// the last holdout — it used to be handed an exec-backed extension manager
// built from the web process's own --definitions-root/--updex flags, and now
// reads through broker.QueryExtensionsState like every other read-only
// module, so those two flags no longer exist on the web binary. The
// exec-backed manager itself moved to internal/modules/sysext/extctl, which
// this binary does not import at all. (cmd/pilothoused keeps its own
// definitions-root/updex flags; it is the process that actually runs the
// tools.)
//
// Its one argument, dev, mirrors run()'s --dev flag (default false, so
// production — including packaging/pilothouse.service's ExecStart, which
// does not pass it — gets dev=false). It gates the preview-only modules that
// are not backed by real functionality; today that is exactly fleet, a
// static mock with no multi-system transport or enrollment behind it. When
// dev is false, fleet.New() is never constructed and never reaches
// platform.NewRegistry, so Mount() is never called for it and all three of
// its routes (/fleet, /fleet/enroll, /fleet/systems/{id}) are genuinely
// unregistered — a real 404 from the mux, not a capability gate. Because
// internal/web derives the nav entry and the sidebar system-picker link from
// the registry's manifests, dropping the registration is the single switch
// that removes all three of Fleet's surfaces at once.
func newRegistry(dev bool) (*platform.Registry, error) {
	system := systemmodule.New(systemmodule.NewLinuxCollector("/"))
	serviceModule := services.New()
	backupModule := backups.New()
	maintenanceModule := maintenance.New()
	storageModule := storage.New()
	modules := []platform.Module{}
	if dev {
		modules = append(modules, fleet.New())
	}
	modules = append(modules,
		attention.New(system, serviceModule, maintenanceModule, backupModule, storageModule),
		activity.New(),
		system,
		storageModule,
		sysext.New(),
		podman.New(),
		docker.New(),
		incus.New(),
		k3s.New(),
		logs.New(),
		files.New(),
		serviceModule,
		maintenanceModule,
		backupModule,
	)
	return platform.NewRegistry(modules...)
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
