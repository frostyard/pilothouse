// Command releaserpm acquires the last released x86_64 Pilothouse RPM as an
// ephemeral image-test fixture. It publishes nothing and refuses to reuse an
// existing fixture directory.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	latestReleaseURL = "https://api.github.com/repos/frostyard/pilothouse/releases/latest"
	fixtureDirName   = "fixture-release-rpm"
	manifestName     = "fixture.json"
	maxMetadataBytes = 1 << 20
	maxRPMBytes      = 256 << 20
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type release struct {
	ID         int64   `json:"id"`
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Digest   string `json:"digest"`
	Download string `json:"browser_download_url"`
}

type fixtureManifest struct {
	Schema    int    `json:"schema"`
	Kind      string `json:"kind"`
	ReleaseID int64  `json:"release_id"`
	Tag       string `json:"tag"`
	AssetID   int64  `json:"asset_id"`
	Asset     string `json:"asset"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
	URL       string `json:"url"`
	Artifact  string `json:"artifact"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := githubClient(os.Getenv("GITHUB_TOKEN"))
	if err := run(ctx, os.Args[1:], os.Stdout, client); err != nil {
		fmt.Fprintf(os.Stderr, "releaserpm: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("releaserpm", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspace := flags.String("workspace", "", "absolute existing image-test workspace")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("usage: releaserpm --workspace ABSOLUTE_PATH: %w", err)
	}
	if flags.NArg() != 0 || *workspace == "" {
		return errors.New("usage: releaserpm --workspace ABSOLUTE_PATH")
	}

	manifest, rollback, err := acquire(ctx, client, *workspace)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, manifest); err != nil {
		return errors.Join(fmt.Errorf("report fixture manifest: %w", err), rollback())
	}

	return nil
}

func githubClient(token string) *http.Client {
	transport := http.DefaultTransport
	if token != "" {
		transport = authTransport{token: token, next: transport}
	}

	return &http.Client{
		Timeout:   2 * time.Minute,
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if !allowedGitHubEndpoint(request.URL) {
				return fmt.Errorf("refusing redirect to %s", request.URL.Redacted())
			}

			return nil
		},
	}
}

type authTransport struct {
	token string
	next  http.RoundTripper
}

func (t authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	deleteHeaderFold(clone.Header, "Authorization")
	if allowedHTTPSPort(clone.URL) {
		host := clone.URL.Hostname()
		if host == "api.github.com" || host == "github.com" {
			clone.Header.Set("Authorization", "Bearer "+t.token)
		}
	}

	return t.next.RoundTrip(clone)
}

func deleteHeaderFold(header http.Header, name string) {
	for key := range header {
		if strings.EqualFold(key, name) {
			delete(header, key)
		}
	}
}

func allowedGitHubEndpoint(endpoint *url.URL) bool {
	return allowedHTTPSPort(endpoint) &&
		allowedRedirectHost(endpoint.Hostname())
}

func allowedHTTPSPort(endpoint *url.URL) bool {
	port := endpoint.Port()

	return endpoint.Scheme == "https" &&
		endpoint.User == nil &&
		(port == "" || port == "443")
}

func allowedRedirectHost(host string) bool {
	return host == "github.com" ||
		host == "objects.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

func acquire(
	ctx context.Context,
	client *http.Client,
	workspace string,
) (manifestPath string, rollback func() error, err error) {
	root, err := canonicalWorkspace(workspace)
	if err != nil {
		return "", nil, err
	}
	fixtureDir := filepath.Join(root, fixtureDirName)
	if err := os.Mkdir(fixtureDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create fresh fixture directory %s: %w", fixtureDir, err)
	}
	var created []string
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, cleanupFixture(fixtureDir, created))
		}
	}()

	released, err := fetchRelease(ctx, client)
	if err != nil {
		return "", nil, err
	}
	selected, err := selectRPM(released)
	if err != nil {
		return "", nil, err
	}

	artifactPath := filepath.Join(fixtureDir, selected.Name)
	if err := downloadRPM(ctx, client, selected, artifactPath); err != nil {
		return "", nil, err
	}
	created = append(created, artifactPath)

	manifest := fixtureManifest{
		Schema:    1,
		Kind:      "pilothouse-release-rpm-fixture",
		ReleaseID: released.ID,
		Tag:       released.Tag,
		AssetID:   selected.ID,
		Asset:     selected.Name,
		Size:      selected.Size,
		Digest:    selected.Digest,
		URL:       selected.Download,
		Artifact:  selected.Name,
	}
	manifestPath = filepath.Join(fixtureDir, manifestName)
	if err := writeManifest(manifestPath, manifest); err != nil {
		return "", nil, err
	}
	created = append(created, manifestPath)

	complete = true
	rollback = func() error {
		return cleanupFixture(fixtureDir, created)
	}

	return manifestPath, rollback, nil
}

func cleanupFixture(fixtureDir string, created []string) error {
	var cleanupErr error
	for index := len(created) - 1; index >= 0; index-- {
		if err := os.Remove(created[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr,
				fmt.Errorf("remove fixture file %s: %w", created[index], err))
		}
	}
	if err := os.Remove(fixtureDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr,
			fmt.Errorf("remove fixture directory %s: %w", fixtureDir, err))
	}

	return cleanupErr
}

func canonicalWorkspace(workspace string) (string, error) {
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return "", fmt.Errorf("workspace must be an absolute clean path: %q", workspace)
	}
	info, err := os.Lstat(workspace)
	if err != nil {
		return "", fmt.Errorf("inspect workspace %s: %w", workspace, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("workspace must be a real directory: %s", workspace)
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace %s: %w", workspace, err)
	}
	if canonical != workspace {
		return "", fmt.Errorf("workspace must already be canonical: got %s, resolved %s", workspace, canonical)
	}

	return workspace, nil
}

func fetchRelease(ctx context.Context, client *http.Client) (_ release, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "frostyard-pilothouse-image-fixture")

	response, err := client.Do(request)
	if err != nil {
		return release{}, fmt.Errorf("fetch latest release metadata: %w", err)
	}
	bodyClosed := false
	defer func() {
		if !bodyClosed {
			err = errors.Join(err, closeResponseBody(response.Body, "latest release metadata"))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("fetch latest release metadata: HTTP %s", response.Status)
	}

	content, err := io.ReadAll(io.LimitReader(response.Body, maxMetadataBytes+1))
	closeErr := closeResponseBody(response.Body, "latest release metadata")
	bodyClosed = true
	if err != nil || closeErr != nil {
		var readErr error
		if err != nil {
			readErr = fmt.Errorf("read latest release metadata: %w", err)
		}

		return release{}, errors.Join(readErr, closeErr)
	}
	if len(content) > maxMetadataBytes {
		return release{}, fmt.Errorf("latest release metadata exceeds %d bytes", maxMetadataBytes)
	}

	var result release
	if err := json.Unmarshal(content, &result); err != nil {
		return release{}, fmt.Errorf("decode latest release metadata: %w", err)
	}
	if result.ID <= 0 || result.Draft || result.Prerelease || !stableReleaseTag(result.Tag) {
		return release{}, fmt.Errorf(
			"latest release is not an identified stable semver release: id=%d tag=%q draft=%t prerelease=%t",
			result.ID, result.Tag, result.Draft, result.Prerelease,
		)
	}

	return result, nil
}

func stableReleaseTag(tag string) bool {
	core := strings.SplitN(tag, "+", 2)[0]

	return semver.IsValid(tag) &&
		semver.Prerelease(tag) == "" &&
		strings.Count(core, ".") == 2
}

func selectRPM(released release) (asset, error) {
	var matches []asset
	for _, candidate := range released.Assets {
		if strings.HasSuffix(candidate.Name, ".x86_64.rpm") {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return asset{}, fmt.Errorf("release %s has %d x86_64 RPM assets, want exactly 1",
			released.Tag, len(matches))
	}

	selected := matches[0]
	expectedName := "frostyard-pilothouse-" +
		strings.TrimPrefix(released.Tag, "v") + "-1.x86_64.rpm"
	switch {
	case selected.ID <= 0:
		return asset{}, errors.New("x86_64 RPM asset has no positive GitHub asset id")
	case selected.Name != expectedName || filepath.Base(selected.Name) != selected.Name:
		return asset{}, fmt.Errorf("x86_64 RPM asset name %q does not match release tag %s (want %q)",
			selected.Name, released.Tag, expectedName)
	case selected.Size <= 0 || selected.Size > maxRPMBytes:
		return asset{}, fmt.Errorf("x86_64 RPM asset size %d is outside (0, %d]",
			selected.Size, maxRPMBytes)
	case !digestPattern.MatchString(selected.Digest):
		return asset{}, fmt.Errorf("x86_64 RPM asset digest %q is not a lowercase SHA-256", selected.Digest)
	}

	download, err := url.Parse(selected.Download)
	if err != nil {
		return asset{}, fmt.Errorf("parse x86_64 RPM download URL: %w", err)
	}
	expectedPath := "/frostyard/pilothouse/releases/download/" + released.Tag + "/" + selected.Name
	if download.Scheme != "https" || download.Host != "github.com" ||
		download.User != nil || download.EscapedPath() != expectedPath ||
		download.RawQuery != "" || download.Fragment != "" {
		return asset{}, fmt.Errorf("x86_64 RPM download URL is not the expected release asset URL: %s",
			download.Redacted())
	}

	return selected, nil
}

func downloadRPM(
	ctx context.Context,
	client *http.Client,
	selected asset,
	destination string,
) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, selected.Download, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "frostyard-pilothouse-image-fixture")

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download x86_64 RPM: %w", err)
	}
	bodyClosed := false
	defer func() {
		if !bodyClosed {
			err = errors.Join(err, closeResponseBody(response.Body, "x86_64 RPM response"))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download x86_64 RPM: HTTP %s", response.Status)
	}
	if response.ContentLength >= 0 && response.ContentLength != selected.Size {
		return fmt.Errorf("x86_64 RPM Content-Length %d does not match release metadata %d",
			response.ContentLength, selected.Size)
	}

	partial := destination + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create RPM partial file: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(partial)
		}
	}()

	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, selected.Size+1))
	closeErr := closeResponseBody(response.Body, "x86_64 RPM response")
	bodyClosed = true
	if copyErr != nil || closeErr != nil {
		var downloadErr error
		if copyErr != nil {
			downloadErr = fmt.Errorf("download x86_64 RPM body: %w", copyErr)
		}

		return errors.Join(downloadErr, closeErr)
	}
	if written != selected.Size {
		return fmt.Errorf("downloaded x86_64 RPM size %d does not match release metadata %d",
			written, selected.Size)
	}
	wantDigest := strings.TrimPrefix(selected.Digest, "sha256:")
	if gotDigest := hex.EncodeToString(digest.Sum(nil)); gotDigest != wantDigest {
		return fmt.Errorf("downloaded x86_64 RPM SHA-256 %s does not match release metadata %s",
			gotDigest, wantDigest)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync RPM partial file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close RPM partial file: %w", err)
	}
	createdDestination, err := publishNoReplace(partial, destination)
	if err != nil {
		if createdDestination {
			err = errors.Join(err, removeCreatedDestination(destination))
		}

		return fmt.Errorf("publish verified RPM inside fixture: %w", err)
	}
	keep = true

	return nil
}

func writeManifest(path string, manifest fixtureManifest) error {
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')

	partial := path + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create fixture manifest partial file: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(partial)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write fixture manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync fixture manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close fixture manifest: %w", err)
	}
	createdDestination, err := publishNoReplace(partial, path)
	if err != nil {
		if createdDestination {
			err = errors.Join(err, removeCreatedDestination(path))
		}

		return fmt.Errorf("publish fixture manifest: %w", err)
	}
	keep = true

	return nil
}

func publishNoReplace(partial, destination string) (createdDestination bool, err error) {
	if err := os.Link(partial, destination); err != nil {
		return false, err
	}
	if err := os.Remove(partial); err != nil {
		return true, err
	}

	return false, nil
}

func removeCreatedDestination(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove newly published fixture file %s: %w", path, err)
	}

	return nil
}

func closeResponseBody(body io.Closer, description string) error {
	if err := body.Close(); err != nil {
		return fmt.Errorf("close %s body: %w", description, err)
	}

	return nil
}
