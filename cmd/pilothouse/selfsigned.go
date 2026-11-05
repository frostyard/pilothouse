package main

import (
	"log/slog"
	"net"
	"time"

	"github.com/frostyard/pilothouse/internal/problemrec"
	"github.com/frostyard/pilothouse/internal/tlscert"
	"github.com/frostyard/std/problems"
)

// prepareSelfSigned loads or creates the self-signed certificate of a
// non-loopback bind in certStateDir. When it cannot, it records the cause
// under problemrec.CodeTLSSelfSignedFailed (local journal only; a nil or
// inert rec records nothing) and returns the selfSignedUnavailable refusal.
func prepareSelfSigned(addr string, now func() time.Time, logger *slog.Logger, rec *problems.Recorder) (string, string, error) {
	host, _, _ := net.SplitHostPort(addr)
	dir, err := certStateDir()
	if err != nil {
		rec.Error(problemrec.CodeTLSSelfSignedFailed, err)
		return "", "", selfSignedUnavailable(addr, err)
	}
	certPath, keyPath, err := tlscert.LoadOrCreate(dir, host, now, logger)
	if err != nil {
		rec.Error(problemrec.CodeTLSSelfSignedFailed, err)
		return "", "", selfSignedUnavailable(addr, err)
	}
	return certPath, keyPath, nil
}
