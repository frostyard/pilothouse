package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/problemrec"
	"github.com/frostyard/std/problems"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenJournal(t *testing.T) (string, *net.UnixConn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return path, conn
}

func readJournalRecord(t *testing.T, conn *net.UnixConn) map[string]string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1<<16)
	n, err := conn.Read(buf)
	require.NoError(t, err, "read a record from the test socket")
	record, err := problems.DecodeEntry(bytes.Clone(buf[:n]))
	require.NoError(t, err)
	return record
}

func expectNoJournalRecord(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	buf := make([]byte, 1<<16)
	n, err := conn.Read(buf)
	require.Errorf(t, err, "unexpected datagram %q", buf[:n])
	var netErr net.Error
	require.True(t, errors.As(err, &netErr) && netErr.Timeout(), "read: %v", err)
}

func TestVersionDefaultsToDev(t *testing.T) {
	// The unstamped default must stay inert; .goreleaser.yaml stamps
	// release builds (packaging.TestBuildsStampMainVersion).
	assert.Equal(t, "dev", version)
	assert.Error(t, problemrec.New(version).Err())
}

// TestPrepareSelfSignedRecordsProblem drives both refusal paths of the
// fail-closed self-signed guardrail and reads the record of each.
func TestPrepareSelfSignedRecordsProblem(t *testing.T) {
	obstacle := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(obstacle, []byte("x"), 0o600))
	tests := []struct {
		name string
		env  map[string]string
	}{
		// LoadOrCreate fails: a path below a regular file cannot be created.
		{"certificate cannot be created", map[string]string{"STATE_DIRECTORY": filepath.Join(obstacle, "certs")}},
		// certStateDir fails: no STATE_DIRECTORY, XDG_STATE_HOME or HOME.
		{"state directory cannot be resolved", map[string]string{"STATE_DIRECTORY": "", "XDG_STATE_HOME": "", "HOME": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			sock, conn := listenJournal(t)
			rec := problemrec.New("1.2.3", problems.WithSocket(sock))

			certPath, keyPath, err := prepareSelfSigned("0.0.0.0:8888", time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)), rec)
			require.Error(t, err)
			assert.Empty(t, certPath)
			assert.Empty(t, keyPath)
			assert.Contains(t, err.Error(), `refusing to serve plaintext HTTP on non-loopback address "0.0.0.0:8888"`)

			record := readJournalRecord(t, conn)
			assert.Equal(t, problems.MessageID, record["MESSAGE_ID"])
			assert.Equal(t, "org.frostyard.pilothouse", record["FROSTYARD_APP_ID"])
			assert.Equal(t, "1.2.3", record["FROSTYARD_APP_VERSION"])
			assert.Equal(t, "daemon", record["FROSTYARD_COMPONENT"])
			assert.Equal(t, "error", record["FROSTYARD_CLASS"])
			assert.Equal(t, "tls.self_signed_failed", record["FROSTYARD_CODE"])
			_, err = problems.Validate(record)
			require.NoError(t, err)
			expectNoJournalRecord(t, conn)
		})
	}
}

func TestPrepareSelfSignedSuccessRecordsNothing(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	sock, conn := listenJournal(t)
	rec := problemrec.New("1.2.3", problems.WithSocket(sock))

	certPath, keyPath, err := prepareSelfSigned("0.0.0.0:8888", time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)), rec)
	require.NoError(t, err)
	assert.FileExists(t, certPath)
	assert.FileExists(t, keyPath)
	expectNoJournalRecord(t, conn)
}
