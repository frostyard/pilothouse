package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testRPMName = "frostyard-pilothouse-0.6.0-1.x86_64.rpm"

type memoryTransport struct {
	metadata              []byte
	rpm                   []byte
	metadataStatus        int
	rpmStatus             int
	metadataContentLength *int64
	rpmContentLength      *int64
	metadataCloseError    error
	rpmCloseError         error
	closeCount            int
	requests              []*http.Request
}

func (t *memoryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, request.Clone(request.Context()))

	status := t.metadataStatus
	body := t.metadata
	contentLength := t.metadataContentLength
	closeError := t.metadataCloseError
	if request.URL.String() != latestReleaseURL {
		status = t.rpmStatus
		body = t.rpm
		contentLength = t.rpmContentLength
		closeError = t.rpmCloseError
	}
	if status == 0 {
		status = http.StatusOK
	}
	length := int64(len(body))
	if contentLength != nil {
		length = *contentLength
	}

	return &http.Response{
		Status:     http.StatusText(status),
		StatusCode: status,
		Body: &countingReadCloser{
			Reader: bytes.NewReader(body),
			count:  &t.closeCount,
			err:    closeError,
		},
		ContentLength: length,
		Header:        make(http.Header),
		Request:       request,
	}, nil
}

type countingReadCloser struct {
	io.Reader
	count *int
	err   error
}

func (r *countingReadCloser) Close() error {
	(*r.count)++

	return r.err
}

func TestAcquireReleaseRPMFixture(t *testing.T) {
	rpm := []byte("signed release rpm fixture bytes")
	released := testRelease(rpm)
	transport := &memoryTransport{
		metadata: mustJSON(t, released),
		rpm:      rpm,
	}
	workspace := canonicalTempDir(t)

	manifestPath, rollback, err := acquire(
		context.Background(), &http.Client{Transport: transport}, workspace)
	require.NoError(t, err)
	require.NotNil(t, rollback)
	require.Equal(t, filepath.Join(workspace, fixtureDirName, manifestName), manifestPath)

	artifactPath := filepath.Join(workspace, fixtureDirName, testRPMName)
	content, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	require.Equal(t, rpm, content)

	var manifest fixtureManifest
	content, err = os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(content, &manifest))
	require.Equal(t, fixtureManifest{
		Schema:    1,
		Kind:      "pilothouse-release-rpm-fixture",
		ReleaseID: released.ID,
		Tag:       released.Tag,
		AssetID:   released.Assets[0].ID,
		Asset:     testRPMName,
		Size:      int64(len(rpm)),
		Digest:    released.Assets[0].Digest,
		URL:       released.Assets[0].Download,
		Artifact:  testRPMName,
	}, manifest)

	require.Len(t, transport.requests, 2)
	require.Equal(t, 2, transport.closeCount)
	require.Equal(t, latestReleaseURL, transport.requests[0].URL.String())
	require.Equal(t, "application/vnd.github+json", transport.requests[0].Header.Get("Accept"))
	require.Equal(t, released.Assets[0].Download, transport.requests[1].URL.String())
	require.Equal(t, "application/octet-stream", transport.requests[1].Header.Get("Accept"))

	fixtureInfo, err := os.Stat(filepath.Join(workspace, fixtureDirName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fixtureInfo.Mode().Perm())
	for _, path := range []string{artifactPath, manifestPath} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(workspace, fixtureDirName, "*.partial"))
	require.NoError(t, err)
	require.Empty(t, matches)
	require.NoError(t, rollback())
	require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
}

func TestReleaseMetadataRejectionsLeaveNoFixture(t *testing.T) {
	rpm := []byte("rpm")

	tests := map[string]func(*release){
		"unidentified release": func(value *release) { value.ID = 0 },
		"draft release":        func(value *release) { value.Draft = true },
		"prerelease":           func(value *release) { value.Prerelease = true },
		"non-semver tag":       func(value *release) { value.Tag = "latest" },
		"major-only semver":    func(value *release) { value.Tag = "v1" },
		"major-minor semver":   func(value *release) { value.Tag = "v1.2" },
		"leading-zero semver":  func(value *release) { value.Tag = "v01.2.3" },
		"empty build metadata": func(value *release) { value.Tag = "v1.2.3+." },
		"empty build element":  func(value *release) { value.Tag = "v1.2.3+a..b" },
		"no x86_64 RPM":        func(value *release) { value.Assets[0].Name = "package.aarch64.rpm" },
		"two x86_64 RPMs": func(value *release) {
			value.Assets = append(value.Assets, value.Assets[0])
			value.Assets[1].ID++
		},
		"missing asset id":  func(value *release) { value.Assets[0].ID = 0 },
		"unsafe asset name": func(value *release) { value.Assets[0].Name = "../bad.x86_64.rpm" },
		"empty asset":       func(value *release) { value.Assets[0].Size = 0 },
		"oversized asset":   func(value *release) { value.Assets[0].Size = maxRPMBytes + 1 },
		"malformed digest":  func(value *release) { value.Assets[0].Digest = "sha256:ABC" },
		"download query":    func(value *release) { value.Assets[0].Download += "?mutable=1" },
		"tag RPM disagreement": func(value *release) {
			value.Assets[0].Name = "frostyard-pilothouse-9.9.9-1.x86_64.rpm"
			value.Assets[0].Download =
				"https://github.com/frostyard/pilothouse/releases/download/v0.6.0/" +
					value.Assets[0].Name
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			released := testRelease(rpm)
			mutate(&released)
			transport := &memoryTransport{metadata: mustJSON(t, released), rpm: rpm}
			workspace := canonicalTempDir(t)

			_, _, err := acquire(context.Background(), &http.Client{Transport: transport}, workspace)
			require.Error(t, err)
			require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
			require.Len(t, transport.requests, 1)
		})
	}
}

func TestReleaseURLValidationIsIndependent(t *testing.T) {
	rpm := []byte("rpm")
	tests := map[string]func(*release){
		"wrong host": func(value *release) {
			value.Assets[0].Download = strings.Replace(
				value.Assets[0].Download, "https://github.com/", "https://example.com/", 1)
		},
		"wrong tag path": func(value *release) {
			value.Assets[0].Download = strings.Replace(
				value.Assets[0].Download, "/download/v0.6.0/", "/download/v0.7.0/", 1)
		},
		"userinfo": func(value *release) {
			value.Assets[0].Download = strings.Replace(
				value.Assets[0].Download, "https://github.com/", "https://user:password@github.com/", 1)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			released := testRelease(rpm)
			mutate(&released)
			transport := &memoryTransport{metadata: mustJSON(t, released), rpm: rpm}
			workspace := canonicalTempDir(t)

			_, _, err := acquire(context.Background(), &http.Client{Transport: transport}, workspace)
			require.ErrorContains(t, err, "expected release asset URL")
			require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
			require.Len(t, transport.requests, 1)
		})
	}
}

func TestReleaseMetadataBodyIsBoundedAndValidJSON(t *testing.T) {
	tests := map[string][]byte{
		"oversized": make([]byte, maxMetadataBytes+1),
		"malformed": []byte(`{"id":`),
	}

	for name, metadata := range tests {
		t.Run(name, func(t *testing.T) {
			transport := &memoryTransport{metadata: metadata}
			workspace := canonicalTempDir(t)

			_, _, err := acquire(context.Background(), &http.Client{Transport: transport}, workspace)
			require.Error(t, err)
			require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
			require.Len(t, transport.requests, 1)
		})
	}
}

func TestReleaseDownloadRejectionsLeaveNoFixture(t *testing.T) {
	rpm := []byte("expected rpm")

	tests := map[string]func(*release, *memoryTransport){
		"HTTP failure": func(_ *release, transport *memoryTransport) {
			transport.rpmStatus = http.StatusNotFound
		},
		"short unknown-length body": func(_ *release, transport *memoryTransport) {
			transport.rpm = []byte("short")
			transport.rpmContentLength = int64Pointer(-1)
		},
		"long unknown-length body": func(_ *release, transport *memoryTransport) {
			transport.rpm = append(append([]byte(nil), rpm...), 'x')
			transport.rpmContentLength = int64Pointer(-1)
		},
		"digest mismatch": func(released *release, _ *memoryTransport) {
			other := sha256.Sum256([]byte("same-length!"))
			released.Assets[0].Digest = "sha256:" + hex.EncodeToString(other[:])
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			released := testRelease(rpm)
			transport := &memoryTransport{rpm: rpm}
			mutate(&released, transport)
			transport.metadata = mustJSON(t, released)
			workspace := canonicalTempDir(t)

			_, _, err := acquire(context.Background(), &http.Client{Transport: transport}, workspace)
			require.Error(t, err)
			require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
			require.Len(t, transport.requests, 2)
		})
	}
}

func TestResponseCloseFailuresLeaveNoFixture(t *testing.T) {
	rpm := []byte("rpm")
	tests := map[string]struct {
		metadataCloseError error
		rpmCloseError      error
		requests           int
	}{
		"metadata": {
			metadataCloseError: errors.New("metadata close failed"),
			requests:           1,
		},
		"RPM": {
			rpmCloseError: errors.New("RPM close failed"),
			requests:      2,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			transport := &memoryTransport{
				metadata:           mustJSON(t, testRelease(rpm)),
				rpm:                rpm,
				metadataCloseError: test.metadataCloseError,
				rpmCloseError:      test.rpmCloseError,
			}
			workspace := canonicalTempDir(t)

			_, _, err := acquire(context.Background(), &http.Client{Transport: transport}, workspace)
			require.ErrorContains(t, err, "close")
			require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
			require.Len(t, transport.requests, test.requests)
		})
	}
}

func TestAcquireRefusesExistingFixtureDirectory(t *testing.T) {
	workspace := canonicalTempDir(t)
	fixtureDir := filepath.Join(workspace, fixtureDirName)
	require.NoError(t, os.Mkdir(fixtureDir, 0o700))
	marker := filepath.Join(fixtureDir, "belongs-to-caller")
	require.NoError(t, os.WriteFile(marker, []byte("preserve"), 0o600))
	transport := &memoryTransport{}

	_, _, err := acquire(context.Background(), &http.Client{Transport: transport}, workspace)
	require.ErrorContains(t, err, "fresh fixture directory")
	require.FileExists(t, marker)
	require.Empty(t, transport.requests)
}

func TestRunRequiresCanonicalAbsoluteWorkspace(t *testing.T) {
	client := &http.Client{Transport: &memoryTransport{}}

	for _, args := range [][]string{
		nil,
		{"--workspace", "relative"},
		{"--workspace", "/tmp/../tmp"},
		{"--workspace", canonicalTempDir(t), "extra"},
	} {
		var stdout bytes.Buffer
		err := run(context.Background(), args, &stdout, client)
		require.Error(t, err)
		require.Empty(t, stdout.String())
	}
}

func TestRunRollsBackFixtureWhenStdoutFails(t *testing.T) {
	rpm := []byte("rpm")
	transport := &memoryTransport{
		metadata: mustJSON(t, testRelease(rpm)),
		rpm:      rpm,
	}
	workspace := canonicalTempDir(t)

	err := run(
		context.Background(),
		[]string{"--workspace", workspace},
		failingWriter{},
		&http.Client{Transport: transport},
	)
	require.ErrorContains(t, err, "report fixture manifest")
	require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("deliberate output failure")
}

func TestAcquireRejectsSymlinkWorkspace(t *testing.T) {
	parent := canonicalTempDir(t)
	target := filepath.Join(parent, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	link := filepath.Join(parent, "link")
	require.NoError(t, os.Symlink(target, link))
	transport := &memoryTransport{}

	_, _, err := acquire(context.Background(), &http.Client{Transport: transport}, link)
	require.ErrorContains(t, err, "real directory")
	require.Empty(t, transport.requests)
	require.NoDirExists(t, filepath.Join(target, fixtureDirName))
}

func TestAcquireCancellationLeavesNoFixture(t *testing.T) {
	workspace := canonicalTempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := acquire(ctx, &http.Client{Transport: cancelledTransport{}}, workspace)
	require.ErrorIs(t, err, context.Canceled)
	require.NoDirExists(t, filepath.Join(workspace, fixtureDirName))
}

type cancelledTransport struct{}

func (cancelledTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()

	return nil, request.Context().Err()
}

func TestCleanupRemovesOnlyKnownFixtureEntries(t *testing.T) {
	workspace := canonicalTempDir(t)
	fixtureDir := filepath.Join(workspace, fixtureDirName)
	require.NoError(t, os.Mkdir(fixtureDir, 0o700))
	known := filepath.Join(fixtureDir, "known")
	unknown := filepath.Join(fixtureDir, "caller-added")
	require.NoError(t, os.WriteFile(known, []byte("known"), 0o600))
	require.NoError(t, os.WriteFile(unknown, []byte("preserve"), 0o600))

	err := cleanupFixture(fixtureDir, []string{known})
	require.Error(t, err)
	require.NoFileExists(t, known)
	require.FileExists(t, unknown)
	require.DirExists(t, fixtureDir)
}

func TestPublishNoReplacePreservesExistingDestination(t *testing.T) {
	dir := canonicalTempDir(t)
	partial := filepath.Join(dir, "partial")
	destination := filepath.Join(dir, "destination")
	require.NoError(t, os.WriteFile(partial, []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(destination, []byte("caller"), 0o600))

	createdDestination, err := publishNoReplace(partial, destination)
	require.Error(t, err)
	require.False(t, createdDestination)
	content, readErr := os.ReadFile(destination)
	require.NoError(t, readErr)
	require.Equal(t, "caller", string(content))
	require.FileExists(t, partial)
}

func TestGitHubRedirectPolicy(t *testing.T) {
	client := githubClient("")
	require.NotNil(t, client.CheckRedirect)

	tests := map[string]bool{
		"https://github.com/frostyard/pilothouse/releases/download/v0.6.0/a.rpm":               true,
		"https://github.com:443/frostyard/pilothouse/releases/download/v0.6.0/a.rpm":           true,
		"https://objects.githubusercontent.com/release-asset/a.rpm":                            true,
		"http://github.com/frostyard/pilothouse/releases/download/v0.6.0/a.rpm":                false,
		"https://github.com:4443/frostyard/pilothouse/releases/download/v0.6.0/a.rpm":          false,
		"https://user:password@github.com/frostyard/pilothouse/releases/download/v0.6.0/a.rpm": false,
		"https://example.com/a.rpm": false,
	}

	for rawURL, allowed := range tests {
		t.Run(rawURL, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, rawURL, nil)
			require.NoError(t, err)
			err = client.CheckRedirect(request, nil)
			if allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGitHubTokenStaysOnGitHubHosts(t *testing.T) {
	recorder := &headerTransport{}
	transport := authTransport{token: "secret", next: recorder}

	for _, rawURL := range []string{
		"https://api.github.com/repos/frostyard/pilothouse/releases/latest",
		"https://api.github.com:4443/repos/frostyard/pilothouse/releases/latest",
		"https://github.com/frostyard/pilothouse/releases/download/v0.6.0/a.rpm",
		"https://user:password@github.com/frostyard/pilothouse/releases/download/v0.6.0/a.rpm",
		"https://github.com:4443/frostyard/pilothouse/releases/download/v0.6.0/a.rpm",
		"https://objects.githubusercontent.com/release-asset/a.rpm",
	} {
		request, err := http.NewRequest(http.MethodGet, rawURL, nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer inherited")
		_, err = transport.RoundTrip(request)
		require.NoError(t, err)
	}

	request, err := http.NewRequest(
		http.MethodGet, "https://objects.githubusercontent.com/release-asset/raw.rpm", nil)
	require.NoError(t, err)
	request.Header["authorization"] = []string{"Bearer inherited lowercase"}
	_, err = transport.RoundTrip(request)
	require.NoError(t, err)

	require.Equal(t, []string{"Bearer secret", "", "Bearer secret", "", "", "", ""}, recorder.authorization)
}

type headerTransport struct {
	authorization []string
}

func (t *headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var authorization []string
	for key, values := range request.Header {
		if strings.EqualFold(key, "Authorization") {
			authorization = append(authorization, values...)
		}
	}
	t.authorization = append(t.authorization, strings.Join(authorization, ","))

	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func testRelease(rpm []byte) release {
	digest := sha256.Sum256(rpm)

	return release{
		ID:  358276825,
		Tag: "v0.6.0",
		Assets: []asset{{
			ID:       486354638,
			Name:     testRPMName,
			Size:     int64(len(rpm)),
			Digest:   "sha256:" + hex.EncodeToString(digest[:]),
			Download: "https://github.com/frostyard/pilothouse/releases/download/v0.6.0/" + testRPMName,
		}},
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	content, err := json.Marshal(value)
	require.NoError(t, err)

	return content
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	canonical, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)

	return canonical
}

func int64Pointer(value int64) *int64 {
	return &value
}
