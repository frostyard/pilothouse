package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// serveMode is how run() serves HTTP traffic once the listen address and
// TLS material are resolved.
type serveMode int

const (
	// serveHTTP is plaintext HTTP: the loopback default, or a non-loopback
	// bind the operator explicitly acknowledged with --allow-insecure-http.
	serveHTTP serveMode = iota
	// serveTLSProvided is HTTPS with an operator-supplied certificate.
	serveTLSProvided
	// serveTLSSelfSigned is HTTPS with the auto-generated self-signed
	// certificate: the fail-closed result of binding beyond loopback with
	// no certificate configured and no plaintext acknowledgment.
	serveTLSSelfSigned
)

// resolveString applies the flag → environment → default precedence shared
// by every single-valued option: a flag passed on the command line wins even
// when its value equals the built-in default, otherwise a non-empty
// environment value wins, otherwise flagValue already holds the default.
func resolveString(flagSet bool, flagValue, envValue string) string {
	if flagSet {
		return flagValue
	}
	if envValue = strings.TrimSpace(envValue); envValue != "" {
		return envValue
	}
	return flagValue
}

// resolveBool is resolveString for boolean options. A malformed environment
// value is a startup error rather than a silent false, so a typo like
// PILOTHOUSE_ALLOW_INSECURE_HTTP=yes cannot be mistaken for either choice.
func resolveBool(flagSet, flagValue bool, envName, envValue string) (bool, error) {
	if flagSet {
		return flagValue, nil
	}
	if envValue = strings.TrimSpace(envValue); envValue == "" {
		return flagValue, nil
	}
	parsed, err := strconv.ParseBool(envValue)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean (got %q)", envName, envValue)
	}
	return parsed, nil
}

// isLoopbackListen reports whether addr binds only to loopback. It fails
// closed: the unspecified addresses ("0.0.0.0", "::", empty host) and any
// hostname other than the literal "localhost" are treated as non-loopback —
// no DNS resolution happens at startup, so a hostname that resolves to
// 127.0.0.1 still requires TLS or an explicit plaintext acknowledgment.
func isLoopbackListen(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("listen address must be host:port (got %q): %w", addr, err)
	}
	if host == "" {
		return false, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	return strings.EqualFold(host, "localhost"), nil
}

// decideServeMode picks how to serve given the resolved inputs. Operator-
// supplied certificates always win, the loopback default stays plaintext
// HTTP, an explicit acknowledgment permits plaintext beyond loopback, and
// the remaining case — non-loopback, no certificate, no acknowledgment —
// falls closed to the self-signed certificate.
func decideServeMode(loopback, certConfigured, allowInsecure bool) serveMode {
	switch {
	case certConfigured:
		return serveTLSProvided
	case loopback:
		return serveHTTP
	case allowInsecure:
		return serveHTTP
	default:
		return serveTLSSelfSigned
	}
}

// certStateDir resolves where the self-signed certificate persists. Under
// systemd the unit's StateDirectory= exports STATE_DIRECTORY (colon-
// separated when a unit declares several; the first is ours). Outside
// systemd — `go run`, a dev shell — the XDG state directory keeps the
// certificate stable across restarts without requiring any setup.
func certStateDir() (string, error) {
	if dir := os.Getenv("STATE_DIRECTORY"); dir != "" {
		if first, _, ok := strings.Cut(dir, ":"); ok {
			return first, nil
		}
		return dir, nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "pilothouse"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve state directory for self-signed certificate: %w", err)
	}
	return filepath.Join(home, ".local", "state", "pilothouse"), nil
}

// selfSignedUnavailable builds the refusal error for the fail-closed path:
// a non-loopback bind whose self-signed certificate cannot be prepared. It
// names every way out so the operator does not have to read source to
// proceed.
func selfSignedUnavailable(addr string, cause error) error {
	return fmt.Errorf("refusing to serve plaintext HTTP on non-loopback address %q: no TLS certificate could be prepared (%v). Either (1) provide --tls-cert and --tls-key (or PILOTHOUSE_TLS_CERT/PILOTHOUSE_TLS_KEY), (2) make a writable state directory available for an auto-generated self-signed certificate (systemd sets $STATE_DIRECTORY; the packaged unit uses /var/lib/pilothouse/web), or (3) acknowledge plaintext HTTP with --allow-insecure-http or PILOTHOUSE_ALLOW_INSECURE_HTTP=1", addr, cause)
}
