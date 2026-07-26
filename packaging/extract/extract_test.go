// This file is an INTERNAL test — package extract, not extract_test — because
// the two parsers it exercises are unexported. It exists to reach the shapes a
// real dpkg-deb cannot be made to emit: a Depends field with an empty
// comma-separated component, a conffiles member of nothing but blank lines. Per
// docs/agents/skills/test-the-composing-function-not-its-merge-helper.md these
// tables SUPPLEMENT the fixture-backed tests in deb_test.go, which drive the
// same parsers through Deb itself against a real .deb; they do not replace them,
// and no row here stands in for an end-to-end case that a fixture can express.
//
// Every expected value below is a hand-written literal.
package extract

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestDebConffilesParsesLines tables the conffiles control member.
//
// present=false is the absent-member row: a package that marks nothing a
// configuration file ships no such member at all, and that absence means "no
// configuration entries", not a failure.
func TestDebConffilesParsesLines(t *testing.T) {
	cases := []struct {
		name    string
		present bool
		body    string
		want    map[string]bool
		wantErr bool
	}{
		{
			name:    "member absent entirely",
			present: false,
			want:    map[string]bool{},
		},
		{
			name:    "empty member",
			present: true,
			body:    "",
			want:    map[string]bool{},
		},
		{
			name:    "blank lines only",
			present: true,
			body:    "\n\n\n",
			want:    map[string]bool{},
		},
		{
			name:    "whitespace-only lines only",
			present: true,
			body:    "   \n\t\n \t \n",
			want:    map[string]bool{},
		},
		{
			name:    "one absolute path",
			present: true,
			body:    "/etc/phx/phx.conf\n",
			want:    map[string]bool{"/etc/phx/phx.conf": true},
		},
		{
			name:    "no trailing newline",
			present: true,
			body:    "/etc/phx/phx.conf",
			want:    map[string]bool{"/etc/phx/phx.conf": true},
		},
		{
			name:    "several paths with blank lines between them",
			present: true,
			body:    "/etc/phx/phx.conf\n\n/etc/phx/other.conf\n \n",
			want: map[string]bool{
				"/etc/phx/phx.conf":   true,
				"/etc/phx/other.conf": true,
			},
		},
		{
			name:    "surrounding whitespace is trimmed",
			present: true,
			body:    "  /etc/phx/phx.conf\t\n",
			want:    map[string]bool{"/etc/phx/phx.conf": true},
		},
		{
			name:    "relative path",
			present: true,
			body:    "etc/phx/phx.conf\n",
			wantErr: true,
		},
		{
			name:    "relative path after a valid one",
			present: true,
			body:    "/etc/phx/phx.conf\netc/phx/other.conf\n",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			if tc.present {
				if err := os.WriteFile(filepath.Join(dir, "conffiles"), []byte(tc.body), 0o644); err != nil {
					t.Fatalf("write conffiles: %v", err)
				}
			}

			got, err := debConffiles(dir)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("debConffiles(%q) = %v, want an error", tc.body, got)
				}

				if got != nil {
					t.Errorf("debConffiles(%q) returned %v alongside its error, want nil", tc.body, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("debConffiles(%q): %v", tc.body, err)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("debConffiles(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestDebConffilesReportsAReadFailure covers the one remaining branch: a
// conffiles member that exists but cannot be read. An unreadable member is not
// an absent one, so it must not be reported as "no configuration entries".
func TestDebConffilesReportsAReadFailure(t *testing.T) {
	dir := t.TempDir()

	// A directory named conffiles is readable as a name and not as a file, which
	// is a read failure that is not fs.ErrNotExist and needs no permission
	// games — this suite also runs as root inside the dev image, where a
	// chmod-based unreadable file would still be readable.
	if err := os.Mkdir(filepath.Join(dir, "conffiles"), fs.FileMode(0o755)); err != nil {
		t.Fatalf("stage conffiles as a directory: %v", err)
	}

	got, err := debConffiles(dir)
	if err == nil {
		t.Fatalf("debConffiles = %v, want an error for an unreadable conffiles member", got)
	}

	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %q reports fs.ErrNotExist, but the member exists and could not be read", err)
	}
}

// TestDebDependenciesSplitsOnCommasOnly tables the Depends field.
//
// The alternatives and version-constraint rows are the load-bearing ones:
// passing them through verbatim is what keeps a rule against alternatives
// fireable and a drifting version visible instead of normalized away.
func TestDebDependenciesSplitsOnCommasOnly(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{
			name: "empty field",
			raw:  "",
			want: nil,
		},
		{
			name: "field is a bare newline",
			raw:  "\n",
			want: nil,
		},
		{
			name: "whitespace-only field",
			raw:  "  \t \n",
			want: nil,
		},
		{
			name: "one dependency",
			raw:  "alpha",
			want: []string{"alpha"},
		},
		{
			name: "one dependency with a trailing newline",
			raw:  "alpha\n",
			want: []string{"alpha"},
		},
		{
			name: "alternatives are preserved verbatim",
			raw:  "alpha | beta",
			want: []string{"alpha | beta"},
		},
		{
			name: "version constraints are preserved verbatim",
			raw:  "gamma (>= 1)",
			want: []string{"gamma (>= 1)"},
		},
		{
			name: "several dependencies in declaration order",
			raw:  "alpha | beta, gamma (>= 1), delta\n",
			want: []string{"alpha | beta", "gamma (>= 1)", "delta"},
		},
		{
			name: "inner whitespace inside an expression is untouched",
			raw:  "epsilon  (<<  2)  |  zeta",
			want: []string{"epsilon  (<<  2)  |  zeta"},
		},
		{
			name:    "trailing comma",
			raw:     "alpha,",
			wantErr: true,
		},
		{
			name:    "trailing comma with a newline after it",
			raw:     "alpha, beta,\n",
			wantErr: true,
		},
		{
			name:    "leading comma",
			raw:     ", alpha",
			wantErr: true,
		},
		{
			name:    "empty component between two commas",
			raw:     "alpha,,beta",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := debDependencies([]byte(tc.raw))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("debDependencies(%q) = %q, want an error", tc.raw, got)
				}

				if got != nil {
					t.Errorf("debDependencies(%q) returned %q alongside its error, want nil", tc.raw, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("debDependencies(%q): %v", tc.raw, err)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("debDependencies(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
