package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/frostyard/pilothouse/packaging"
)

// The three external tools the rpm backend runs. rpm answers every metadata
// question; rpm2archive and tar together produce the payload bytes.
//
// rpm2archive rather than the older rpm2cpio, because rpm2cpio cannot read a
// package nfpm produced — which is every package this repository ships.
// nfpm writes RPMTAG_ARCHIVESIZE as the sum of the member file sizes, while
// rpmbuild writes the size of the whole uncompressed cpio archive. rpm 4.18's
// rpm2cpio validates the bytes it emitted against that tag and exits 1 when
// they disagree, after emitting a complete and perfectly valid cpio stream and
// without writing a word to stderr. Against a goreleaser artifact it therefore
// fails with a bare "exit status 1" while nothing is actually wrong with the
// payload. rpm2archive reads the same packages without complaint, ships in the
// same rpm package, and is upstream's supported replacement.
//
// No unit-test fixture reproduces that shape, because every fixture here is
// built by rpmbuild, which writes the tag the other way — that is exactly why
// the bug survived until a real artifact met the verifier. The regression guard
// is .github/workflows/packaging.yml, which runs `make verify-packages` over
// the actual goreleaser output on every push and pull request to main: a
// revert to rpm2cpio turns that gate red immediately.
const (
	rpmTool         = "rpm"
	rpm2archiveTool = "rpm2archive"
	tarTool         = "tar"
)

// rpmFileTableQuery is the one query that produces the whole file table.
//
// It is an explicit `--qf` naming the five header tags this model needs and
// nothing else, rather than one of rpm's positional formatting aliases. Three
// measured reasons, in order of weight:
//
//   - A destination containing a space makes every whitespace-positional scheme
//     unsound: `/opt/phx/with space.txt` splits into one extra field and shifts
//     every column after it. Tab separation is immune, because rpm's own
//     formatter writes the separators.
//   - An alias's column set is a formatting detail rpm documents nowhere and
//     has changed before, so a parser written against one has a positional
//     dependence on something that is not an interface.
//   - %{FILEFLAGS} is the raw header bit, read here with the named
//     rpmfileConfig constant, rather than a pre-digested "is this a config
//     file" column.
//
// It is written as a raw string literal because rpm, not Go, interprets the
// escape sequences.
const rpmFileTableQuery = `[%{FILENAMES}\t%{FILEMODES:octal}\t%{FILEFLAGS}\t%{FILEUSERNAME}\t%{FILEGROUPNAME}\n]`

// rpmFileTableFields is the exact number of tab-separated fields
// rpmFileTableQuery produces per line. A line carrying any other count is an
// error rather than a row parsed as far as it goes.
const rpmFileTableFields = 5

// rpmRequireFlagsQuery reads the provenance flag word of every entry in the
// package's requires table, and carries no dependency text at all, so it cannot
// become a second and divergent parse of the dependency names themselves. Its
// lines are index-parallel with `rpm -qp --requires`.
const rpmRequireFlagsQuery = `[%{REQUIREFLAGS}\n]`

// rpmPostinstallQuery reads the postinstall body out of the POSTIN header tag,
// behind rpm's documented query-format conditional, so the output starts with a
// presence marker the body cannot influence: rpm emits the marker from a
// tag-presence test, before the body, at a fixed position.
//
// The human-readable scriptlet report rpm can print instead is deliberately not
// parsed, and this is the stronger of this file's two divergences from a
// formatted rendering. That report concatenates every scriptlet under a prose
// header with no escaping, no quoting and no length prefix, so a body
// containing the lines
//
//	preuninstall scriptlet (using /bin/sh):
//	postinstall program: /bin/sh
//
// renders them at column 0, byte-identical to real headers (measured on rpm
// 4.18). Any header-anchored terminator therefore truncates such a body, and
// the truncated bytes can still be exactly what a caller expected — a package
// shipping extra postinstall code would look correct. No regexp fixes that: the
// information needed to tell body from header is not in the output.
const rpmPostinstallQuery = `%|POSTIN?{HAS\n%{POSTIN}}:{NONE\n}|`

// The two markers rpmPostinstallQuery can emit on its first line. Anything else
// is an error, never a guess.
const (
	rpmPostinstallPresent = "HAS"
	rpmPostinstallAbsent  = "NONE"
)

// rpmNoneOutput is what rpm prints for an array query over a tag the package
// does not carry. It means zero rows, not a malformed answer.
const rpmNoneOutput = "(none)"

// rpmfileConfig is rpm's RPMFILE_CONFIG bit in the %{FILEFLAGS} word: the
// package's own designation of an entry as a configuration file.
const rpmfileConfig = 1

// The mode bits rpm records per file, in the octal encoding %{FILEMODES:octal}
// produces. Only the two types this model represents are kept; the permission
// mask is applied on its own, and setuid, setgid and sticky are deliberately
// NOT translated into Go's ModeSetuid, ModeSetgid and ModeSticky, because the
// contract compares Mode.Perm() only.
const (
	rpmModeTypeMask = 0o170000
	rpmModeRegular  = 0o100000
	rpmModeDir      = 0o040000
	rpmModePerm     = 0o777
)

// The two provenance bits in an rpm requires entry's flag word. Both mark an
// entry the BUILDER wrote into the header rather than one any packaging author
// declared, which is why filtering on them cannot hide anything a packaging
// author asked for:
//
//   - RPMSENSE_INTERP is what rpmbuild derives from a scriptlet's interpreter.
//     Measured, ANY %post section adds such an entry even under
//     `AutoReqProv: no`.
//   - RPMSENSE_RPMLIB marks a payload-format capability the builder records so
//     that an older rpm refuses the package outright.
//
// The filter is written against these FLAGS and never against a dependency
// name. A name test cannot tell a builder-generated interpreter requirement
// from one a package genuinely declares — measured, a package that declares the
// interpreter itself records a SECOND entry whose flag word is 0, and that one
// survives here.
const (
	rpmsenseInterp = 1 << 8
	rpmsenseRPMLIB = 1 << 24
)

// RPM reads the .rpm at path and returns the model describing it.
//
// It runs four `rpm -qp` metadata queries under ctx and then extracts the
// payload once. Nothing is cached between them:
//
//   - rpmFileTableQuery produces the whole file table — destination, mode,
//     config bit, owner and group — in one query, so the configuration-file
//     designation comes from the same query as everything else.
//   - `rpm -qp --requires` produces the dependency text, and
//     rpmRequireFlagsQuery the index-parallel provenance flag word of each.
//   - rpmPostinstallQuery produces the postinstall body behind a presence
//     marker.
//   - `rpm2archive -n -` reading the artifact on standard input, piped into
//     `tar -x --no-same-owner`, produces the payload bytes. The pipe is wired
//     in Go, so no shell is involved and both exit statuses are checked.
//
// Entries are exactly the paths the package owns. rpm owns only what its
// `%files` section lists, so a package rooted at /opt/phx/… and /usr/bin/…
// yields NO entry for /opt, /opt/phx, /usr or /usr/bin — the deliberate
// opposite of a deb, whose archive carries every intermediate directory. Never
// state the two backends' behaviour here as one claim.
//
// Directory entries carry Mode with fs.ModeDir set and nil Content; regular
// files carry their bytes except under usrBinDir; symlinks, devices and fifos
// are not emitted at all, so a destination the contract requires but the
// package ships as one of those is reported by the parent package as absent
// rather than silently accepted.
//
// Owner and Group are populated per entry from the file table. This is the
// deliberate asymmetry with the deb backend, which leaves both empty because a
// dpkg-deb-extracted tree cannot recover the archive's recorded ownership; the
// rpm header carries the names directly. Both fields remain informational and
// drive no assertion (M1).
//
// Dependencies are the requires entries rpm did not generate itself, passed
// through verbatim — version constraints, spacing and all. rpm does not
// preserve declaration order, so a caller comparing them sorts first.
//
// Postinstall is nil when the package ships no postinstall scriptlet. rpm
// records an empty %post section as no body at all, so "ships an empty
// scriptlet" is not a state an rpm artifact can represent. Whatever body rpm
// did record is returned byte for byte: rpm strips every trailing newline from
// a recorded scriptlet, and re-adding one here would put a byte in the model
// that the package does not contain.
func RPM(ctx context.Context, path string) (packaging.Model, error) {
	scratch, err := os.MkdirTemp("", "pilothouse-extract-rpm-")
	if err != nil {
		return packaging.Model{}, fmt.Errorf("packaging/extract: create scratch directory: %w", err)
	}

	defer func() { _ = os.RemoveAll(scratch) }()

	payloadDir := filepath.Join(scratch, "payload")
	if err := os.Mkdir(payloadDir, scratchDirMode); err != nil {
		return packaging.Model{}, fmt.Errorf("packaging/extract: create scratch directory: %w", err)
	}

	table, err := runTool(ctx, rpmTool, "-qp", "--qf", rpmFileTableQuery, path)
	if err != nil {
		return packaging.Model{}, err
	}

	entries, err := rpmEntries(table)
	if err != nil {
		return packaging.Model{}, err
	}

	requires, err := runTool(ctx, rpmTool, "-qp", "--requires", path)
	if err != nil {
		return packaging.Model{}, err
	}

	requireFlags, err := runTool(ctx, rpmTool, "-qp", "--qf", rpmRequireFlagsQuery, path)
	if err != nil {
		return packaging.Model{}, err
	}

	dependencies, err := rpmDependencies(requires, requireFlags)
	if err != nil {
		return packaging.Model{}, err
	}

	postinstallOutput, err := runTool(ctx, rpmTool, "-qp", "--qf", rpmPostinstallQuery, path)
	if err != nil {
		return packaging.Model{}, err
	}

	postinstall, err := rpmPostinstall(postinstallOutput)
	if err != nil {
		return packaging.Model{}, err
	}

	if err := rpmExtractPayload(ctx, path, payloadDir); err != nil {
		return packaging.Model{}, err
	}

	if err := rpmReadContent(entries, payloadDir); err != nil {
		return packaging.Model{}, err
	}

	return packaging.Model{
		Format:       packaging.FormatRPM,
		Entries:      entries,
		Dependencies: dependencies,
		Postinstall:  postinstall,
	}, nil
}

// rpmEntries maps rpmFileTableQuery's output onto entries, without Content:
// the bytes come from the extracted payload, not from the header.
//
// Empty output and the literal "(none)" both mean a package that owns no files.
// Anything else parses strictly.
func rpmEntries(raw []byte) ([]packaging.Entry, error) {
	var entries []packaging.Entry

	for _, line := range rpmLines(raw) {
		entry, keep, err := rpmFileTableEntry(line)
		if err != nil {
			return nil, err
		}

		if keep {
			entries = append(entries, entry)
		}
	}

	return entries, nil
}

// rpmFileTableEntry parses one line of the file table.
//
// It reports keep=false for a payload member this model does not represent —
// a symlink, device or fifo — and an error for a line it cannot read at all.
// The split is on tabs, because strings.Split preserves empty fields: an entry
// whose owner or group name is empty is present-but-empty rather than a row
// with a field silently collapsed away, and a destination containing a space is
// one field rather than two.
func rpmFileTableEntry(line string) (packaging.Entry, bool, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != rpmFileTableFields {
		return packaging.Entry{}, false, fmt.Errorf(
			"packaging/extract: rpm file table line %q has %d tab-separated fields, want %d",
			line, len(fields), rpmFileTableFields)
	}

	dest, modeField, flagsField, owner, group := fields[0], fields[1], fields[2], fields[3], fields[4]

	// Base 8 explicitly: rpm renders the mode as octal digits, with or without
	// a leading zero (0100644 and 100644 are the same file), and a base Go
	// inferred would read the second of those as decimal.
	mode, err := strconv.ParseUint(modeField, 8, 32)
	if err != nil {
		return packaging.Entry{}, false, fmt.Errorf(
			"packaging/extract: rpm file table line %q has a mode field that is not octal: %w", line, err)
	}

	flags, err := strconv.ParseInt(flagsField, 10, 64)
	if err != nil {
		return packaging.Entry{}, false, fmt.Errorf(
			"packaging/extract: rpm file table line %q has a flags field that is not decimal: %w", line, err)
	}

	entry := packaging.Entry{
		Dest:   dest,
		Mode:   fs.FileMode(mode & rpmModePerm),
		Config: flags&rpmfileConfig != 0,
		Owner:  owner,
		Group:  group,
	}

	switch mode & rpmModeTypeMask {
	case rpmModeRegular:
		return entry, true, nil
	case rpmModeDir:
		entry.Mode |= fs.ModeDir

		return entry, true, nil
	default:
		return packaging.Entry{}, false, nil
	}
}

// rpmDependencies pairs the dependency text with its provenance flag words and
// keeps the entries rpm did not generate itself.
//
// The two queries are index-parallel. Unequal line counts mean the pairing
// assumption does not hold for this package, which is an error rather than a
// merge truncated to the shorter of the two: a dependency list silently paired
// against the wrong flags would drop or keep arbitrary entries.
func rpmDependencies(requires, requireFlags []byte) ([]string, error) {
	names := rpmLines(requires)
	words := rpmLines(requireFlags)

	if len(names) != len(words) {
		return nil, fmt.Errorf(
			"packaging/extract: rpm reported %d requires entries but %d requires flag words; they are read as index-parallel and cannot be paired",
			len(names), len(words))
	}

	var dependencies []string

	for i, name := range names {
		flags, err := strconv.ParseInt(words[i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"packaging/extract: rpm requires flag word %q is not decimal: %w", words[i], err)
		}

		if flags&(rpmsenseInterp|rpmsenseRPMLIB) != 0 {
			continue
		}

		dependencies = append(dependencies, name)
	}

	return dependencies, nil
}

// rpmPostinstall parses rpmPostinstallQuery's output.
//
// It splits on the FIRST newline and nothing else. The marker is consumed
// positionally, so no body — however header-shaped, however long — can change
// the verdict, and everything after that one newline is the body, byte for
// byte: never scanned, never trimmed, never re-terminated.
func rpmPostinstall(raw []byte) (*packaging.Scriptlet, error) {
	marker, body, found := strings.Cut(string(raw), "\n")
	if !found {
		return nil, fmt.Errorf(
			"packaging/extract: rpm postinstall query returned %q, which carries no newline-terminated presence marker; want %q or %q",
			raw, rpmPostinstallPresent, rpmPostinstallAbsent)
	}

	switch marker {
	case rpmPostinstallAbsent:
		return nil, nil //nolint:nilnil // a nil Scriptlet is the model's representation of "ships none".
	case rpmPostinstallPresent:
		return &packaging.Scriptlet{Content: []byte(body)}, nil
	default:
		return nil, fmt.Errorf(
			"packaging/extract: rpm postinstall query returned presence marker %q; want %q or %q",
			marker, rpmPostinstallPresent, rpmPostinstallAbsent)
	}
}

// rpmLines splits one rpm query's output into its lines, dropping the single
// terminator rpm writes after the last one.
//
// Empty output and the literal "(none)" both mean zero rows. Nothing else is
// interpreted: a blank line anywhere else survives as a blank line and its
// caller rejects it.
func rpmLines(raw []byte) []string {
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" || text == rpmNoneOutput {
		return nil
	}

	return strings.Split(text, "\n")
}

// rpmExtractPayload extracts the artifact's payload into dir.
//
// The artifact is opened here and handed to rpm2archive on standard input,
// with `-` as its operand, for two reasons: rpm2archive given a path writes
// <path>.tgz beside it, which would leave a stray file in the directory being
// verified, and `-n` then makes it emit an uncompressed tar so neither side of
// the pipe spends time on a gzip round trip.
func rpmExtractPayload(ctx context.Context, artifact, dir string) error {
	file, err := os.Open(artifact)
	if err != nil {
		return fmt.Errorf("packaging/extract: open %s: %w", artifact, err)
	}

	defer func() { _ = file.Close() }()

	return runPipe(ctx, dir,
		pipeCommand{name: rpm2archiveTool, args: []string{"-n", "-"}, stdin: file},
		pipeCommand{name: tarTool, args: []string{"-x", "--no-same-owner"}},
	)
}

// rpmReadContent fills in Content for the entries whose bytes matter, reading
// them out of the extracted payload tree.
//
// Directories get none (they have none), and neither does anything under
// usrBinDir: those are build outputs whose bytes are compared against nothing
// and are roughly 20 MB each, so reading them in would be pure waste. Their nil
// Content means "deliberately not captured".
func rpmReadContent(entries []packaging.Entry, payloadDir string) error {
	for i := range entries {
		entry := &entries[i]

		if entry.Mode.IsDir() || underUsrBin(entry.Dest) {
			continue
		}

		name := filepath.Join(payloadDir, filepath.FromSlash(strings.TrimPrefix(entry.Dest, "/")))

		content, err := os.ReadFile(name)
		if err != nil {
			return fmt.Errorf("packaging/extract: read %s from the extracted rpm payload: %w", entry.Dest, err)
		}

		entry.Content = content
	}

	return nil
}

// pipeCommand names one half of a two-process pipe.
//
// stdin, when non-nil, becomes src's standard input. It exists because
// rpm2archive writes <artifact>.tgz beside its input when handed a path, which
// would mutate the very directory being verified; reading the artifact on
// standard input with `-` makes it write to standard output instead and leaves
// dist/ untouched.
type pipeCommand struct {
	name  string
	args  []string
	stdin *os.File
}

// runPipe runs src with its standard output connected to dst's standard input,
// both under ctx, with dst running in dir.
//
// The pipe is wired here in Go with StdoutPipe rather than handed to a shell:
// no shell is invoked, so no argument can be reinterpreted as shell syntax, and
// BOTH exit statuses are the verdict — a shell pipeline would report only the
// last one. Every error carries the same "packaging/extract: <tool>: " prefix
// runTool produces, naming which half failed.
//
// What each half wrote to standard error is folded into the error and is read
// for NOTHING else. cpio writes its `2 blocks` progress line there on SUCCESS,
// so treating a non-empty standard error as a failure would fail every correct
// extraction.
//
// The pipe is an os.Pipe rather than srcCmd.StdoutPipe(), and BOTH parent
// copies of it are closed as soon as both children are running. That is what
// makes an early dst exit survivable: with the parent holding no read end, a
// src that keeps writing into the drained-and-abandoned pipe takes SIGPIPE and
// dies, instead of blocking forever on a full buffer nobody will ever read.
// StdoutPipe cannot give that guarantee — the parent owns its read end, and
// exec's own documentation warns that Wait closes it out from under any
// in-flight copy.
//
// Both halves are then waited on CONCURRENTLY. Waiting on src first is what
// deadlocked earlier: if dst had already exited, src was still blocked writing,
// so src's Wait never returned and dst's status was never collected.
//
// Error precedence: src's failure is reported first because a src that dies
// mid-stream makes dst fail too and is the more useful root cause — EXCEPT when
// src died of SIGPIPE, which means dst went away first and dst's status is the
// real explanation.
func runPipe(ctx context.Context, dir string, src, dst pipeCommand) error {
	srcPath, err := lookTool(src.name)
	if err != nil {
		return err
	}

	dstPath, err := lookTool(dst.name)
	if err != nil {
		return err
	}

	srcCmd := exec.CommandContext(ctx, srcPath, src.args...)
	dstCmd := exec.CommandContext(ctx, dstPath, dst.args...)
	dstCmd.Dir = dir

	if src.stdin != nil {
		srcCmd.Stdin = src.stdin
	}

	var srcStderr, dstStderr bytes.Buffer

	srcCmd.Stderr = &srcStderr
	dstCmd.Stderr = &dstStderr

	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		return pipeError(src.name, err, &srcStderr)
	}

	// Both ends are *os.File, so exec hands the descriptors straight to the
	// children instead of interposing a copying goroutine in this process.
	srcCmd.Stdout = pipeWrite
	dstCmd.Stdin = pipeRead

	if err := srcCmd.Start(); err != nil {
		_ = pipeRead.Close()
		_ = pipeWrite.Close()

		return pipeError(src.name, err, &srcStderr)
	}

	if err := dstCmd.Start(); err != nil {
		// Drop both ends before reaping src: without a reader, a src already
		// writing cannot wedge on a full pipe while we wait for it.
		_ = pipeRead.Close()
		_ = pipeWrite.Close()
		_ = srcCmd.Process.Kill()
		_ = srcCmd.Wait()

		return pipeError(dst.name, err, &dstStderr)
	}

	// The children hold their own descriptors now. This process must let go of
	// both: keeping the write end open would deny dst its end of input, and
	// keeping the read end open would let src block forever if dst exits early.
	_ = pipeRead.Close()
	_ = pipeWrite.Close()

	var (
		wg             sync.WaitGroup
		srcErr, dstErr error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()

		srcErr = srcCmd.Wait()
	}()

	go func() {
		defer wg.Done()

		dstErr = dstCmd.Wait()
	}()

	wg.Wait()

	if srcErr != nil && !isBrokenPipe(srcErr) {
		return pipeError(src.name, srcErr, &srcStderr)
	}

	if dstErr != nil {
		return pipeError(dst.name, dstErr, &dstStderr)
	}

	if srcErr != nil {
		return pipeError(src.name, srcErr, &srcStderr)
	}

	return nil
}

// isBrokenPipe reports whether err is a process killed by SIGPIPE — the shape a
// producer takes when its consumer exited first. It is the one src failure that
// says nothing about src.
func isBrokenPipe(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	status, ok := exitErr.Sys().(syscall.WaitStatus)

	return ok && status.Signaled() && status.Signal() == syscall.SIGPIPE
}

// pipeError wraps one half of a pipe's failure in the same shape runTool
// produces, so a caller recognises which tool failed from a token no
// artifact's filename can forge.
func pipeError(name string, err error, stderr *bytes.Buffer) error {
	if detail := strings.TrimSpace(stderr.String()); detail != "" {
		return fmt.Errorf("packaging/extract: %s: %w: %s", name, err, detail)
	}

	return fmt.Errorf("packaging/extract: %s: %w", name, err)
}
