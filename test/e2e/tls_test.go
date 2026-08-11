package e2e_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/tlscert"
)

// buildPilothouse compiles the real binary into a per-test directory. The
// Go build cache makes repeat builds across the package cheap.
func buildPilothouse(t *testing.T) string {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "pilothouse")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/pilothouse")
	build.Dir = repositoryRoot
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build pilothouse: %v\n%s", buildErr, output)
	}
	return binary
}

// freePort reserves a TCP port on host and returns it, closed and ready
// for the child process to bind.
func freePort(t *testing.T, host string) string {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// startPilothouse launches the binary with args and extraEnv, returning the
// path of its combined log. The process is interrupted at test cleanup.
func startPilothouse(t *testing.T, binary string, args []string, extraEnv []string) (waitCh chan error, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "pilothouse.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := logFile.Close(); closeErr != nil {
			t.Errorf("close pilothouse log: %v", closeErr)
		}
	})

	command := exec.CommandContext(t.Context(), binary, args...)
	command.Env = append(os.Environ(), extraEnv...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	t.Cleanup(func() {
		select {
		case <-wait:
			return
		default:
		}
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-wait:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-wait
		}
	})
	return wait, logPath
}

// waitHealthy polls url with client until /healthz answers 200, failing the
// test with the process log if the process exits first.
func waitHealthy(t *testing.T, client *http.Client, url string, wait chan error, logPath string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(url + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case commandErr := <-wait:
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("pilothouse exited before becoming healthy: %v\n%s", commandErr, logs)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pilothouse did not become healthy at %s", url)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// certPoolClient builds a client that verifies the server against exactly
// the given PEM certificate, proving the served certificate is the one on
// disk rather than skipping verification.
func certPoolClient(t *testing.T, certPath string) *http.Client {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("no certificate parsed from %s", certPath)
	}
	return &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// TestTLSWebFlowEndToEnd starts the real binary with operator-supplied
// certificate flags on loopback and exercises the public pages over HTTPS
// with full certificate verification.
func TestTLSWebFlowEndToEnd(t *testing.T) {
	binary := buildPilothouse(t)
	certDir := t.TempDir()
	certPath, keyPath, err := tlscert.LoadOrCreate(certDir, "127.0.0.1", time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	address := "127.0.0.1:" + freePort(t, "127.0.0.1")
	wait, logPath := startPilothouse(t, binary, []string{
		"--listen", address,
		"--tls-cert", certPath,
		"--tls-key", keyPath,
		"--broker-socket", filepath.Join(t.TempDir(), "broker.sock"),
	}, nil)

	client := certPoolClient(t, certPath)
	baseURL := "https://" + address
	waitHealthy(t, client, baseURL, wait, logPath)

	response, err := client.Get(baseURL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", response.StatusCode)
	}
	if !strings.Contains(string(body), "<title>Sign in · Pilothouse</title>") {
		t.Fatalf("GET /login did not render the Pilothouse sign-in page over TLS")
	}
}

// TestSelfSignedTLSOnNonLoopbackBind proves the fail-closed default: a
// non-loopback bind with no certificate configured and no plaintext
// acknowledgment generates a persistent self-signed certificate in
// $STATE_DIRECTORY and serves HTTPS with it.
func TestSelfSignedTLSOnNonLoopbackBind(t *testing.T) {
	binary := buildPilothouse(t)
	stateDir := t.TempDir()
	address := "0.0.0.0:" + freePort(t, "0.0.0.0")
	wait, logPath := startPilothouse(t, binary, []string{
		"--listen", address,
		"--broker-socket", filepath.Join(t.TempDir(), "broker.sock"),
	}, []string{"STATE_DIRECTORY=" + stateDir})

	// The certificate is generated before the listener binds, so once the
	// server is healthy the persisted pair must exist; the verified probe
	// below proves the served certificate is that exact pair.
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://" + net.JoinHostPort("127.0.0.1", port)
	insecure := &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	waitHealthy(t, insecure, baseURL, wait, logPath)

	certPath := filepath.Join(stateDir, "self-signed.crt")
	client := certPoolClient(t, certPath)
	response, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("verified TLS request against the persisted self-signed certificate failed: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", response.StatusCode)
	}
}

// TestNonLoopbackPlaintextRefusedWithoutStateDir proves the guardrail's
// refusal branch behaviorally: a non-loopback bind with no certificate, no
// acknowledgment, and no usable state directory must exit nonzero with the
// remedy message before ever accepting a connection. STATE_DIRECTORY points
// below a regular file, which no privilege level can create through —
// unlike a chmod'd directory, which root writes through.
func TestNonLoopbackPlaintextRefusedWithoutStateDir(t *testing.T) {
	binary := buildPilothouse(t)
	obstacle := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(obstacle, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), binary,
		"--listen", "0.0.0.0:0",
		"--broker-socket", filepath.Join(t.TempDir(), "broker.sock"),
	)
	command.Env = append(os.Environ(), "STATE_DIRECTORY="+filepath.Join(obstacle, "certs"))
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("pilothouse started despite an unpreparable TLS certificate; output:\n%s", output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected an exit error, got %v", err)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("expected a nonzero exit code, got %d", exitErr.ExitCode())
	}
	for _, fragment := range []string{
		"refusing to serve plaintext HTTP on non-loopback address",
		"--tls-cert",
		"--allow-insecure-http",
	} {
		if !strings.Contains(string(output), fragment) {
			t.Fatalf("refusal output missing %q:\n%s", fragment, output)
		}
	}
}
