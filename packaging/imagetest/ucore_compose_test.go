package imagetest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testIndexDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testMemberDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testBaselineID   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testUpdateID     = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testReleaseID    = 358276825
	testAssetID      = 486354638
	testWebSHA256    = "4b5e57f6eb2f42b9039b3d1e13929295f231749c510cbe341cd68036d9af97e2"
	testDaemonSHA256 = "f77b12a53ece5f6b7050800bbdbf8cc5ebe87f1b1387cf739f243e43e2ce886b"
)

func TestUCoreComposeResolvesVerifiesAndBuildsTwoLocalImages(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	compose := imageComposeScript(t)

	result := imageRunChild(t, sandbox, imageRequireTool(t, "env"),
		"CONTAINER_HOST=ssh://outside.invalid/run/podman.sock",
		"CONTAINER_CONNECTION=outside",
		"CONTAINER_SSHKEY=/outside/key",
		"CONTAINERS_CONF=/outside/containers.conf",
		"CONTAINERS_CONF_OVERRIDE=/outside/override.conf",
		"CONTAINERS_STORAGE_CONF=/outside/storage.conf",
		"STORAGE_DRIVER=outside",
		"STORAGE_OPTS=imagestore=/outside/images",
		compose,
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.NoError(t, result.Err, result.Stderr)
	require.False(t, result.TimedOut)
	require.Equal(t, 0, result.ExitCode)

	manifestPath := filepath.Join(sandbox.cwd, "fixture-ucore-images", "fixture.json")
	require.Equal(t, manifestPath+"\n", result.Stdout)
	require.FileExists(t, manifestPath)
	require.NoFileExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images", "index.json"))

	content, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	expectedManifest := `{
		"schema": 1,
		"kind": "pilothouse-ucore-image-fixture",
		"producer_uid": ` + fmt.Sprint(os.Geteuid()) + `,
		"release": {
			"id": 358276825,
			"asset_id": 486354638,
			"tag": "v0.6.0",
			"artifact": "frostyard-pilothouse-0.6.0-1.x86_64.rpm",
			"pam_compatibility": "v0.6.0-debian-pam"
		},
		"executables": {
			"source": "checked-out-head",
			"pilothouse_sha256": "sha256:` + testWebSHA256 + `",
			"pilothoused_sha256": "sha256:` + testDaemonSHA256 + `"
		},
		"source": "ghcr.io/ublue-os/ucore:latest",
		"source_index_digest": "` + testIndexDigest + `",
		"source_linux_amd64_digest": "` + testMemberDigest + `",
		"baseline": {
			"ref": "localhost/pilothouse-image-test-run-123:baseline",
			"id": "` + testBaselineID + `",
			"slot": "baseline"
		},
		"update": {
			"ref": "localhost/pilothouse-image-test-run-123:update",
			"id": "` + testUpdateID + `",
			"slot": "update"
		},
		"storage": {
			"root": "` + filepath.Join(sandbox.cwd, "fixture-ucore-images", "storage") + `",
			"imagestore": "` + filepath.Join(sandbox.cwd, "fixture-ucore-images", "imagestore") + `",
			"runroot": "` + filepath.Join(sandbox.cwd, "fixture-ucore-images", "runroot") + `",
			"podman_tmpdir": "` + filepath.Join(sandbox.cwd, "fixture-ucore-images", "libpod-tmp") + `",
			"image_tmpdir": "` + filepath.Join(sandbox.cwd, "fixture-ucore-images", "image-tmp") + `",
			"config": "` + filepath.Join(sandbox.cwd, "fixture-ucore-images", "storage.conf") + `"
		}
	}`
	require.JSONEq(t, expectedManifest, string(content))

	storageConfigPath := filepath.Join(sandbox.cwd, "fixture-ucore-images", "storage.conf")
	storageConfig, err := os.ReadFile(storageConfigPath)
	require.NoError(t, err)
	require.Equal(t, `[storage]
driver = "overlay"
graphroot = "`+filepath.Join(sandbox.cwd, "fixture-ucore-images", "storage")+`"
imagestore = "`+filepath.Join(sandbox.cwd, "fixture-ucore-images", "imagestore")+`"
runroot = "`+filepath.Join(sandbox.cwd, "fixture-ucore-images", "runroot")+`"
transient_store = false
`, string(storageConfig))
	info, err := os.Stat(storageConfigPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	buildContext := filepath.Join(sandbox.cwd, "fixture-ucore-images", "build-context")
	rpmCopy, err := os.ReadFile(filepath.Join(
		buildContext,
		"frostyard-pilothouse-0.6.0-1.x86_64.rpm",
	))
	require.NoError(t, err)
	require.Equal(t, []byte("rpm"), rpmCopy)
	pamCopy, err := os.ReadFile(filepath.Join(buildContext, "pilothouse-image-test.pam"))
	require.NoError(t, err)
	reviewedPAM, err := os.ReadFile(filepath.Join(imageRepositoryRoot(t), "packaging/rpm/pilothouse.pam"))
	require.NoError(t, err)
	require.Equal(t, reviewedPAM, pamCopy)
	webCopy, err := os.ReadFile(filepath.Join(buildContext, "pilothouse"))
	require.NoError(t, err)
	require.Equal(t, []byte("web"), webCopy)
	daemonCopy, err := os.ReadFile(filepath.Join(buildContext, "pilothoused"))
	require.NoError(t, err)
	require.Equal(t, []byte("daemon"), daemonCopy)

	logContent, err := os.ReadFile(filepath.Join(sandbox.cwd, "tool.log"))
	require.NoError(t, err)
	log := string(logContent)
	imageRequireOrdered(t, log,
		"skopeo inspect --format {{.Digest}} docker://ghcr.io/ublue-os/ucore:latest",
		"cosign verify --key",
		"ghcr.io/ublue-os/ucore@"+testIndexDigest,
		"skopeo inspect --raw docker://ghcr.io/ublue-os/ucore@"+testIndexDigest,
		"ghcr.io/ublue-os/ucore@"+testMemberDigest,
		"podman --remote=false --root",
		"pull ghcr.io/ublue-os/ucore@"+testMemberDigest,
		"IMAGE_TEST_SLOT=baseline",
		"IMAGE_TEST_SLOT=update",
		"image inspect --format {{.Id}} localhost/pilothouse-image-test-run-123:baseline",
		"image inspect --format {{.Id}} localhost/pilothouse-image-test-run-123:update",
	)
	require.NotContains(t, log, "push")
	require.NotContains(t, log, ":latest build")
}

func TestUCoreComposeIndexSignatureFailureStopsBeforeRawIndex(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	require.NoError(t, os.WriteFile(filepath.Join(sandbox.cwd, "fail-cosign-1"), nil, 0o600))

	result := imageRunChild(t, sandbox, imageComposeScript(t),
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.NotEqual(t, 0, result.ExitCode)
	require.DirExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images"))
	require.NoFileExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images", "fixture.json"))

	logContent, err := os.ReadFile(filepath.Join(sandbox.cwd, "tool.log"))
	require.NoError(t, err)
	require.Contains(t, string(logContent), "cosign verify")
	require.NotContains(t, string(logContent), "skopeo inspect --raw")
	require.NotContains(t, string(logContent), "podman")
}

func TestUCoreComposeDoesNotApplyLegacyPAMCompatibilityToAnyMismatchedIdentity(t *testing.T) {
	const legacyArtifact = "frostyard-pilothouse-0.6.0-1.x86_64.rpm"
	tests := []struct {
		name             string
		old              string
		replacement      string
		expectedID       int
		expectedAssetID  int
		expectedTag      string
		expectedArtifact string
		renameArtifact   bool
	}{
		{
			name: "release ID", old: `"release_id":358276825`,
			replacement: `"release_id":358276826`, expectedID: testReleaseID + 1,
			expectedAssetID: testAssetID, expectedTag: "v0.6.0", expectedArtifact: legacyArtifact,
		},
		{
			name: "asset ID", old: `"asset_id":486354638`,
			replacement: `"asset_id":486354639`, expectedID: testReleaseID,
			expectedAssetID: testAssetID + 1, expectedTag: "v0.6.0", expectedArtifact: legacyArtifact,
		},
		{
			name: "tag", old: `"tag":"v0.6.0"`,
			replacement: `"tag":"v0.6.1"`, expectedID: testReleaseID,
			expectedAssetID: testAssetID, expectedTag: "v0.6.1", expectedArtifact: legacyArtifact,
		},
		{
			name: "artifact", old: legacyArtifact,
			replacement: "frostyard-pilothouse-0.6.1-1.x86_64.rpm", expectedID: testReleaseID,
			expectedAssetID: testAssetID, expectedTag: "v0.6.0",
			expectedArtifact: "frostyard-pilothouse-0.6.1-1.x86_64.rpm", renameArtifact: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sandbox := newImageSandbox(t)
			imageWriteRPMFixture(t, sandbox.cwd)
			fixtureDir := filepath.Join(sandbox.cwd, "fixture-release-rpm")
			manifestPath := filepath.Join(fixtureDir, "fixture.json")
			manifest, err := os.ReadFile(manifestPath)
			require.NoError(t, err)
			updated := strings.Replace(string(manifest), test.old, test.replacement, 1)
			require.NotEqual(t, string(manifest), updated)
			require.NoError(t, os.WriteFile(manifestPath, []byte(updated), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(sandbox.cwd, "future-release"), nil, 0o600))
			if test.renameArtifact {
				require.NoError(t, os.Rename(
					filepath.Join(fixtureDir, legacyArtifact),
					filepath.Join(fixtureDir, test.expectedArtifact),
				))
				require.NoError(t, os.WriteFile(
					filepath.Join(sandbox.cwd, "future-artifact"),
					nil,
					0o600,
				))
			}
			imageWriteComposeTools(t, sandbox)

			result := imageRunChild(t, sandbox, imageComposeScript(t),
				"--workspace", sandbox.cwd,
				"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
				"--run-id", "run-123",
			)
			require.NoError(t, result.Err, result.Stderr)
			require.False(t, result.TimedOut)

			fixture, err := os.ReadFile(filepath.Join(
				sandbox.cwd,
				"fixture-ucore-images",
				"fixture.json",
			))
			require.NoError(t, err)
			require.Contains(t, string(fixture), fmt.Sprintf(`"release": {
    "id": %d,
    "asset_id": %d,
    "tag": %q,
    "artifact": %q,
    "pam_compatibility": "none"
  }`, test.expectedID, test.expectedAssetID, test.expectedTag, test.expectedArtifact))
		})
	}
}

func TestUCoreComposeMemberSignatureFailureStopsBeforePodman(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	require.NoError(t, os.WriteFile(filepath.Join(sandbox.cwd, "fail-cosign-2"), nil, 0o600))

	result := imageRunChild(t, sandbox, imageComposeScript(t),
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.NotEqual(t, 0, result.ExitCode)
	require.DirExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images"))
	require.NoFileExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images", "fixture.json"))

	logContent, err := os.ReadFile(filepath.Join(sandbox.cwd, "tool.log"))
	require.NoError(t, err)
	log := string(logContent)
	require.Equal(t, 2, strings.Count(log, "cosign verify"))
	require.Contains(t, log, "ghcr.io/ublue-os/ucore@"+testMemberDigest)
	require.NotContains(t, log, "podman")
}

func TestUCoreComposeBoundsOversizedRawIndexBeforeDisk(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	oversized := strings.Repeat("x", (4<<20)+4096)
	require.NoError(t, os.WriteFile(
		filepath.Join(sandbox.cwd, "oversized-index"),
		[]byte(oversized),
		0o600,
	))

	result := imageRunChild(t, sandbox, imageComposeScript(t),
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.Contains(t, result.Stderr, "uCore index exceeds 4194304 bytes")

	rawIndex := filepath.Join(sandbox.cwd, "fixture-ucore-images", "index.json")
	info, err := os.Stat(rawIndex)
	require.NoError(t, err)
	require.EqualValues(t, (4<<20)+1, info.Size())

	logContent, err := os.ReadFile(filepath.Join(sandbox.cwd, "tool.log"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(logContent), "cosign verify"))
	require.NotContains(t, string(logContent), "podman")
}

func TestUCoreComposeRejectsAmbiguousLinuxAMD64Member(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	require.NoError(t, os.WriteFile(filepath.Join(sandbox.cwd, "ambiguous-index"), nil, 0o600))

	result := imageRunChild(t, sandbox, imageComposeScript(t),
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.Contains(t, result.Stderr, "want exactly one linux/amd64 uCore member")
	require.DirExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images"))
	require.NoFileExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images", "fixture.json"))

	logContent, err := os.ReadFile(filepath.Join(sandbox.cwd, "tool.log"))
	require.NoError(t, err)
	require.NotContains(t, string(logContent), "podman")
}

func TestUCoreComposeRejectsRPMMutationBeforeImageOperations(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	artifact := filepath.Join(
		sandbox.cwd,
		"fixture-release-rpm",
		"frostyard-pilothouse-0.6.0-1.x86_64.rpm",
	)
	require.NoError(t, os.WriteFile(artifact, []byte("tampered"), 0o600))

	result := imageRunChild(t, sandbox, imageComposeScript(t),
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.Contains(t, result.Stderr, "no longer matches its manifest")
	require.NoDirExists(t, filepath.Join(sandbox.cwd, "fixture-ucore-images"))
	require.NoFileExists(t, filepath.Join(sandbox.cwd, "tool.log"))
}

func TestUCoreComposePreservesExistingOutputDirectory(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	output := filepath.Join(sandbox.cwd, "fixture-ucore-images")
	require.NoError(t, os.Mkdir(output, 0o700))
	marker := filepath.Join(output, "caller-marker")
	require.NoError(t, os.WriteFile(marker, []byte("preserve"), 0o600))

	result := imageRunChild(t, sandbox, imageComposeScript(t),
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.Contains(t, result.Stderr, "create fresh uCore fixture directory")
	require.FileExists(t, marker)
	require.NoFileExists(t, filepath.Join(sandbox.cwd, "tool.log"))
}

func TestUCoreComposeRetainsPartialLocalStoreOnBuildFailure(t *testing.T) {
	sandbox := newImageSandbox(t)
	imageWriteRPMFixture(t, sandbox.cwd)
	imageWriteComposeTools(t, sandbox)
	require.NoError(t, os.WriteFile(filepath.Join(sandbox.cwd, "fail-update-build"), nil, 0o600))

	result := imageRunChild(t, sandbox, imageComposeScript(t),
		"--workspace", sandbox.cwd,
		"--bin-dir", filepath.Join(sandbox.cwd, "fixture-branch-executables"),
		"--run-id", "run-123",
	)
	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.NotEqual(t, 0, result.ExitCode)

	output := filepath.Join(sandbox.cwd, "fixture-ucore-images")
	require.FileExists(t, filepath.Join(output, "storage", "partial-update"))
	require.NoFileExists(t, filepath.Join(output, "fixture.json"))
}

func TestUCoreContainerfileUsesOfflineReleasedRPMAndBootcLint(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(imageRepositoryRoot(t), "test/image/ucore/Containerfile"))
	require.NoError(t, err)
	instructions := imageContainerfileInstructions(string(content))

	require.Equal(t, []string{
		"ARG UCORE_IMAGE",
		"FROM ${UCORE_IMAGE}",
		"ARG PILOTHOUSE_RPM",
		"ARG IMAGE_TEST_SLOT",
		"ARG PILOTHOUSE_PAM_COMPAT",
		"ARG PILOTHOUSE_SHA256",
		"ARG PILOTHOUSED_SHA256",
		"COPY ${PILOTHOUSE_RPM} /tmp/pilothouse-image-test.rpm",
		"RUN dnf -y --disable-repo='*' install /tmp/pilothouse-image-test.rpm " +
			"&& rm -f /tmp/pilothouse-image-test.rpm && dnf clean all " +
			"&& rm -rf /run/dnf /var/cache/libdnf5 " +
			"&& rm -f /var/cache/ldconfig/aux-cache /var/lib/dnf/system-repo.lock /var/log/dnf5.log",
		"COPY pilothouse-image-test.pam /tmp/pilothouse-image-test.pam",
		`RUN case "${PILOTHOUSE_PAM_COMPAT}" in v0.6.0-debian-pam) ` +
			`printf '%s  %s\n' ` +
			`'0e8ab613d8bb5d197ce6ce92d0e67098e70ae0de60eea5678cac8c20e8227992' ` +
			`/etc/pam.d/pilothouse | sha256sum -c - ` +
			`&& install -o root -g root -m 0644 /tmp/pilothouse-image-test.pam /etc/pam.d/pilothouse ` +
			`;; none) ;; *) exit 64 ;; esac && rm -f /tmp/pilothouse-image-test.pam`,
		"COPY --chmod=0755 pilothouse /usr/bin/pilothouse",
		"COPY --chmod=0755 pilothoused /usr/bin/pilothoused",
		`RUN printf '%s  %s\n' "${PILOTHOUSE_SHA256}" /usr/bin/pilothouse ` +
			`"${PILOTHOUSED_SHA256}" /usr/bin/pilothoused | sha256sum -c -`,
		"RUN systemctl enable pilothoused.service pilothouse.service",
		`RUN install -d -m 0755 /usr/lib/pilothouse-image-test && printf '%s\n' "${IMAGE_TEST_SLOT}" > /usr/lib/pilothouse-image-test/slot`,
		"RUN bootc container lint",
		`LABEL org.opencontainers.image.title="Pilothouse image-tier test fixture"`,
		`LABEL org.opencontainers.image.description="Ephemeral issue-80 fixture; never publish"`,
		`LABEL io.frostyard.pilothouse.image-test="true"`,
		`LABEL io.frostyard.pilothouse.image-test.slot="${IMAGE_TEST_SLOT}"`,
	}, instructions)
}

func TestUCoreCosignKeyMatchesReviewedDigest(t *testing.T) {
	root := imageRepositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "test/image/ucore/cosign.pub"))
	require.NoError(t, err)
	digest := sha256.Sum256(content)
	const expected = "af78ecfda6eb21c35195af3739341715e9cfc3f2f5911dd9c10b0670547bf6e8"
	require.Equal(t, expected, fmt.Sprintf("%x", digest))

	documentation, err := os.ReadFile(filepath.Join(root, "test/image/ucore/README.md"))
	require.NoError(t, err)
	require.Contains(t, string(documentation), expected)
	require.Contains(t, string(documentation), "724b05abfcdb1ab4633cd3880d26b28a8dad3e64")
}

func TestUCorePAMCompatibilityPolicyMatchesReviewedDigest(t *testing.T) {
	root := imageRepositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "packaging/rpm/pilothouse.pam"))
	require.NoError(t, err)
	digest := sha256.Sum256(content)
	const expected = "af72dc5708248288d056e3ef7d8188d6c24b6991f1f2b50e4805e2108f505993"
	require.Equal(t, expected, fmt.Sprintf("%x", digest))

	documentation, err := os.ReadFile(filepath.Join(root, "test/image/ucore/README.md"))
	require.NoError(t, err)
	require.Contains(t, string(documentation), expected)
}

func imageWriteRPMFixture(t *testing.T, workspace string) {
	t.Helper()
	dir := filepath.Join(workspace, "fixture-release-rpm")
	require.NoError(t, os.Mkdir(dir, 0o700))
	binDir := filepath.Join(workspace, "fixture-branch-executables")
	require.NoError(t, os.Mkdir(binDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "pilothouse"), []byte("web"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "pilothoused"), []byte("daemon"), 0o700))
	artifact := "frostyard-pilothouse-0.6.0-1.x86_64.rpm"
	require.NoError(t, os.WriteFile(filepath.Join(dir, artifact), []byte("rpm"), 0o600))
	manifest := `{"schema":1,"kind":"pilothouse-release-rpm-fixture",` +
		`"release_id":` + fmt.Sprint(testReleaseID) + `,` +
		`"asset_id":` + fmt.Sprint(testAssetID) + `,` +
		`"tag":"v0.6.0","artifact":"` +
		artifact + `","size":3,"digest":"sha256:` +
		`9e7ab438597fee20e16e8e441bed0ce966bd59e0fb993fa7c94be31fb1384d88"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture.json"), []byte(manifest), 0o600))
}

func imageWriteComposeTools(t *testing.T, sandbox imageSandbox) {
	t.Helper()
	imageWriteExecutable(t, filepath.Join(sandbox.bin, "skopeo"), `#!/bin/sh
printf 'skopeo %s\n' "$*" >> "$PWD/tool.log"
if [ "$#" -eq 4 ] &&
   [ "$1" = "inspect" ] &&
   [ "$2" = "--format" ] &&
   [ "$3" = "{{.Digest}}" ] &&
   [ "$4" = "docker://ghcr.io/ublue-os/ucore:latest" ]; then
  printf '%s\n' '`+testIndexDigest+`'
elif [ "$#" -eq 3 ] &&
     [ "$1" = "inspect" ] &&
     [ "$2" = "--raw" ] &&
     [ "$3" = "docker://ghcr.io/ublue-os/ucore@`+testIndexDigest+`" ]; then
    if [ -f "$PWD/oversized-index" ]; then
      cat "$PWD/oversized-index"
    elif [ -f "$PWD/ambiguous-index" ]; then
      printf '%s\n' '{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+testMemberDigest+`","platform":{"os":"linux","architecture":"amd64"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","platform":{"os":"linux","architecture":"amd64"}}]}'
    else
      printf '%s\n' '{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+testMemberDigest+`","platform":{"os":"linux","architecture":"amd64"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","platform":{"os":"linux","architecture":"arm64"}}]}'
    fi
else
  exit 64
fi
`)
	imageWriteExecutable(t, filepath.Join(sandbox.bin, "cosign"), `#!/bin/sh
printf 'cosign %s\n' "$*" >> "$PWD/tool.log"
[ "$#" -eq 4 ] || exit 64
[ "$1" = "verify" ] || exit 64
[ "$2" = "--key" ] || exit 64
case "$3" in
  */test/image/ucore/cosign.pub) ;;
  *) exit 64 ;;
esac
[ -f "$3" ] || exit 64
count=0
if [ -f "$PWD/cosign-count" ]; then
  read -r count < "$PWD/cosign-count"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$PWD/cosign-count"
case "$count:$4" in
  "1:ghcr.io/ublue-os/ucore@`+testIndexDigest+`") ;;
  "2:ghcr.io/ublue-os/ucore@`+testMemberDigest+`") ;;
  *) exit 64 ;;
esac
[ ! -f "$PWD/fail-cosign-$count" ]
`)
	imageWriteExecutable(t, filepath.Join(sandbox.bin, "podman"), `#!/bin/sh
printf 'podman %s\n' "$*" >> "$PWD/tool.log"
output="$PWD/fixture-ucore-images"
root="$output/storage"
imagestore="$output/imagestore"
runroot="$output/runroot"
podman_tmpdir="$output/libpod-tmp"
image_tmpdir="$output/image-tmp"
storage_conf="$output/storage.conf"
[ "${TMPDIR-}" = "$image_tmpdir" ] || exit 65
[ -z "${CONTAINER_HOST+x}" ] || exit 65
[ -z "${CONTAINER_CONNECTION+x}" ] || exit 65
[ -z "${CONTAINER_SSHKEY+x}" ] || exit 65
[ "${CONTAINERS_CONF-}" = "/dev/null" ] || exit 65
[ -z "${CONTAINERS_CONF_OVERRIDE+x}" ] || exit 65
[ "${CONTAINERS_STORAGE_CONF-}" = "$storage_conf" ] || exit 65
[ -f "$storage_conf" ] || exit 65
[ -z "${STORAGE_DRIVER+x}" ] || exit 65
[ -z "${STORAGE_OPTS+x}" ] || exit 65
[ "$1" = "--remote=false" ] || exit 65
shift
[ "$1" = "--root" ] && [ "$2" = "$root" ] || exit 65
shift 2
[ "$1" = "--imagestore" ] && [ "$2" = "$imagestore" ] || exit 65
shift 2
[ "$1" = "--runroot" ] && [ "$2" = "$runroot" ] || exit 65
shift 2
[ "$1" = "--tmpdir" ] && [ "$2" = "$podman_tmpdir" ] || exit 65
shift 2
[ "$1" = "--events-backend" ] && [ "$2" = "none" ] || exit 65
shift 2
[ "$1" = "--storage-driver" ] && [ "$2" = "overlay" ] || exit 65
shift 2
mkdir -p "$root" "$imagestore" "$runroot" "$podman_tmpdir" "$image_tmpdir"

base_ref="ghcr.io/ublue-os/ucore@`+testMemberDigest+`"
prefix="localhost/pilothouse-image-test-run-123"
case "${1-}" in
  pull)
    [ "$#" -eq 2 ] && [ "$2" = "$base_ref" ] || exit 65
    ;;
  build)
    [ "$#" -eq 20 ] || exit 65
    [ "$2" = "--pull=never" ] || exit 65
    [ "$3" = "--network=none" ] || exit 65
    [ "$4" = "--file" ] || exit 65
    case "$5" in
      */test/image/ucore/Containerfile) ;;
      *) exit 65 ;;
    esac
    [ "$6" = "--tag" ] || exit 65
    image_ref="$7"
    [ "$8" = "--build-arg" ] && [ "$9" = "UCORE_IMAGE=$base_ref" ] || exit 65
    artifact="frostyard-pilothouse-0.6.0-1.x86_64.rpm"
    [ ! -f "$PWD/future-artifact" ] ||
      artifact="frostyard-pilothouse-0.6.1-1.x86_64.rpm"
    [ "${10}" = "--build-arg" ] &&
      [ "${11}" = "PILOTHOUSE_RPM=$artifact" ] || exit 65
    [ "${12}" = "--build-arg" ] || exit 65
    slot_arg="${13}"
    pam_compatibility="v0.6.0-debian-pam"
    [ ! -f "$PWD/future-release" ] || pam_compatibility="none"
    [ "${14}" = "--build-arg" ] &&
      [ "${15}" = "PILOTHOUSE_PAM_COMPAT=$pam_compatibility" ] || exit 65
    [ "${16}" = "--build-arg" ] &&
      [ "${17}" = "PILOTHOUSE_SHA256=`+testWebSHA256+`" ] || exit 65
    [ "${18}" = "--build-arg" ] &&
      [ "${19}" = "PILOTHOUSED_SHA256=`+testDaemonSHA256+`" ] || exit 65
    [ "${20}" = "$output/build-context" ] || exit 65
    [ "$(cat "${20}/$artifact")" = "rpm" ] || exit 65
    [ "$(sha256sum "${20}/pilothouse-image-test.pam" | awk '{print $1}')" = "af72dc5708248288d056e3ef7d8188d6c24b6991f1f2b50e4805e2108f505993" ] || exit 65
    [ "$(sha256sum "${20}/pilothouse" | awk '{print $1}')" = "`+testWebSHA256+`" ] || exit 65
    [ "$(sha256sum "${20}/pilothoused" | awk '{print $1}')" = "`+testDaemonSHA256+`" ] || exit 65
    case "$image_ref:$slot_arg" in
      "$prefix:baseline:IMAGE_TEST_SLOT=baseline") ;;
      "$prefix:update:IMAGE_TEST_SLOT=update")
        printf 'partial\n' > "$root/partial-update"
        [ ! -f "$PWD/fail-update-build" ] || exit 66
        ;;
      *) exit 65 ;;
    esac
    ;;
  image)
    [ "$#" -eq 5 ] || exit 65
    [ "$2" = "inspect" ] && [ "$3" = "--format" ] && [ "$4" = "{{.Id}}" ] || exit 65
    case "$5" in
      "$prefix:baseline") printf '%s\n' '`+strings.TrimPrefix(testBaselineID, "sha256:")+`' ;;
      "$prefix:update") printf '%s\n' '`+strings.TrimPrefix(testUpdateID, "sha256:")+`' ;;
      *) exit 65 ;;
    esac
    ;;
  *) exit 65 ;;
esac
`)
}

func imageWriteExecutable(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o700))
}

func imageComposeScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(imageRepositoryRoot(t), "test/image/compose-ucore.sh")
}

func imageRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

func imageRequireOrdered(t *testing.T, content string, fragments ...string) {
	t.Helper()
	offset := 0
	for _, fragment := range fragments {
		index := strings.Index(content[offset:], fragment)
		require.NotEqualf(t, -1, index, "missing ordered fragment %q in:\n%s", fragment, content)
		offset += index + len(fragment)
	}
}

func imageContainerfileInstructions(source string) []string {
	var instructions []string
	current := ""

	for _, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		continued := strings.HasSuffix(line, "\\")
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		if current != "" {
			current += " "
		}
		current += line
		if !continued {
			instructions = append(instructions, current)
			current = ""
		}
	}
	if current != "" {
		instructions = append(instructions, current)
	}

	return instructions
}
