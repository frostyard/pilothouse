package problemrec_test

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/problemrec"
	"github.com/frostyard/std/problems"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperEnv selects a helper mode of the re-executed test binary. Only the
// tests below set it.
const helperEnv = "PILOTHOUSE_PROBLEMREC_TEST_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case "crash":
		helperCrash()
	case "leftover":
		helperLeftover()
	}
	os.Exit(3)
}

// helperCrash arms crash output for a release build and crashes with an
// unrecovered panic in a goroutine, as a daemon bug would.
func helperCrash() {
	rec := problemrec.New("1.2.3", problems.WithSocket(os.Getenv("PILOTHOUSE_PROBLEMREC_TEST_SOCKET")))
	if err := rec.CrashOutput(); err != nil {
		os.Exit(4)
	}
	go crashingGoroutine()
	select {}
}

//go:noinline
func crashingGoroutine() {
	panic("problemrec crash")
}

// helperLeftover is the next start: CrashOutput turns the leftover crash file
// into 1 record on the test socket.
func helperLeftover() {
	rec := problemrec.New("1.2.4", problems.WithSocket(os.Getenv("PILOTHOUSE_PROBLEMREC_TEST_SOCKET")))
	if err := rec.CrashOutput(); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

func listen(t *testing.T) (string, *net.UnixConn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return path, conn
}

func readRecord(t *testing.T, conn *net.UnixConn) map[string]string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1<<20)
	n, err := conn.Read(buf)
	require.NoError(t, err, "read a record from the test socket")
	record, err := problems.DecodeEntry(bytes.Clone(buf[:n]))
	require.NoError(t, err)
	return record
}

func expectNone(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	buf := make([]byte, 1<<20)
	n, err := conn.Read(buf)
	require.Errorf(t, err, "unexpected datagram %q", buf[:n])
	var netErr net.Error
	require.True(t, errors.As(err, &netErr) && netErr.Timeout(), "read: %v", err)
}

func assertIdentity(t *testing.T, record map[string]string, version, class string) problems.Detail {
	t.Helper()
	assert.Equal(t, problems.MessageID, record["MESSAGE_ID"])
	assert.Equal(t, "org.frostyard.pilothouse", record["FROSTYARD_APP_ID"])
	assert.Equal(t, version, record["FROSTYARD_APP_VERSION"])
	assert.Equal(t, "daemon", record["FROSTYARD_COMPONENT"])
	assert.Equal(t, class, record["FROSTYARD_CLASS"])
	detail, err := problems.Validate(record)
	require.NoError(t, err)
	return detail
}

func TestCodesAndAppID(t *testing.T) {
	want := []string{"tls.self_signed_failed", "audit.open_failed", "jobs.open_failed", "audit.write_failed", "jobs.write_failed"}
	assert.Equal(t, want, problemrec.Codes)
	assert.Equal(t, "org.frostyard.pilothouse", problemrec.AppID)
}

// TestNewRecordsCodedErrorForRelease writes 1 record for each code: a code
// that fails the schema pattern would write nothing and fail the read.
func TestNewRecordsCodedErrorForRelease(t *testing.T) {
	sock, conn := listen(t)
	rec := problemrec.New("1.2.3", problems.WithSocket(sock))
	require.NoError(t, rec.Err())

	for _, code := range problemrec.Codes {
		rec.Error(code, errors.New("store: timeout"))
		record := readRecord(t, conn)
		detail := assertIdentity(t, record, "1.2.3", "error")
		assert.Equal(t, code, record["FROSTYARD_CODE"])
		assert.Equal(t, code, detail.Code)
		assert.Equal(t, "store: timeout", record["MESSAGE"])
	}
}

func TestNewIsInertForNonReleaseVersions(t *testing.T) {
	sock, conn := listen(t)
	for _, version := range []string{"dev", "0.0.0-abc1234-snapshot", "1.0.0-dev"} {
		rec := problemrec.New(version, problems.WithSocket(sock))
		assert.Errorf(t, rec.Err(), "version %q must make an inert recorder", version)
		rec.Error(problemrec.CodeJobsOpenFailed, errors.New("boom"))
		handler := problemrec.Handler(rec, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("inert") }))
		assert.PanicsWithValue(t, "inert", func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		})
		assert.Error(t, rec.CrashOutput(), "an inert recorder arms no crash output")
	}
	expectNone(t, conn)
}

func TestHandlerPassesThroughWithoutPanic(t *testing.T) {
	sock, conn := listen(t)
	rec := problemrec.New("1.2.3", problems.WithSocket(sock))
	handler := problemrec.Handler(rec, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusTeapot, response.Code)
	assert.Equal(t, "ok", response.Body.String())
	expectNone(t, conn)
}

//go:noinline
func panickingHandler(http.ResponseWriter, *http.Request) {
	panic("handler boom")
}

func TestHandlerRecordsPanicAndRepanics(t *testing.T) {
	sock, conn := listen(t)
	rec := problemrec.New("1.2.3", problems.WithSocket(sock))
	handler := problemrec.Handler(rec, http.HandlerFunc(panickingHandler))

	// Through a real server: net/http recovers the re-panic and drops the
	// connection, exactly as it does without the middleware.
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.Start()
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL)
	if err == nil {
		_ = response.Body.Close()
	}
	require.Error(t, err, "the server must abort the request after the panic")

	record := readRecord(t, conn)
	assertIdentity(t, record, "1.2.3", "panic")
	assert.Equal(t, "handler boom", record["MESSAGE"])
	assert.NotContains(t, record, "FROSTYARD_CODE")
	frames := strings.Split(record["FROSTYARD_FRAMES"], "\n")
	assert.True(t, slices.Contains(frames, "github.com/frostyard/pilothouse/internal/problemrec_test.panickingHandler"), "frames %q", frames)

	// Directly: the panic value propagates to the caller.
	assert.PanicsWithValue(t, "handler boom", func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
	record = readRecord(t, conn)
	assertIdentity(t, record, "1.2.3", "panic")
}

func runHelper(t *testing.T, mode string, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	cmd.Env = append(cmd.Env, env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stderr.String()
	default:
		t.Fatalf("run helper: %v", err)
		return -1, ""
	}
}

func TestCrashOutputBecomesRecordAtNextStart(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "frostyard-problems", "org.frostyard.pilothouse")
	sock, conn := listen(t)
	env := []string{"STATE_DIRECTORY=" + state, "PILOTHOUSE_PROBLEMREC_TEST_SOCKET=" + sock}

	code, stderr := runHelper(t, "crash", env...)
	require.Equalf(t, 2, code, "the crash helper must die of the panic; stderr:\n%s", stderr)
	assert.Contains(t, stderr, "problemrec crash")
	files, err := filepath.Glob(filepath.Join(dir, "*.crash"))
	require.NoError(t, err)
	require.Lenf(t, files, 1, "crash files in %s", dir)
	// The crash itself sends nothing: it is recorded at the next start.
	expectNone(t, conn)

	code, stderr = runHelper(t, "leftover", env...)
	require.Equalf(t, 0, code, "leftover helper; stderr:\n%s", stderr)
	record := readRecord(t, conn)
	assertIdentity(t, record, "1.2.3", "panic")
	assert.Equal(t, "panic: problemrec crash", record["MESSAGE"])
	frames := strings.Split(record["FROSTYARD_FRAMES"], "\n")
	assert.True(t, slices.Contains(frames, "github.com/frostyard/pilothouse/internal/problemrec_test.crashingGoroutine"), "frames %q", frames)
	expectNone(t, conn)
	_, err = os.Stat(files[0])
	assert.True(t, os.IsNotExist(err), "the leftover crash file must be removed: %v", err)
}
