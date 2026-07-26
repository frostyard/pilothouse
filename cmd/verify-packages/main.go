// Command verify-packages reports the artifact-contract findings for built
// .deb and .rpm packages.
//
// It is a repository tool, not a shipped binary: it is deliberately absent
// from `make build` and from .goreleaser.yaml's builds, so bin/ keeps only
// pilothouse and pilothoused and no development tool can leak into a package.
// Its non-product name signals that; its placement under cmd/ follows the
// convention that each binary is a directory there.
//
// It holds no contract logic. Its whole job is to find artifacts, hand each to
// the extractor for its format, pass the resulting packaging.Model to the
// parent package's Verify, print what comes back, and exit non-zero if anything
// did. It invents no finding code, never compares a Finding.Message, and refers
// to that function exactly once — in defaultDeps. Every verdict it prints for a
// real artifact is that function's.
//
// Usage:
//
//	verify-packages [-dir <directory>] [artifact ...]
//
// With no positional arguments it discovers artifacts by globbing the -dir
// directory (default dist) for each backend's extension in backend order, deb
// first, each glob's matches sorted. With positional arguments those are the
// artifact paths, in the order given, and -dir is not consulted.
//
// Exit status is 0 only when every artifact was extracted and verified with no
// findings; any finding or any extraction failure makes it 1. An extraction
// failure does not stop the run: the remaining artifacts are still processed.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/frostyard/pilothouse/packaging"
	"github.com/frostyard/pilothouse/packaging/extract"
)

// toolName is the program name used in usage and in every line this command
// emits on its own behalf, as opposed to a line about one artifact.
const toolName = "verify-packages"

// defaultDir is the directory discovery globs when -dir is not given. It is
// where GoReleaser writes its output.
const defaultDir = "dist"

// emptyPathPlaceholder is what a finding's Path renders as when it is empty.
// A dependency mismatch and a missing scriptlet are not path-scoped, so the
// column would otherwise be blank and the line would be ambiguous.
const emptyPathPlaceholder = "-"

// backend is one artifact format this command knows how to read: the file
// extension that selects it, the label printed for it, and the extractor that
// turns a path into a model.
type backend struct {
	ext     string
	label   string
	extract func(context.Context, string) (packaging.Model, error)
}

// deps is the seam between run and the two things it cannot produce on a host
// with no conforming package: an extraction result and a verification result.
//
// backends is a slice rather than a map on purpose. Discovery globs in backend
// order, so the order artifacts are reported in is a property of this list; a
// map would make it unspecified.
//
// Production wiring never builds one of these by hand: main passes
// defaultDeps(), and only the exit-semantics test injects a substitute, for the
// outcomes a real artifact cannot demonstrate here.
type deps struct {
	backends []backend
	verify   func(packaging.Model) []packaging.Finding
}

// defaultDeps returns the production wiring: the real extractors, in the order
// discovery reports them, and the real contract check.
func defaultDeps() deps {
	return deps{
		backends: []backend{
			{ext: ".deb", label: string(packaging.FormatDeb), extract: extract.Deb},
			{ext: ".rpm", label: string(packaging.FormatRPM), extract: extract.RPM},
		},
		verify: packaging.Verify,
	}
}

func main() {
	os.Exit(run(context.Background(), defaultDeps(), os.Args[1:], os.Stdout, os.Stderr))
}

// run is the composing entry point: it parses arguments, discovers or accepts
// artifact paths, dispatches each to its backend, verifies, reports, and
// returns the process exit status.
//
// The report — per-artifact headers, finding lines, extraction-failure lines
// and the closing summary — goes to stdout. stderr carries only the reasons
// the command could not produce a report at all: a bad flag, an unreadable
// directory, or no artifacts to check.
func run(ctx context.Context, d deps, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(toolName, flag.ContinueOnError)
	flags.SetOutput(stderr)

	dir := flags.String("dir", defaultDir, "directory to search for artifacts when no paths are given")

	if err := flags.Parse(args); err != nil {
		return 1
	}

	paths := flags.Args()
	if len(paths) == 0 {
		discovered, err := discover(*dir, d.backends)
		if err != nil {
			printf(stderr, "%s: %v\n", toolName, err)

			return 1
		}

		if len(discovered) == 0 {
			printNoArtifacts(stderr, *dir, d.backends)

			return 1
		}

		paths = discovered
	}

	findings, failures := 0, 0

	for _, path := range paths {
		found, failed := report(ctx, d, path, stdout)
		findings += found

		if failed {
			failures++
		}
	}

	printf(stdout, "%s: %s checked, %s, %s\n",
		toolName,
		plural(len(paths), "artifact"),
		plural(findings, "finding"),
		plural(failures, "extraction failure"),
	)

	if findings > 0 || failures > 0 {
		return 1
	}

	return 0
}

// report processes one artifact and prints its block: either a header naming
// the file, its format and its finding count followed by one indented line per
// finding, or a single failure line. It returns the number of findings printed
// and whether the artifact could not be read.
//
// A failure is reported and returned, never propagated: the caller continues
// to the remaining artifacts, because one unreadable file must not hide the
// findings of the others.
func report(ctx context.Context, d deps, path string, stdout io.Writer) (int, bool) {
	selected, ok := backendFor(d.backends, path)
	if !ok {
		printf(stdout, "%s: unsupported artifact extension %q: expected one of %s\n",
			path, filepath.Ext(path), strings.Join(extensions(d.backends), ", "))

		return 0, true
	}

	model, err := selected.extract(ctx, path)
	if err != nil {
		printf(stdout, "%s (%s): extraction failed: %s\n", path, selected.label, oneLine(err.Error()))

		return 0, true
	}

	findings := d.verify(model)

	printf(stdout, "%s (%s): %s\n", path, selected.label, plural(len(findings), "finding"))

	for _, finding := range findings {
		dest := finding.Path
		if dest == "" {
			dest = emptyPathPlaceholder
		}

		printf(stdout, "\t%s\t%s\t%s\n", finding.Code, dest, oneLine(finding.Message))
	}

	return len(findings), false
}

// discover globs dir for each backend's extension, in backend order, sorting
// each glob's matches and concatenating the results.
//
// The order is fixed rather than incidental: filepath.Glob already returns
// sorted names, and sorting again costs nothing and makes the guarantee this
// function's callers rely on local to it.
func discover(dir string, backends []backend) ([]string, error) {
	var paths []string

	for _, b := range backends {
		pattern := filepath.Join(dir, "*"+b.ext)

		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("search %s: %w", pattern, err)
		}

		sort.Strings(matches)

		paths = append(paths, matches...)
	}

	return paths, nil
}

// backendFor selects the backend whose extension matches path's, comparing
// case-insensitively so an artifact named A.DEB is not silently unsupported.
func backendFor(backends []backend, path string) (backend, bool) {
	ext := filepath.Ext(path)

	for _, b := range backends {
		if strings.EqualFold(b.ext, ext) {
			return b, true
		}
	}

	return backend{}, false
}

// printNoArtifacts explains an empty search, which is the normal outcome on a
// development host and inside the development image.
//
// Everything it says is true at this commit. The three workflows named are the
// CI producers of a package: .github/workflows/release.yml runs GoReleaser Pro
// on a tag, .github/workflows/snapshot.yml runs it on main, and
// .github/workflows/packaging.yml runs it on pushes and pull requests targeting
// main, where it verifies the artifacts and publishes nothing.
// The local producer is `make package`, which builds a snapshot into dist/ but
// requires the goreleaser Pro distribution, so it does not succeed on a stock
// development host or in the development image — the message says so rather
// than pointing a reader at a target that will only fail for them unexplained.
func printNoArtifacts(stderr io.Writer, dir string, backends []backend) {
	patterns := make([]string, 0, len(backends))
	for _, b := range backends {
		patterns = append(patterns, filepath.Join(dir, "*"+b.ext))
	}

	printf(stderr, "%s: no package artifacts found in %s: searched %s\n",
		toolName, dir, strings.Join(patterns, " and "))
	printf(stderr, "%s: GoReleaser Pro builds them in CI: .github/workflows/release.yml on a tag, .github/workflows/snapshot.yml on main, .github/workflows/packaging.yml on pushes and pull requests targeting main.\n",
		toolName)
	printf(stderr, "%s: locally, `make package` builds them into dist/, but it requires goreleaser Pro, which is not installed on a stock development host or in the development image, so it will not succeed there.\n",
		toolName)
}

// extensions lists the artifact extensions the backends claim, for the
// unsupported-extension message.
func extensions(backends []backend) []string {
	exts := make([]string, 0, len(backends))
	for _, b := range backends {
		exts = append(exts, b.ext)
	}

	return exts
}

// oneLine folds a multi-line string onto one line so that every line of the
// report belongs to exactly one artifact. An external tool's standard error is
// folded into an extraction error verbatim and may span lines; without this a
// continuation line would read as though it were a separate artifact's.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")

	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "; ")
}

// printf writes one formatted line of the report.
//
// Every write this command makes goes through here, and the write error is
// deliberately discarded: the destinations are the process's own standard
// output and standard error, a failure to write to them cannot be reported
// anywhere, and it must not change the exit status, which means "did the
// artifacts verify" and nothing else.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// plural renders a count with its noun, pluralized by appending s.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}

	return fmt.Sprintf("%d %ss", n, noun)
}
