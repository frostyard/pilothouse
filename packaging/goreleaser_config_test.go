package packaging

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// goreleaserConfigPath is the release configuration this package's contract is
// asserted against, relative to this test package's directory.
const goreleaserConfigPath = "../.goreleaser.yaml"

// systemdManagedPaths are the two directories the broker unit's
// RuntimeDirectory=/StateDirectory= own. They must never appear in any
// packaged content entry: systemd creates and removes them with the correct
// ownership at unit start/stop, and a package-owned copy would fight it.
var systemdManagedPaths = []string{"/run/pilothouse", "/var/lib/pilothouse"}

// The types below intentionally model only the slice of .goreleaser.yaml this
// repository's packaging contract lives in. They are NOT an attempt to decode
// the file into goreleaser's own config.Project: that type cannot represent
// this repo's GoReleaser Pro `nightly` block, and validating the file against
// goreleaser's schema is explicitly out of scope. Unknown keys are ignored
// (no yaml.Decoder.KnownFields), so unrelated sections of the file — builds,
// archives, changelog, release, nightly — parse and are discarded.

type fileInfo struct {
	// Mode is written in the YAML as a leading-zero octal literal (0640),
	// which yaml.v3 resolves as octal, so 0640 decodes to 0o640 (416).
	Mode  int    `yaml:"mode"`
	Owner string `yaml:"owner"`
	Group string `yaml:"group"`
}

type contentEntry struct {
	Src      string    `yaml:"src"`
	Dst      string    `yaml:"dst"`
	Type     string    `yaml:"type"`
	FileInfo *fileInfo `yaml:"file_info"`
}

type formatOverride struct {
	Dependencies []string       `yaml:"dependencies"`
	Contents     []contentEntry `yaml:"contents"`
}

type nfpmScripts struct {
	PostInstall string `yaml:"postinstall"`
}

type nfpmEntry struct {
	Formats   []string                  `yaml:"formats"`
	Scripts   nfpmScripts               `yaml:"scripts"`
	Overrides map[string]formatOverride `yaml:"overrides"`
}

type goreleaserConfig struct {
	NFPMs []nfpmEntry `yaml:"nfpms"`
}

// loadNFPMEntry parses .goreleaser.yaml and returns nfpms[0].
func loadNFPMEntry(t *testing.T) nfpmEntry {
	t.Helper()

	raw, err := os.ReadFile(goreleaserConfigPath)
	require.NoErrorf(t, err, "read %s", goreleaserConfigPath)

	var cfg goreleaserConfig
	require.NoErrorf(t, yaml.Unmarshal(raw, &cfg), "parse %s", goreleaserConfigPath)
	require.Lenf(t, cfg.NFPMs, 1, "%s must declare exactly one nfpms entry", goreleaserConfigPath)

	return cfg.NFPMs[0]
}

// loadOverride returns the per-format override block for format.
func loadOverride(t *testing.T, entry nfpmEntry, format string) formatOverride {
	t.Helper()

	override, ok := entry.Overrides[format]
	require.Truef(t, ok, "nfpms[0].overrides.%s is missing", format)

	return override
}

// cloneContents returns a deep copy of entries so a test can mutate the parsed
// data without touching the real file or another test's view of it.
func cloneContents(entries []contentEntry) []contentEntry {
	cloned := make([]contentEntry, len(entries))

	for i, entry := range entries {
		cloned[i] = entry

		if entry.FileInfo != nil {
			info := *entry.FileInfo
			cloned[i].FileInfo = &info
		}
	}

	return cloned
}

// normalizeSrc strips the leading "./" the config writes on every src, so
// assertions can name the repository-relative path.
func normalizeSrc(src string) string {
	if src == "" {
		return ""
	}

	return strings.TrimPrefix(path.Clean(src), "./")
}

// entriesWithDst returns every content entry installing to dst.
func entriesWithDst(entries []contentEntry, dst string) []contentEntry {
	var matched []contentEntry

	for _, entry := range entries {
		if entry.Dst == dst {
			matched = append(matched, entry)
		}
	}

	return matched
}

// checkNoSrc reports an error if any entry is sourced from the given
// repository-relative path. This is the cross-format absence check: proving
// the right file is present is not the same claim as proving the other
// format's file is not also dragged in.
func checkNoSrc(entries []contentEntry, src string) error {
	for i, entry := range entries {
		if normalizeSrc(entry.Src) == src {
			return fmt.Errorf("contents[%d] (dst %s) is sourced from %s", i, entry.Dst, src)
		}
	}

	return nil
}

// checkDependencies compares a sorted copy of got against want. want is a
// hand-written list with no duplicates, so an exact sorted comparison rejects
// a missing element, an extra element, and a duplicated element alike — it is
// not a length check.
func checkDependencies(got, want []string) error {
	sorted := slices.Clone(got)
	slices.Sort(sorted)

	if !slices.Equal(sorted, want) {
		return fmt.Errorf("dependencies %v (sorted) do not equal %v", sorted, want)
	}

	return nil
}

// checkNoSystemdManagedPaths reports an error if any content entry installs to
// a systemd-managed path. The comparison is a plain string prefix on purpose:
// it rejects the directory itself (/run/pilothouse), anything nested under it
// (/var/lib/pilothouse/storage/mounts), and a near-miss sibling name.
func checkNoSystemdManagedPaths(entries []contentEntry) error {
	for i, entry := range entries {
		for _, managed := range systemdManagedPaths {
			if strings.HasPrefix(entry.Dst, managed) {
				return fmt.Errorf("contents[%d] packages %s, which is under systemd-managed %s", i, entry.Dst, managed)
			}
		}
	}

	return nil
}

// TestNFPMEntryFormatsAndScripts asserts the two package-wide settings: apk is
// gone (its PAM stack and broker unit have no correct per-format source), and
// the single shared postinstall scriptlet is wired at the nfpms entry level,
// which is the only level nFPM's non-per-format `scripts` key supports.
func TestNFPMEntryFormatsAndScripts(t *testing.T) {
	entry := loadNFPMEntry(t)

	require.Equal(t, []string{"deb", "rpm"}, entry.Formats, "nfpms[0].formats")
	require.NotContains(t, entry.Formats, "apk", "apk must not be built")
	require.Equal(t, "./packaging/postinstall.sh", entry.Scripts.PostInstall, "nfpms[0].scripts.postinstall")
}

// perFormatFiles names, for each packaged format, the PAM policy and broker
// unit that format must ship — and the other format's counterparts, which it
// must never ship. The table is written by hand from the packaging contract,
// not derived from the parsed config.
var perFormatFiles = []struct {
	format      string
	pamSrc      string
	unitSrc     string
	otherPAMSrc string
	otherUnit   string
}{
	{
		format:      "deb",
		pamSrc:      "packaging/pilothouse.pam",
		unitSrc:     "packaging/deb/pilothoused.service",
		otherPAMSrc: "packaging/rpm/pilothouse.pam",
		otherUnit:   "packaging/rpm/pilothoused.service",
	},
	{
		format:      "rpm",
		pamSrc:      "packaging/rpm/pilothouse.pam",
		unitSrc:     "packaging/rpm/pilothoused.service",
		otherPAMSrc: "packaging/pilothouse.pam",
		otherUnit:   "packaging/deb/pilothoused.service",
	},
}

// TestOverridesShipPerFormatPAMAndUnit asserts each format's override contains
// exactly one PAM entry and exactly one broker-unit entry, each sourced from
// that format's own file, and that the other format's two files appear nowhere
// in the same list.
func TestOverridesShipPerFormatPAMAndUnit(t *testing.T) {
	entry := loadNFPMEntry(t)

	for _, tc := range perFormatFiles {
		t.Run(tc.format, func(t *testing.T) {
			contents := loadOverride(t, entry, tc.format).Contents

			pam := entriesWithDst(contents, "/etc/pam.d/pilothouse")
			require.Lenf(t, pam, 1, "overrides.%s.contents entries with dst /etc/pam.d/pilothouse", tc.format)
			require.Equal(t, tc.pamSrc, normalizeSrc(pam[0].Src), "PAM policy source")

			unit := entriesWithDst(contents, "/usr/lib/systemd/system/pilothoused.service")
			require.Lenf(t, unit, 1, "overrides.%s.contents entries with dst /usr/lib/systemd/system/pilothoused.service", tc.format)
			require.Equal(t, tc.unitSrc, normalizeSrc(unit[0].Src), "broker unit source")

			// Both sources must be real files, so a typo cannot pass as a
			// correctly-named-but-absent path.
			for _, src := range []string{tc.pamSrc, tc.unitSrc} {
				_, err := os.Stat("../" + src)
				require.NoErrorf(t, err, "stat %s", src)
			}

			require.NoErrorf(t, checkNoSrc(contents, tc.otherPAMSrc),
				"overrides.%s.contents must not reference the other format's PAM policy", tc.format)
			require.NoErrorf(t, checkNoSrc(contents, tc.otherUnit),
				"overrides.%s.contents must not reference the other format's broker unit", tc.format)
		})
	}
}

// checkNoSrc must actually fire — a mutated copy that adds the other format's
// PAM policy or broker unit has to be rejected, otherwise the absence
// assertions above are only vacuously true. The mutations run against
// test-local deep copies; .goreleaser.yaml is never written.
func TestCheckNoSrcRejectsCrossFormatSource(t *testing.T) {
	entry := loadNFPMEntry(t)

	for _, tc := range perFormatFiles {
		t.Run(tc.format, func(t *testing.T) {
			for _, foreign := range []string{tc.otherPAMSrc, tc.otherUnit} {
				t.Run(foreign, func(t *testing.T) {
					contents := cloneContents(loadOverride(t, entry, tc.format).Contents)
					require.NoError(t, checkNoSrc(contents, foreign), "unmutated copy must pass")

					contents = append(contents, contentEntry{
						Src: "./" + foreign,
						Dst: "/etc/pilothouse/smuggled",
					})

					require.Error(t, checkNoSrc(contents, foreign))
				})
			}
		})
	}
}

// wantDependencies is the hand-written expected dependency set per format,
// sorted. Each list names the direct provider of the same six runtime roles:
// the linked C library, the PAM shared library pilothoused links via cgo, the
// package providing the PAM modules the policy loads, the package providing
// the PAM stacks the policy includes, the libsystemd shared library, and
// systemd itself.
var wantDependencies = map[string][]string{
	"deb": {"libc6", "libpam-modules", "libpam-runtime", "libpam0g", "libsystemd0", "systemd"},
	"rpm": {"authselect-libs", "glibc", "pam", "pam-libs", "systemd", "systemd-libs"},
}

// TestOverrideDependenciesMatchExactly asserts each format's dependency list
// has exactly the expected elements, with no extras, no omissions, and no
// duplicates.
func TestOverrideDependenciesMatchExactly(t *testing.T) {
	entry := loadNFPMEntry(t)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			want := wantDependencies[format]
			require.Len(t, want, 6, "expected six dependency roles")

			deps := loadOverride(t, entry, format).Dependencies
			require.Lenf(t, deps, 6, "overrides.%s.dependencies", format)
			require.NoError(t, checkDependencies(deps, want))
		})
	}
}

// checkDependencies must reject a duplicated or missing element, proving the
// assertion above is a real set comparison and not a length check. The
// mutations run against test-local copies; .goreleaser.yaml is untouched.
func TestCheckDependenciesRejectsMutations(t *testing.T) {
	entry := loadNFPMEntry(t)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			want := wantDependencies[format]
			deps := slices.Clone(loadOverride(t, entry, format).Dependencies)
			require.NoError(t, checkDependencies(deps, want), "unmutated copy must pass")

			duplicated := append(slices.Clone(deps), deps[0])
			require.Error(t, checkDependencies(duplicated, want), "duplicated %q must be rejected", deps[0])

			missing := slices.Clone(deps)[1:]
			require.Error(t, checkDependencies(missing, want), "missing %q must be rejected", deps[0])

			renamed := slices.Clone(deps)
			renamed[0] += "-typo"
			require.Error(t, checkDependencies(renamed, want), "renamed element must be rejected")
		})
	}
}

// wantOwnedPaths is the hand-written expectation for every path the package
// owns beyond the PAM policy, broker unit, sysusers file, and web unit: the
// two configuration directories (C5 ownership and modes) and the two
// environment files (type: config so operator edits survive upgrades).
var wantOwnedPaths = []struct {
	dst      string
	src      string
	typ      string
	mode     int
	owner    string
	group    string
	whyGroup string
}{
	{
		dst:      "/etc/pilothouse",
		typ:      "dir",
		mode:     0o750,
		owner:    "root",
		group:    "pilothouse",
		whyGroup: "group-readable so the units' EnvironmentFile= reads work",
	},
	{
		dst:      "/etc/pilothouse/storage/credentials",
		typ:      "dir",
		mode:     0o700,
		owner:    "root",
		group:    "root",
		whyGroup: "only the root broker reads remote-mount secrets",
	},
	{
		dst:      "/etc/pilothouse/pilothouse.env",
		src:      "packaging/pilothouse.env",
		typ:      "config",
		mode:     0o640,
		owner:    "root",
		group:    "pilothouse",
		whyGroup: "the web unit reads it as the pilothouse user",
	},
	{
		dst:      "/etc/pilothouse/pilothoused.env",
		src:      "packaging/pilothoused.env",
		typ:      "config",
		mode:     0o640,
		owner:    "root",
		group:    "pilothouse",
		whyGroup: "shipped alongside the web env file with matching metadata",
	},
}

// TestOverridesDeclareOwnedDirectoriesAndEnvFiles asserts both configuration
// directories and both environment files are declared, once each, with the
// same type/mode/owner/group, in both formats' contents.
func TestOverridesDeclareOwnedDirectoriesAndEnvFiles(t *testing.T) {
	entry := loadNFPMEntry(t)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			contents := loadOverride(t, entry, format).Contents

			for _, want := range wantOwnedPaths {
				t.Run(want.dst, func(t *testing.T) {
					matched := entriesWithDst(contents, want.dst)
					require.Lenf(t, matched, 1, "overrides.%s.contents entries with dst %s", format, want.dst)

					got := matched[0]
					require.Equal(t, want.typ, got.Type, "type")
					require.Equal(t, want.src, normalizeSrc(got.Src), "src")
					require.NotNilf(t, got.FileInfo, "%s needs explicit file_info", want.dst)
					require.Equalf(t, want.mode, got.FileInfo.Mode, "mode (want %#o, got %#o)", want.mode, got.FileInfo.Mode)
					require.Equal(t, want.owner, got.FileInfo.Owner, "owner")
					require.Equalf(t, want.group, got.FileInfo.Group, "group: %s", want.whyGroup)
				})
			}
		})
	}
}

// TestOverridesNeverPackageSystemdManagedPaths asserts /run/pilothouse and
// /var/lib/pilothouse — and anything under them — stay out of both formats'
// contents. They are created by the broker unit's RuntimeDirectory= and
// StateDirectory=; packaging them would duplicate that ownership.
func TestOverridesNeverPackageSystemdManagedPaths(t *testing.T) {
	entry := loadNFPMEntry(t)

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			contents := loadOverride(t, entry, format).Contents
			require.NotEmptyf(t, contents, "overrides.%s.contents", format)
			require.NoError(t, checkNoSystemdManagedPaths(contents))
		})
	}
}

// checkNoSystemdManagedPaths must fire on a mutated copy, so the assertion
// above is a real check rather than a pass that only holds because no entry
// happens to be nested there.
func TestCheckNoSystemdManagedPathsRejectsMutations(t *testing.T) {
	entry := loadNFPMEntry(t)

	mutations := []string{
		"/run/pilothouse",
		"/var/lib/pilothouse",
		"/var/lib/pilothouse/storage/mounts",
		"/run/pilothouse/broker.sock",
	}

	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			for _, dst := range mutations {
				t.Run(dst, func(t *testing.T) {
					contents := cloneContents(loadOverride(t, entry, format).Contents)
					require.NoError(t, checkNoSystemdManagedPaths(contents), "unmutated copy must pass")

					contents = append(contents, contentEntry{
						Dst:      dst,
						Type:     "dir",
						FileInfo: &fileInfo{Mode: 0o750, Owner: "root", Group: "pilothouse"},
					})

					require.Error(t, checkNoSystemdManagedPaths(contents))
				})
			}
		})
	}
}
