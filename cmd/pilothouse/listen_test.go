package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveStringPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		flagSet   bool
		flagValue string
		envValue  string
		want      string
	}{
		{"flag beats env", true, "127.0.0.1:8888", "0.0.0.0:8888", "127.0.0.1:8888"},
		{"flag set to default still beats env", true, "127.0.0.1:8888", "10.0.1.200:8888", "127.0.0.1:8888"},
		{"env beats default", false, "127.0.0.1:8888", "0.0.0.0:8888", "0.0.0.0:8888"},
		{"env whitespace trimmed", false, "", "  /etc/pilothouse/tls/console.crt  ", "/etc/pilothouse/tls/console.crt"},
		{"empty env keeps default", false, "127.0.0.1:8888", "", "127.0.0.1:8888"},
		{"blank env keeps default", false, "127.0.0.1:8888", "   ", "127.0.0.1:8888"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveString(tt.flagSet, tt.flagValue, tt.envValue))
		})
	}
}

func TestResolveBool(t *testing.T) {
	tests := []struct {
		name      string
		flagSet   bool
		flagValue bool
		envValue  string
		want      bool
		wantErr   bool
	}{
		{"flag beats env", true, false, "true", false, false},
		{"env true", false, false, "true", true, false},
		{"env numeric", false, false, "1", true, false},
		{"env false", false, false, "false", false, false},
		{"empty env keeps default", false, false, "", false, false},
		{"malformed env is an error not a silent false", false, false, "yes", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBool(tt.flagSet, tt.flagValue, "PILOTHOUSE_ALLOW_INSECURE_HTTP", tt.envValue)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "PILOTHOUSE_ALLOW_INSECURE_HTTP")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsLoopbackListen(t *testing.T) {
	tests := []struct {
		addr     string
		loopback bool
		wantErr  bool
	}{
		{"127.0.0.1:8888", true, false},
		{"127.0.0.2:8888", true, false},
		{"[::1]:8888", true, false},
		{"localhost:8888", true, false},
		{"LOCALHOST:8888", true, false},
		{"0.0.0.0:8888", false, false},
		{"[::]:8888", false, false},
		{":8888", false, false},
		{"192.168.1.10:8888", false, false},
		{"10.0.1.200:8888", false, false},
		// Link-local with a zone is not parseable as a bare IP; it is a
		// non-loopback bind either way.
		{"[fe80::1%eth0]:8888", false, false},
		// Hostnames other than the literal "localhost" fail closed as
		// non-loopback: no DNS resolution happens at startup.
		{"myhost.lan:8888", false, false},
		{"cayo:8888", false, false},
		// A bare address without a port is a startup error.
		{"127.0.0.1", false, true},
		{"", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := isLoopbackListen(tt.addr)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "host:port")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.loopback, got)
		})
	}
}

// TestDecideServeMode covers the full three-input truth table: operator
// certificates always win, loopback stays plaintext, and the non-loopback
// no-certificate case falls closed to self-signed TLS unless plaintext was
// explicitly acknowledged.
func TestDecideServeMode(t *testing.T) {
	tests := []struct {
		loopback, certConfigured, allowInsecure bool
		want                                    serveMode
	}{
		{true, false, false, serveHTTP},
		{true, false, true, serveHTTP},
		{true, true, false, serveTLSProvided},
		{true, true, true, serveTLSProvided},
		{false, true, false, serveTLSProvided},
		{false, true, true, serveTLSProvided},
		{false, false, false, serveTLSSelfSigned},
		{false, false, true, serveHTTP},
	}
	for _, tt := range tests {
		got := decideServeMode(tt.loopback, tt.certConfigured, tt.allowInsecure)
		assert.Equal(t, tt.want, got,
			"loopback=%v certConfigured=%v allowInsecure=%v", tt.loopback, tt.certConfigured, tt.allowInsecure)
	}
}

func TestCertStateDir(t *testing.T) {
	t.Run("systemd state directory wins", func(t *testing.T) {
		t.Setenv("STATE_DIRECTORY", "/var/lib/pilothouse/web")
		t.Setenv("XDG_STATE_HOME", "/ignored")
		dir, err := certStateDir()
		require.NoError(t, err)
		assert.Equal(t, "/var/lib/pilothouse/web", dir)
	})
	t.Run("first entry of a colon-separated list", func(t *testing.T) {
		t.Setenv("STATE_DIRECTORY", "/var/lib/pilothouse/web:/var/lib/other")
		dir, err := certStateDir()
		require.NoError(t, err)
		assert.Equal(t, "/var/lib/pilothouse/web", dir)
	})
	t.Run("xdg fallback", func(t *testing.T) {
		t.Setenv("STATE_DIRECTORY", "")
		t.Setenv("XDG_STATE_HOME", "/home/dev/.local/state")
		dir, err := certStateDir()
		require.NoError(t, err)
		assert.Equal(t, "/home/dev/.local/state/pilothouse", dir)
	})
	t.Run("home fallback", func(t *testing.T) {
		t.Setenv("STATE_DIRECTORY", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "/home/dev")
		dir, err := certStateDir()
		require.NoError(t, err)
		assert.Equal(t, "/home/dev/.local/state/pilothouse", dir)
	})
}
