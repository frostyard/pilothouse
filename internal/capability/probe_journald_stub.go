//go:build !sdjournal

// This file holds the fallback journald probe, compiled when the
// "sdjournal" build tag is NOT set. It keeps go vet / go test /
// golangci-lint (and an unqualified go build) working on toolchains
// without the libsystemd development headers, by never touching the cgo
// systemd bindings at all. Journald is simply reported absent.
//
// Build with `-tags sdjournal` (and libsystemd-dev installed) to get the
// real journal-backed probe in probe_journald.go.
package capability

// ProbeJournald always reports journald absent when built without the
// "sdjournal" tag.
func ProbeJournald() Set {
	return New()
}
