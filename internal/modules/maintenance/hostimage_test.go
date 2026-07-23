package maintenance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootcBootedOnly, bootcBootedStaged, and bootcBootedStagedRollback are shaped
// after real `bootc status --json` output (apiVersion org.containers.bootc/v1,
// kind BootcHost, .status.{booted,staged,rollback} each optionally present with
// an image reference and digest), including keys this parser deliberately
// ignores, so the fixtures prove unknown fields do not break decoding.
const bootcBootedOnly = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "metadata": {"name": "host"},
  "spec": {"image": {"image": "quay.io/frostyard/snosi:stable", "transport": "registry"}, "bootOrder": "default"},
  "status": {
    "staged": null,
    "booted": {
      "image": {
        "image": {"image": "quay.io/frostyard/snosi:stable", "transport": "registry"},
        "version": "41.20260701.0",
        "timestamp": "2026-07-01T00:00:00Z",
        "imageDigest": "sha256:1111111111111111111111111111111111111111111111111111111111111111"
      },
      "cachedUpdate": null,
      "incompatible": false,
      "pinned": false,
      "store": "ostree",
      "ostree": {"checksum": "aaaa", "deploySerial": 0}
    },
    "rollback": null,
    "rollbackQueued": false,
    "type": "bootcHost"
  }
}`

const bootcBootedStaged = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "status": {
    "staged": {
      "image": {
        "image": {"image": "quay.io/frostyard/snosi:next", "transport": "registry"},
        "imageDigest": "sha256:2222222222222222222222222222222222222222222222222222222222222222"
      },
      "incompatible": false,
      "pinned": false
    },
    "booted": {
      "image": {
        "image": {"image": "quay.io/frostyard/snosi:stable", "transport": "registry"},
        "imageDigest": "sha256:1111111111111111111111111111111111111111111111111111111111111111"
      }
    },
    "rollback": null,
    "type": "bootcHost"
  }
}`

const bootcBootedStagedRollback = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "status": {
    "staged": {
      "image": {
        "image": {"image": "quay.io/frostyard/snosi:next", "transport": "registry"},
        "imageDigest": "sha256:2222222222222222222222222222222222222222222222222222222222222222"
      }
    },
    "booted": {
      "image": {
        "image": {"image": "quay.io/frostyard/snosi:stable", "transport": "registry"},
        "imageDigest": "sha256:1111111111111111111111111111111111111111111111111111111111111111"
      }
    },
    "rollback": {
      "image": {
        "image": {"image": "quay.io/frostyard/snosi:previous", "transport": "registry"},
        "imageDigest": "sha256:3333333333333333333333333333333333333333333333333333333333333333"
      }
    },
    "rollbackQueued": false,
    "type": "bootcHost"
  }
}`

func TestParseBootcStatusBootedOnly(t *testing.T) {
	status, err := ParseBootcStatus([]byte(bootcBootedOnly))
	require.NoError(t, err)
	assert.True(t, status.BootcAvailable)
	assert.Empty(t, status.BootcError)
	require.NotNil(t, status.Booted)
	assert.Equal(t, "quay.io/frostyard/snosi:stable", status.Booted.Image)
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", status.Booted.Digest)
	assert.Nil(t, status.Staged, "a null staged slot must not become a Deployment")
	assert.Nil(t, status.Rollback, "a null rollback slot must not become a Deployment")
	assert.Nil(t, status.SoftRebootCapable, "this fixture exposes no soft-reboot key")
}

func TestParseBootcStatusBootedAndStaged(t *testing.T) {
	status, err := ParseBootcStatus([]byte(bootcBootedStaged))
	require.NoError(t, err)
	assert.True(t, status.BootcAvailable)
	require.NotNil(t, status.Booted)
	assert.Equal(t, "quay.io/frostyard/snosi:stable", status.Booted.Image)
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", status.Booted.Digest)
	require.NotNil(t, status.Staged)
	assert.Equal(t, "quay.io/frostyard/snosi:next", status.Staged.Image)
	assert.Equal(t, "sha256:2222222222222222222222222222222222222222222222222222222222222222", status.Staged.Digest)
	assert.Nil(t, status.Rollback)
}

func TestParseBootcStatusBootedStagedAndRollback(t *testing.T) {
	status, err := ParseBootcStatus([]byte(bootcBootedStagedRollback))
	require.NoError(t, err)
	assert.True(t, status.BootcAvailable)
	require.NotNil(t, status.Booted)
	assert.Equal(t, "quay.io/frostyard/snosi:stable", status.Booted.Image)
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", status.Booted.Digest)
	require.NotNil(t, status.Staged)
	assert.Equal(t, "quay.io/frostyard/snosi:next", status.Staged.Image)
	assert.Equal(t, "sha256:2222222222222222222222222222222222222222222222222222222222222222", status.Staged.Digest)
	require.NotNil(t, status.Rollback)
	assert.Equal(t, "quay.io/frostyard/snosi:previous", status.Rollback.Image)
	assert.Equal(t, "sha256:3333333333333333333333333333333333333333333333333333333333333333", status.Rollback.Digest)
}

// TestParseBootcStatusDeploymentPresentWithoutImage pins the split between
// "this deployment slot exists" and "we know which image it is": a slot bootc
// reports without an image reference is still a Deployment, just an unnamed
// one, so later consumers can tell it apart from an absent slot.
func TestParseBootcStatusDeploymentPresentWithoutImage(t *testing.T) {
	status, err := ParseBootcStatus([]byte(`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":null,"store":"ostree"}}}`))
	require.NoError(t, err)
	require.NotNil(t, status.Booted)
	assert.Empty(t, status.Booted.Image)
	assert.Empty(t, status.Booted.Digest)
}

// TestParseBootcStatusSoftRebootEligibility covers the three-state contract:
// present-and-true, present-and-false, and absent (nil, never an error).
//
// The key was confirmed against bootc's published schema
// (github.com/bootc-dev/bootc, `crates/lib/src/spec.rs`): `softRebootCapable`
// on a `BootEntry`, with no equivalent on `HostStatus`. The cases below pin
// both where the parser reads it (staged first, then booted) and where it
// deliberately does not (a status-level key of the same name is not bootc's
// shape and must not be mistaken for eligibility).
func TestParseBootcStatusSoftRebootEligibility(t *testing.T) {
	boolPtr := func(value bool) *bool { return &value }
	cases := []struct {
		input string
		name  string
		want  *bool
	}{
		{
			name:  "staged entry reports eligible",
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}},"staged":{"image":{"image":{"image":"img:b"},"imageDigest":"sha256:b"},"softRebootCapable":true}}}`,
			want:  boolPtr(true),
		},
		{
			name:  "staged entry reports ineligible",
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}},"staged":{"image":{"image":{"image":"img:b"},"imageDigest":"sha256:b"},"softRebootCapable":false}}}`,
			want:  boolPtr(false),
		},
		{
			name:  "booted entry when nothing is staged",
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"softRebootCapable":true}}}`,
			want:  boolPtr(true),
		},
		{
			name:  "booted entry reporting ineligible when nothing is staged",
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"},"softRebootCapable":false}}}`,
			want:  boolPtr(false),
		},
		{
			name:  "a status-level key of the same name is not bootc's shape and is ignored",
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"softRebootCapable":true,"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`,
			want:  nil,
		},
		{
			name:  "staged entry wins over booted entry",
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"softRebootCapable":true},"staged":{"softRebootCapable":false}}}`,
			want:  boolPtr(false),
		},
		{
			name:  "older bootc exposes no eligibility key",
			input: bootcBootedStaged,
			want:  nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, err := ParseBootcStatus([]byte(testCase.input))
			require.NoError(t, err, "a missing or unexpected eligibility key is never a parse error")
			assert.True(t, status.BootcAvailable)
			if testCase.want == nil {
				assert.Nil(t, status.SoftRebootCapable, "absent key means unknown, not false")
				return
			}
			require.NotNil(t, status.SoftRebootCapable)
			assert.Equal(t, *testCase.want, *status.SoftRebootCapable)
		})
	}
}

// TestParseBootcStatusSoftRebootCapableIsACopy proves the returned
// pointer is a copy, so a consumer mutating it cannot reach back into decoded
// state shared with anything else.
func TestParseBootcStatusSoftRebootCapableIsACopy(t *testing.T) {
	input := []byte(`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"staged":{"softRebootCapable":true}}}`)
	first, err := ParseBootcStatus(input)
	require.NoError(t, err)
	require.NotNil(t, first.SoftRebootCapable)
	*first.SoftRebootCapable = false
	second, err := ParseBootcStatus(input)
	require.NoError(t, err)
	require.NotNil(t, second.SoftRebootCapable)
	assert.True(t, *second.SoftRebootCapable)
}

// TestParseBootcStatusMalformed asserts the error contract: a non-nil error and
// a zero-value HostImageStatus (BootcAvailable false, every slot nil), never a
// partially populated result the caller might mistake for real bootc data.
func TestParseBootcStatusMalformed(t *testing.T) {
	cases := []struct {
		input string
		name  string
	}{
		{name: "empty input", input: ``},
		{name: "not JSON at all", input: `error: unknown command "status"`},
		{name: "truncated JSON", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":`},
		{name: "JSON array", input: `[{"kind":"BootcHost"}]`},
		{name: "JSON string", input: `"BootcHost"`},
		{name: "JSON null", input: `null`},
		{name: "unrelated JSON object", input: `{"some":"json"}`},
		{name: "wrong kind", input: `{"apiVersion":"org.containers.bootc/v1","kind":"SomethingElse","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "unexpected apiVersion", input: `{"apiVersion":"org.example.other/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "deployment slot of the wrong type", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":"stable"}}`},
		{name: "eligibility key of the wrong type", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"staged":{"softRebootCapable":"yes"}}}`},
		{name: "digest of the wrong type", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"imageDigest":42}}}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, err := ParseBootcStatus([]byte(testCase.input))
			require.Error(t, err)
			assert.Equal(t, HostImageStatus{}, status, "a failed parse must return a zero HostImageStatus, not partial data")
			assert.False(t, status.BootcAvailable)
		})
	}
}

// TestParseBootcStatusAcceptsFutureAPIVersion documents the deliberate
// looseness of the apiVersion discriminator: the kind is what identifies the
// document, and a schema revision must not turn a readable status into an
// error.
func TestParseBootcStatusAcceptsFutureAPIVersion(t *testing.T) {
	status, err := ParseBootcStatus([]byte(`{"apiVersion":"org.containers.bootc/v2","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`))
	require.NoError(t, err)
	require.NotNil(t, status.Booted)
	assert.Equal(t, "img:a", status.Booted.Image)
}

// TestHostImageRunsNoBootcSubcommand mechanically enforces the read-only
// invariant for hostimage.go: the file can execute nothing at all. It asserts
// the file's imports against an allowlist of pure decoding/formatting packages
// -- no os/exec, os, syscall, or network package -- so no bootc invocation of
// any kind (status included, mutations especially) can exist here, and
// separately that no string literal names a bootc mutation subcommand. The
// bootc command line itself belongs to the caller, which only ever runs
// `bootc status --json` through the package's injected Runner.
//
// "rollback" is deliberately absent from the forbidden verb list: it is the
// name of a read-only deployment slot this parser reports, and the import
// allowlist -- not word-matching -- is what proves `bootc rollback` can never
// be invoked from this file.
func TestHostImageRunsNoBootcSubcommand(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "hostimage.go", nil, parser.ParseComments)
	require.NoError(t, err)

	allowedImports := []string{"encoding/json", "fmt", "strings"}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		assert.Contains(t, allowedImports, path, "hostimage.go must not import anything that can run a command or reach the host")
	}

	forbiddenVerbs := []string{"upgrade", "switch", "rebase"}
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		for _, verb := range forbiddenVerbs {
			assert.False(t, strings.Contains(strings.ToLower(value), verb), "string literal %q references bootc mutation verb %q", value, verb)
		}
		return true
	})
}
