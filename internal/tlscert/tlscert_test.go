package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fixedNow returns a clock pinned to t.
func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

var testEpoch = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

func TestLoadOrCreateGeneratesUsablePair(t *testing.T) {
	// A nested path proves LoadOrCreate creates its own directory with
	// owner-only permissions rather than requiring it to pre-exist.
	dir := filepath.Join(t.TempDir(), "state", "certs")
	certPath, keyPath, err := LoadOrCreate(dir, "", fixedNow(testEpoch), discardLogger())
	require.NoError(t, err)

	// The pair must be loadable by the exact call the server makes.
	_, err = tls.LoadX509KeyPair(certPath, keyPath)
	require.NoError(t, err)

	cert, err := parseCertFile(certPath)
	require.NoError(t, err)
	assert.False(t, cert.IsCA)
	assert.Equal(t, x509.KeyUsageDigitalSignature, cert.KeyUsage)
	assert.Contains(t, cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
	require.NoError(t, cert.VerifyHostname("localhost"))
	require.NoError(t, cert.VerifyHostname("127.0.0.1"))
	require.NoError(t, cert.VerifyHostname("::1"))
	if hostname, hostErr := os.Hostname(); hostErr == nil && hostname != "" {
		assert.NoError(t, cert.VerifyHostname(hostname))
	}
	_, ok := cert.PublicKey.(*ecdsa.PublicKey)
	assert.True(t, ok, "expected an ECDSA public key")
	assert.Equal(t, testEpoch.Add(-clockSkew), cert.NotBefore.UTC())
	assert.Equal(t, testEpoch.Add(lifetime), cert.NotAfter.UTC())

	for path, want := range map[string]os.FileMode{certPath: 0o600, keyPath: 0o600} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.Equal(t, want, info.Mode().Perm(), path)
	}
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	// Atomic-write temp files must not survive a successful generation.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestLoadOrCreateReusesExistingPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := LoadOrCreate(dir, "", fixedNow(testEpoch), discardLogger())
	require.NoError(t, err)
	firstCert, err := os.ReadFile(certPath)
	require.NoError(t, err)
	firstKey, err := os.ReadFile(keyPath)
	require.NoError(t, err)

	// A later start well before the renewal window must not churn the pair.
	_, _, err = LoadOrCreate(dir, "", fixedNow(testEpoch.Add(24*time.Hour)), discardLogger())
	require.NoError(t, err)
	secondCert, err := os.ReadFile(certPath)
	require.NoError(t, err)
	secondKey, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, firstCert, secondCert)
	assert.Equal(t, firstKey, secondKey)
}

func TestLoadOrCreateRegenerates(t *testing.T) {
	tests := []struct {
		name string
		// mutate corrupts the freshly generated pair, or returns the
		// clock/listen host a later startup should observe.
		mutate     func(t *testing.T, certPath, keyPath string)
		now        time.Time
		listenHost string
	}{
		{
			name: "expired certificate",
			now:  testEpoch.Add(lifetime + time.Hour),
		},
		{
			name: "inside renewal window",
			now:  testEpoch.Add(lifetime - renewalWindow + time.Hour),
		},
		{
			name:       "listen host not covered",
			now:        testEpoch.Add(time.Hour),
			listenHost: "203.0.113.9",
		},
		{
			name: "corrupt certificate",
			now:  testEpoch.Add(time.Hour),
			mutate: func(t *testing.T, certPath, _ string) {
				require.NoError(t, os.WriteFile(certPath, []byte("not a certificate"), 0o600))
			},
		},
		{
			name: "missing key",
			now:  testEpoch.Add(time.Hour),
			mutate: func(t *testing.T, _, keyPath string) {
				require.NoError(t, os.Remove(keyPath))
			},
		},
		{
			name: "key does not match certificate",
			now:  testEpoch.Add(time.Hour),
			mutate: func(t *testing.T, _, keyPath string) {
				stranger, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				require.NoError(t, err)
				der, err := x509.MarshalECPrivateKey(stranger)
				require.NoError(t, err)
				encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
				require.NoError(t, os.WriteFile(keyPath, encoded, 0o600))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			certPath, keyPath, err := LoadOrCreate(dir, "", fixedNow(testEpoch), discardLogger())
			require.NoError(t, err)
			original, err := os.ReadFile(certPath)
			require.NoError(t, err)
			if tt.mutate != nil {
				tt.mutate(t, certPath, keyPath)
			}

			_, _, err = LoadOrCreate(dir, tt.listenHost, fixedNow(tt.now), discardLogger())
			require.NoError(t, err)
			regenerated, err := os.ReadFile(certPath)
			require.NoError(t, err)
			assert.NotEqual(t, original, regenerated, "expected a regenerated certificate")
			_, err = tls.LoadX509KeyPair(certPath, keyPath)
			require.NoError(t, err)
			if tt.listenHost != "" {
				cert, parseErr := parseCertFile(certPath)
				require.NoError(t, parseErr)
				assert.NoError(t, cert.VerifyHostname(tt.listenHost))
			}
		})
	}
}

func TestLoadOrCreateCoversConcreteListenHost(t *testing.T) {
	dir := t.TempDir()
	certPath, _, err := LoadOrCreate(dir, "198.51.100.7", fixedNow(testEpoch), discardLogger())
	require.NoError(t, err)
	cert, err := parseCertFile(certPath)
	require.NoError(t, err)
	assert.NoError(t, cert.VerifyHostname("198.51.100.7"))

	// An unspecified listen host imposes no SAN requirement, so the pair
	// generated for a concrete host is reused as-is.
	before, err := os.ReadFile(certPath)
	require.NoError(t, err)
	for _, host := range []string{"", "0.0.0.0", "::"} {
		_, _, err = LoadOrCreate(dir, host, fixedNow(testEpoch.Add(time.Hour)), discardLogger())
		require.NoError(t, err)
	}
	after, err := os.ReadFile(certPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestLoadOrCreateHostnameListenHost(t *testing.T) {
	dir := t.TempDir()
	certPath, _, err := LoadOrCreate(dir, "console.internal", fixedNow(testEpoch), discardLogger())
	require.NoError(t, err)
	cert, err := parseCertFile(certPath)
	require.NoError(t, err)
	assert.NoError(t, cert.VerifyHostname("console.internal"))
}

func TestLoadOrCreateUncreatableDirectory(t *testing.T) {
	// A path below a regular file cannot be created by any privilege
	// level, unlike a chmod'd directory, which root writes through.
	base := t.TempDir()
	obstacle := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(obstacle, []byte("x"), 0o600))

	_, _, err := LoadOrCreate(filepath.Join(obstacle, "certs"), "", fixedNow(testEpoch), discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate directory")
}
