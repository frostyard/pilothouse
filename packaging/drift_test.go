package packaging

import (
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This file is the ONE artifact-contract file that reads the working tree.
//
// That scope matters, and it is narrow: goreleaser_config_test.go,
// units_test.go and postinstall_test.go — all of which predate the artifact
// contract — read files too, and the latter two also exec systemd-analyze,
// shellcheck and /bin/sh. Nothing here may be read as a claim about this
// package's test suite as a whole.
//
// Among the artifact-contract files, the behavioral ones — model.go, finding.go,
// contract.go, verify.go, finding_test.go and verify_test.go — compare against
// bytes compiled in with //go:embed and open nothing. The two guards below
// cannot work that way. Their whole job is to catch contract.go's hand-written
// tables drifting from .goreleaser.yaml, the file those tables were
// transcribed from, and a guard that compared them against an embedded
// snapshot of the YAML would be the
// fixture-vs-fixture test docs/agents/skills/completeness-tests-need-live-source-of-truth.md
// forbids: the snapshot and the tables would drift together, undetected.
//
// So this file reads ../.goreleaser.yaml — through goreleaserConfigPath, the
// constant goreleaser_config_test.go already declares — and reads nothing else.
// It executes no external command and opens no network connection, so it runs
// under a plain `go test ./packaging/` on any machine with the repository
// checked out.
//
// Direction matters, and it is what keeps this file from being a restatement of
// goreleaser_config_test.go. That test asserts the CONFIG matches hand-written
// expectations. These guards assert VERIFY'S TABLES match the live config — the
// opposite direction, and the only thing standing between contract.go and
// silent divergence.

// usrBinDir is the directory nFPM installs a build's binary into when the nfpms
// entry sets no bindir. Guard 1 excludes requirements under it; guard 2 covers
// them.
const usrBinDir = "/usr/bin"

// underUsrBin reports whether dest is /usr/bin itself or is nested under it.
func underUsrBin(dest string) bool {
	return dest == usrBinDir || strings.HasPrefix(dest, usrBinDir+"/")
}

// TestContractTablesMatchGoreleaserConfig ties contract.go's hand-written
// tables to the live .goreleaser.yaml, in both directions:
//
//   - every dst a format's override actually packages appears in
//     requirements(format), so adding a packaged file to the config without
//     updating contract.go fails here;
//   - every requirement whose dest is not under /usr/bin is installed by
//     exactly one live entry, whose src, mode and type agree with the row;
//   - contractDependencies(format) equals the live dependency list, sorted;
//   - the postinstall scriptlet contract.go compares bytes against is the one
//     the config actually wires.
//
// WHAT THIS GUARD DOES NOT COVER, AND WHY. The two binary destinations
// /usr/bin/pilothouse and /usr/bin/pilothoused are absent from every assertion
// here. They are nFPM BUILD OUTPUTS, not override contents: goreleaser installs
// each builds[] entry's binary itself, so neither destination is written into
// overrides.<format>.contents and neither can be found in the parsed config
// this guard walks. Skipping them is therefore not a hole to be filled by
// widening this test — there is nothing here to compare them against.
// TestBinaryDestinationsMatchBuilds below is their cover: it parses the builds
// section this guard's parser discards, and pins that the two binaries land at
// exactly the two /usr/bin destinations the requirement table names.
func TestContractTablesMatchGoreleaserConfig(t *testing.T) {
	entry := loadNFPMEntry(t)

	// The source c6's scriptlet comparison reads. contract.go's
	// postinstallSource names the repository file; this pins that the config
	// still wires that same file as the package's postinstall script.
	require.Equal(t, "./packaging/"+postinstallSource, entry.Scripts.PostInstall,
		"nfpms[0].scripts.postinstall must be the source packaging/%s that Verify compares the scriptlet against", postinstallSource)

	for _, pf := range packagedFormats {
		t.Run(pf.name, func(t *testing.T) {
			override := loadOverride(t, entry, pf.name)
			contents := override.Contents
			require.NotEmptyf(t, contents, "overrides.%s.contents", pf.name)

			reqs, known := requirements(pf.format)
			require.Truef(t, known, "requirements(%q) must be known", pf.format)

			// Converse direction: a newly packaged file forces a contract
			// update. Without this, .goreleaser.yaml could grow an entry that
			// Verify never checks and nothing would notice.
			for i, live := range contents {
				require.Truef(t,
					slices.ContainsFunc(reqs, func(r requirement) bool { return r.dest == live.Dst }),
					"overrides.%s.contents[%d] installs to %s, which requirements(%q) does not require",
					pf.name, i, live.Dst, pf.format)
			}

			// Forward direction, for every requirement the config can speak to.
			asserted := 0

			for _, req := range reqs {
				if underUsrBin(req.dest) {
					continue
				}

				asserted++

				t.Run(req.dest, func(t *testing.T) {
					matched := entriesWithDst(contents, req.dest)
					require.Lenf(t, matched, 1,
						"overrides.%s.contents entries with dst %s", pf.name, req.dest)

					live := matched[0]

					// SOURCE. The comparison is conditional because the two
					// directory requirements carry no source and their config
					// entries carry no src: key — normalizeSrc("") is "",
					// which would never equal "packaging/". The type: dir half
					// keeps that branch from degenerating into "assert
					// nothing".
					if req.source == "" {
						require.Emptyf(t, live.Src,
							"%s: the contract names no source, so the config entry must carry no src", req.dest)
						require.Equalf(t, "dir", live.Type,
							"%s: an entry with no src must be a directory entry", req.dest)
					} else {
						require.Equalf(t, "packaging/"+req.source, normalizeSrc(live.Src),
							"%s: contract source", req.dest)
					}

					// MODE. file_info.Mode is an int holding the YAML octal
					// literal (0750 decodes to 488). A row's zero mode means
					// the contract states none, which must correspond to an
					// entry the config gives no file_info.
					if live.FileInfo != nil {
						require.Equalf(t, req.mode, fs.FileMode(live.FileInfo.Mode),
							"%s: contract mode %#o vs config mode %#o", req.dest, req.mode, live.FileInfo.Mode)
					} else {
						require.Equalf(t, fs.FileMode(0), req.mode,
							"%s: the config states no file_info, so the contract must pin no mode", req.dest)
					}

					// CONFIG. A total comparison in both directions: the row
					// requires the designation exactly when the config makes
					// it.
					require.Equalf(t, req.config, live.Type == "config",
						"%s: contract config designation vs config type %q", req.dest, live.Type)
				})
			}

			require.Equal(t, 8, asserted,
				"the contract must name eight destinations outside /usr/bin for the config to speak to")

			// Dependencies. Both lists are sorted before comparison because
			// neither the contract nor the config fixes an order, but the
			// comparison is on slices, so an extra, missing or duplicated
			// element still fails.
			want, known := contractDependencies(pf.format)
			require.Truef(t, known, "contractDependencies(%q) must be known", pf.format)
			slices.Sort(want)

			live := slices.Clone(override.Dependencies)
			slices.Sort(live)

			require.Equalf(t, want, live,
				"contractDependencies(%q) (sorted) must equal overrides.%s.dependencies (sorted)", pf.format, pf.name)
		})
	}
}

// The types below model only the two slices of .goreleaser.yaml guard 2 needs:
// the builds section, which goreleaser_config_test.go's parser deliberately
// discards, and the nfpms entry's bindir key. They are declared here rather
// than added to nfpmEntry so that no existing test file is modified, and their
// names are chosen not to collide with goreleaserConfig, nfpmEntry,
// contentEntry, fileInfo, formatOverride or nfpmScripts.

type buildTarget struct {
	Binary string `yaml:"binary"`
}

type bindirEntry struct {
	Bindir string `yaml:"bindir"`
}

type binaryProvenanceConfig struct {
	Builds []buildTarget `yaml:"builds"`
	NFPMs  []bindirEntry `yaml:"nfpms"`
}

// loadBinaryProvenance parses the same .goreleaser.yaml the rest of this
// package's config assertions read, through the same constant.
func loadBinaryProvenance(t *testing.T) binaryProvenanceConfig {
	t.Helper()

	raw, err := os.ReadFile(goreleaserConfigPath)
	require.NoErrorf(t, err, "read %s", goreleaserConfigPath)

	var cfg binaryProvenanceConfig
	require.NoErrorf(t, yaml.Unmarshal(raw, &cfg), "parse %s", goreleaserConfigPath)

	return cfg
}

// TestBinaryDestinationsMatchBuilds covers the two requirements guard 1 cannot
// reach, and pins the reason they are unreachable.
//
// The binaries are not packaged through overrides.<format>.contents; nFPM
// installs each builds[] entry's binary itself, into bindir, which defaults to
// /usr/bin when the nfpms entry sets none. So the chain this test asserts is
// exactly the chain that makes /usr/bin/<binary> the contract destination:
// two builds named pilothouse and pilothoused, no bindir override, therefore
// one requirement at /usr/bin/pilothouse and one at /usr/bin/pilothoused in
// each format — and nothing under /usr/bin declared in contents, which is what
// confirms the binaries are genuinely outside guard 1's reach rather than
// merely overlooked by it.
func TestBinaryDestinationsMatchBuilds(t *testing.T) {
	cfg := loadBinaryProvenance(t)

	require.Lenf(t, cfg.Builds, 2, "%s must declare exactly two builds", goreleaserConfigPath)

	binaries := make([]string, 0, len(cfg.Builds))
	for _, build := range cfg.Builds {
		require.NotEmpty(t, build.Binary, "every build must name a binary")
		binaries = append(binaries, build.Binary)
	}

	slices.Sort(binaries)
	require.Equal(t, []string{"pilothouse", "pilothoused"}, binaries, "builds[].binary")

	require.Lenf(t, cfg.NFPMs, 1, "%s must declare exactly one nfpms entry", goreleaserConfigPath)
	require.Emptyf(t, cfg.NFPMs[0].Bindir,
		"nfpms[0].bindir must stay unset so nFPM's %s default applies; the contract's binary destinations assume it", usrBinDir)

	entry := loadNFPMEntry(t)

	for _, pf := range packagedFormats {
		t.Run(pf.name, func(t *testing.T) {
			reqs, known := requirements(pf.format)
			require.Truef(t, known, "requirements(%q) must be known", pf.format)

			for _, binary := range binaries {
				dest := usrBinDir + "/" + binary

				count := 0
				for _, req := range reqs {
					if req.dest == dest {
						count++
					}
				}

				require.Equalf(t, 1, count,
					"requirements(%q) must contain exactly one requirement at %s", pf.format, dest)
			}

			for i, live := range loadOverride(t, entry, pf.name).Contents {
				require.Falsef(t, underUsrBin(live.Dst),
					"overrides.%s.contents[%d] installs to %s; the binaries are nFPM build outputs and must not be declared as contents",
					pf.name, i, live.Dst)
			}
		})
	}
}
