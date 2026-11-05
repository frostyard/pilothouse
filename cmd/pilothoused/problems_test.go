package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
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

// TestOpenStoresRecordProblem opens each database at a directory path, which
// bolt cannot open at any privilege level, and reads the record.
func TestOpenStoresRecordProblem(t *testing.T) {
	tests := []struct {
		name    string
		open    func(string, *problems.Recorder) error
		code    string
		wantErr string
	}{
		{"audit", func(path string, rec *problems.Recorder) error { _, err := openAuditStore(path, rec); return err }, "audit.open_failed", "open audit database: "},
		{"jobs", func(path string, rec *problems.Recorder) error { _, err := openJobStore(path, rec); return err }, "jobs.open_failed", "open jobs database: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock, conn := listenJournal(t)
			rec := problemrec.New("1.2.3", problems.WithSocket(sock))

			err := tt.open(t.TempDir(), rec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			record := readJournalRecord(t, conn)
			assert.Equal(t, problems.MessageID, record["MESSAGE_ID"])
			assert.Equal(t, "org.frostyard.pilothouse", record["FROSTYARD_APP_ID"])
			assert.Equal(t, "1.2.3", record["FROSTYARD_APP_VERSION"])
			assert.Equal(t, "daemon", record["FROSTYARD_COMPONENT"])
			assert.Equal(t, "error", record["FROSTYARD_CLASS"])
			assert.Equal(t, tt.code, record["FROSTYARD_CODE"])
			assert.Equal(t, err.Error(), record["MESSAGE"])
			_, err = problems.Validate(record)
			require.NoError(t, err)
		})
	}
}

func TestOpenStoresSuccessRecordsNothing(t *testing.T) {
	sock, conn := listenJournal(t)
	rec := problemrec.New("1.2.3", problems.WithSocket(sock))
	dir := t.TempDir()

	auditStore, err := openAuditStore(filepath.Join(dir, "audit.db"), rec)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, auditStore.Close()) })
	jobStore, err := openJobStore(filepath.Join(dir, "jobs.db"), rec)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jobStore.Close()) })
	expectNoJournalRecord(t, conn)
}

// TestActionRegistriesRecordAuditWriteFailure proves the wiring of run():
// both registries record a failed audit write with the real recorder. A
// closed audit store makes every Begin fail.
func TestActionRegistriesRecordAuditWriteFailure(t *testing.T) {
	sock, conn := listenJournal(t)
	rec := problemrec.New("1.2.3", problems.WithSocket(sock))
	dir := t.TempDir()
	auditStore, err := openAuditStore(filepath.Join(dir, "audit.db"), rec)
	require.NoError(t, err)
	jobStore, err := openJobStore(filepath.Join(dir, "jobs.db"), rec)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jobStore.Close()) })
	actions, streamActions := newActionRegistries(auditStore, jobStore, rec)
	require.NoError(t, actions.Register("test.action", true, func(context.Context, auth.Identity, map[string]string) error { return nil }))
	require.NoError(t, streamActions.Register(broker.StreamActionDefinition{
		ID: "test.upload", Admin: true, Limit: 16,
		Resource: func(map[string]string) (string, error) { return "files/x", nil },
		Handler:  func(context.Context, auth.Identity, map[string]string, io.Reader) error { return nil },
	}))
	require.NoError(t, auditStore.Close())
	admin := auth.Identity{Admin: true, UID: 1000, Username: "admin"}

	err = actions.Execute(context.Background(), admin, "test.action", nil, "")
	require.ErrorContains(t, err, "record action intent: ")
	err = streamActions.Execute(context.Background(), admin, "test.upload", map[string]string{}, strings.NewReader("x"))
	require.ErrorContains(t, err, "record stream action intent: ")

	for range 2 {
		record := readJournalRecord(t, conn)
		assert.Equal(t, "error", record["FROSTYARD_CLASS"])
		assert.Equal(t, "audit.write_failed", record["FROSTYARD_CODE"])
		_, err = problems.Validate(record)
		require.NoError(t, err)
	}
	expectNoJournalRecord(t, conn)
}
