//go:build sdjournal

// This file holds the real journald probe, backed by the cgo systemd
// bindings (go-systemd/sdjournal), which require the libsystemd
// development headers. It is compiled only when the "sdjournal" build tag
// is set; without it, probe_journald_stub.go provides a header-free
// fallback so go vet / go test / golangci-lint (and the daemon's own
// unqualified go build) all work on toolchains that lack libsystemd-dev.
// This mirrors internal/modules/{services,logs}/journal's
// journal_sdjournal.go / journal_stub.go split exactly.
package capability

import "github.com/coreos/go-systemd/v22/sdjournal"

// ProbeJournald reports journald present iff opening the system journal
// for reading succeeds. It opens and immediately closes the journal;
// entries are never read here -- this is a presence probe, not a reader.
func ProbeJournald() Set {
	journal, err := sdjournal.NewJournal()
	if err != nil {
		return New()
	}
	defer journal.Close()

	return New(Journald)
}
