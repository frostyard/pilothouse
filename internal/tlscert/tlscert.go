// Package tlscert prepares the self-signed certificate the web binary
// serves when an operator binds it to a non-loopback address without
// supplying their own certificate. The certificate is an appliance-style
// convenience: transport encryption with a one-time browser warning, not a
// substitute for an operator-provided certificate. The package never
// touches the network; everything it needs comes from the local host at
// generation time.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	certFile = "self-signed.crt"
	keyFile  = "self-signed.key"

	// lifetime is how long a generated certificate is valid. renewalWindow
	// is how close to expiry an existing certificate may get before the
	// next startup regenerates it; combined with routine service restarts
	// (package upgrades, reboots) this keeps a long-running install from
	// ever presenting an expired certificate in practice. Expiry is only
	// checked at startup, never mid-run.
	lifetime      = 365 * 24 * time.Hour
	renewalWindow = 30 * 24 * time.Hour

	// clockSkew backdates NotBefore so a host whose clock runs slightly
	// ahead of a browser's does not present a not-yet-valid certificate.
	clockSkew = 5 * time.Minute
)

// LoadOrCreate returns the certificate and key paths under dir, generating
// a new self-signed pair when no usable one exists. An existing pair is
// reused unless it fails to parse, the key does not match the certificate,
// the certificate is expired or inside the renewal window, or listenHost
// names a concrete IP or hostname the certificate's SANs do not cover
// (a host renumbered by DHCP regenerates on its next start). listenHost is
// the host part of the listen address; an empty or unspecified host
// ("0.0.0.0", "::") imposes no SAN requirement beyond the generated
// defaults. now is injectable for tests and must not be nil.
func LoadOrCreate(dir, listenHost string, now func() time.Time, logger *slog.Logger) (string, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create certificate directory: %w", err)
	}
	certPath := filepath.Join(dir, certFile)
	keyPath := filepath.Join(dir, keyFile)

	reason := regenerationReason(certPath, keyPath, listenHost, now())
	if reason == "" {
		return certPath, keyPath, nil
	}
	logger.Info("generating self-signed certificate", "reason", reason, "cert", certPath)

	certPEM, keyPEM, err := generate(sanHosts(listenHost), now())
	if err != nil {
		return "", "", fmt.Errorf("generate self-signed certificate: %w", err)
	}
	// Key before certificate: a crash between the two renames can leave a
	// fresh key alongside a stale certificate (repaired on next start by
	// the key-match check) but never a served certificate whose key is
	// missing.
	if err := writeFileAtomic(keyPath, keyPEM); err != nil {
		return "", "", fmt.Errorf("write key: %w", err)
	}
	if err := writeFileAtomic(certPath, certPEM); err != nil {
		return "", "", fmt.Errorf("write certificate: %w", err)
	}
	return certPath, keyPath, nil
}

// regenerationReason reports why the pair at certPath/keyPath cannot be
// reused, or "" when it can.
func regenerationReason(certPath, keyPath, listenHost string, now time.Time) string {
	cert, err := parseCertFile(certPath)
	if err != nil {
		return fmt.Sprintf("certificate unusable: %v", err)
	}
	key, err := parseKeyFile(keyPath)
	if err != nil {
		return fmt.Sprintf("key unusable: %v", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !key.PublicKey.Equal(pub) {
		return "key does not match certificate"
	}
	if now.After(cert.NotAfter) {
		return "certificate expired"
	}
	if now.After(cert.NotAfter.Add(-renewalWindow)) {
		return "certificate inside renewal window"
	}
	if host := concreteHost(listenHost); host != "" {
		if err := cert.VerifyHostname(host); err != nil {
			return fmt.Sprintf("certificate does not cover listen host %q", host)
		}
	}
	return ""
}

// concreteHost returns listenHost when it names a specific IP or hostname
// the certificate must cover, and "" for the empty or unspecified hosts
// that mean "every local address".
func concreteHost(listenHost string) string {
	if listenHost == "" {
		return ""
	}
	if ip := net.ParseIP(listenHost); ip != nil && ip.IsUnspecified() {
		return ""
	}
	return listenHost
}

// sanHosts collects the DNS names and IP addresses the certificate should
// cover: the machine's hostname, localhost, both loopback addresses, every
// global-unicast interface address at generation time, and the concrete
// listen host when it is not already implied.
func sanHosts(listenHost string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if name, err := os.Hostname(); err == nil && name != "" {
		hosts = append([]string{name}, hosts...)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipNet.IP.IsGlobalUnicast() {
				hosts = append(hosts, ipNet.IP.String())
			}
		}
	}
	if host := concreteHost(listenHost); host != "" && !slices.Contains(hosts, host) {
		hosts = append(hosts, host)
	}
	return hosts
}

// generate builds a self-signed ECDSA P-256 leaf certificate covering
// hosts and returns PEM-encoded certificate and key.
func generate(hosts []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}
	commonName := "pilothouse"
	if name, hostErr := os.Hostname(); hostErr == nil && name != "" {
		commonName = name
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func parseCertFile(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: no CERTIFICATE PEM block", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKeyFile(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("%s: no EC PRIVATE KEY PEM block", path)
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// writeFileAtomic writes data to path via a same-directory temp file,
// fsync, and rename, so a crash never leaves a truncated file at path.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		tmp = nil
		_ = os.Remove(name)
		return err
	}
	tmp = nil
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
