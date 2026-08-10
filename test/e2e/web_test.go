package e2e_test

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicWebFlowEndToEnd(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "pilothouse")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/pilothouse")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pilothouse: %v\n%s", err, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "pilothouse.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	command := exec.CommandContext(
		t.Context(),
		binary,
		"--listen", address,
		"--broker-socket", filepath.Join(t.TempDir(), "broker.sock"),
	)
	command.Dir = repositoryRoot
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	processExited := false
	t.Cleanup(func() {
		if processExited {
			return
		}
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-wait:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-wait
		}
	})

	client := &http.Client{
		Timeout: time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	baseURL := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, requestErr := client.Get(baseURL + "/healthz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case commandErr := <-wait:
			processExited = true
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("pilothouse exited before becoming healthy: %v\n%s", commandErr, logs)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("pilothouse did not become healthy")
		}
		time.Sleep(25 * time.Millisecond)
	}

	response, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("GET /healthz = %d %q, want 200 %q", response.StatusCode, body, "ok\n")
	}
	if response.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("GET /healthz X-Frame-Options = %q, want DENY", response.Header.Get("X-Frame-Options"))
	}

	response, err = client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/login" {
		t.Fatalf("GET / = %d Location %q, want 303 /login", response.StatusCode, response.Header.Get("Location"))
	}

	response, err = client.Get(baseURL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", response.StatusCode)
	}
	if !strings.Contains(string(body), "<title>Sign in · Pilothouse</title>") {
		t.Fatalf("GET /login did not render the Pilothouse sign-in page")
	}
}
