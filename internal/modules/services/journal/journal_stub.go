//go:build !sdjournal

package journal

import (
	"context"
	"errors"
	"time"

	"github.com/frostyard/pilothouse/internal/modules/services"
)

// Reader is the fallback journal reader compiled when the "sdjournal" build
// tag is NOT set. It keeps the daemon (and go vet / test / lint / govulncheck)
// buildable on toolchains without the libsystemd development headers by
// avoiding the cgo systemd bindings entirely. Journald diagnostics are
// reported as unavailable rather than read.
//
// Build the daemon with `-tags sdjournal` (and libsystemd-dev installed) to
// get the real systemd-journal-backed reader in journal_sdjournal.go.
type Reader struct{}

// New returns the fallback reader.
func New() Reader { return Reader{} }

var errUnsupported = errors.New(
	"journald reader unavailable: build with -tags sdjournal (requires libsystemd-dev)",
)

// Read always reports the journal as unavailable. Callers (SystemManager)
// already translate a reader error into a graceful "diagnostics unavailable"
// result, so an unbuilt reader degrades cleanly rather than crashing.
func (Reader) Read(_ context.Context, _ string, _ time.Time, _ int) ([]services.JournalRecord, error) {
	return nil, errUnsupported
}
