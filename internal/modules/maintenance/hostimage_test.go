package maintenance

import (
	"encoding/json"
	"fmt"
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

// rpmOStreeBootedOnly, rpmOStreeBootedStagedRollback, and the fixtures below
// are shaped after real `rpm-ostree status --json` output: a top-level
// `deployments` array, newest first (staged, then booted, then the rollback
// deployment), each entry carrying the ostree commit `checksum` and `version`
// this package wants plus the container-image keys and unrelated keys
// (`osname`, `origin`, `pinned`, `requested-packages`) it ignores. rpm-ostree
// flags the booted and staged deployments explicitly and has no flag for a
// rollback deployment, which is why the third entry below carries neither.
//
// The digests deliberately match the bootc fixtures above deployment for
// deployment, so a merge of the two is a merge of two views of one host.
const rpmOStreeBootedOnly = `{
  "deployments": [
    {
      "container-image-reference": "ostree-unverified-registry:quay.io/frostyard/snosi:stable",
      "container-image-reference-digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "checksum": "aaaa111111111111111111111111111111111111111111111111111111111111",
      "version": "41.20260701.0",
      "osname": "default",
      "booted": true,
      "staged": false,
      "pinned": false,
      "requested-packages": []
    }
  ],
  "transaction": null
}`

const rpmOStreeBootedStagedRollback = `{
  "deployments": [
    {
      "container-image-reference": "ostree-unverified-registry:quay.io/frostyard/snosi:next",
      "container-image-reference-digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
      "checksum": "bbbb222222222222222222222222222222222222222222222222222222222222",
      "version": "41.20260715.0",
      "osname": "default",
      "booted": false,
      "staged": true
    },
    {
      "container-image-reference": "ostree-unverified-registry:quay.io/frostyard/snosi:stable",
      "container-image-reference-digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "checksum": "aaaa111111111111111111111111111111111111111111111111111111111111",
      "version": "41.20260701.0",
      "osname": "default",
      "booted": true,
      "staged": false
    },
    {
      "container-image-reference": "ostree-unverified-registry:quay.io/frostyard/snosi:previous",
      "container-image-reference-digest": "sha256:3333333333333333333333333333333333333333333333333333333333333333",
      "checksum": "cccc333333333333333333333333333333333333333333333333333333333333",
      "version": "41.20260610.0",
      "osname": "default",
      "booted": false,
      "staged": false
    }
  ]
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
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"},"softRebootCapable":true}}}`,
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
			input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"},"softRebootCapable":true},"staged":{"softRebootCapable":false}}}`,
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
	input := []byte(`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}},"staged":{"softRebootCapable":true}}}`)
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
//
// The cases below enumerate the full matrix of what ParseBootcStatus requires,
// rather than only the syntactically broken payloads: for each mandatory
// element of the document (apiVersion, kind, the status object, and the booted
// deployment) both "omitted entirely" and "present but wrong/null" must fail.
// Omission must never be a way around a check -- a required field that is only
// validated when present is an optional field.
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
		{name: "empty JSON object", input: `{}`},
		{name: "unrelated JSON object", input: `{"some":"json"}`},
		{name: "wrong kind", input: `{"apiVersion":"org.containers.bootc/v1","kind":"SomethingElse","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "missing kind", input: `{"apiVersion":"org.containers.bootc/v1","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "empty kind", input: `{"apiVersion":"org.containers.bootc/v1","kind":"","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "unexpected apiVersion", input: `{"apiVersion":"org.example.other/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "missing apiVersion", input: `{"kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "missing apiVersion with an empty booted entry", input: `{"kind":"BootcHost","status":{"booted":{}}}`},
		{name: "empty apiVersion", input: `{"apiVersion":"","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "discriminators only, no status", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost"}`},
		{name: "null status", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":null}`},
		{name: "empty status object", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{}}`},
		{name: "status reporting no booted deployment", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"staged":{"image":{"image":{"image":"img:b"},"imageDigest":"sha256:b"}},"rollbackQueued":false}}`},
		{name: "null booted deployment", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":null,"staged":null,"rollback":null}}`},
		{name: "deployment slot of the wrong type", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":"stable"}}`},
		{name: "status of the wrong type", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":"booted"}`},
		{name: "apiVersion of the wrong type", input: `{"apiVersion":42,"kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}}}}`},
		{name: "eligibility key of the wrong type", input: `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"img:a"},"imageDigest":"sha256:a"}},"staged":{"softRebootCapable":"yes"}}}`},
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

// bootcBootedStagedRollbackSoftReboot is bootcBootedStagedRollback plus the
// soft-reboot eligibility key, so merge tests can prove the merge leaves that
// three-state value alone rather than only proving it stays nil.
const bootcBootedStagedRollbackSoftReboot = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "status": {
    "staged": {
      "image": {
        "image": {"image": "quay.io/frostyard/snosi:next", "transport": "registry"},
        "imageDigest": "sha256:2222222222222222222222222222222222222222222222222222222222222222"
      },
      "softRebootCapable": true
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
    "type": "bootcHost"
  }
}`

// TestParseBootcStatusLeavesSupplementaryFieldsEmpty pins the ownership split
// the merge depends on: Version and Checksum are rpm-ostree's to report, so
// bootc's parser must leave them empty even for a payload that does carry a
// version string and an ostree checksum of its own (bootcBootedOnly has both:
// `.status.booted.image.version` and `.status.booted.ostree.checksum`).
// Likewise the parser sets neither rpm-ostree availability field.
func TestParseBootcStatusLeavesSupplementaryFieldsEmpty(t *testing.T) {
	status, err := ParseBootcStatus([]byte(bootcBootedOnly))
	require.NoError(t, err)
	require.NotNil(t, status.Booted)
	assert.Empty(t, status.Booted.Version, "the version string is rpm-ostree's supplementary detail, not bootc's")
	assert.Empty(t, status.Booted.Checksum, "the ostree checksum is rpm-ostree's supplementary detail, not bootc's")
	assert.False(t, status.RPMOStreeAvailable, "parsing bootc says nothing about rpm-ostree")
	assert.Empty(t, status.RPMOStreeError)
}

// TestParseRPMOStreeStatusExtractsSupplementaryDetail covers the shapes the
// merge consumes: a single booted deployment, and a staged/booted/rollback
// list where only the first two carry a role flag.
func TestParseRPMOStreeStatusExtractsSupplementaryDetail(t *testing.T) {
	t.Run("booted only", func(t *testing.T) {
		supplement, err := ParseRPMOStreeStatus([]byte(rpmOStreeBootedOnly))
		require.NoError(t, err)
		require.Len(t, supplement.Deployments, 1)
		assert.Equal(t, rpmOStreeDeployment{
			Booted:   true,
			Checksum: "aaaa111111111111111111111111111111111111111111111111111111111111",
			Digest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Image:    "ostree-unverified-registry:quay.io/frostyard/snosi:stable",
			Staged:   false,
			Version:  "41.20260701.0",
		}, supplement.Deployments[0])
	})

	t.Run("staged, booted, and rollback", func(t *testing.T) {
		supplement, err := ParseRPMOStreeStatus([]byte(rpmOStreeBootedStagedRollback))
		require.NoError(t, err)
		require.Len(t, supplement.Deployments, 3)
		assert.True(t, supplement.Deployments[0].Staged)
		assert.False(t, supplement.Deployments[0].Booted)
		assert.Equal(t, "41.20260715.0", supplement.Deployments[0].Version)
		assert.Equal(t, "bbbb222222222222222222222222222222222222222222222222222222222222", supplement.Deployments[0].Checksum)
		assert.True(t, supplement.Deployments[1].Booted)
		assert.Equal(t, "41.20260701.0", supplement.Deployments[1].Version)
		assert.False(t, supplement.Deployments[2].Booted, "rpm-ostree has no rollback flag")
		assert.False(t, supplement.Deployments[2].Staged)
		assert.Equal(t, "41.20260610.0", supplement.Deployments[2].Version)
		assert.Equal(t, "cccc333333333333333333333333333333333333333333333333333333333333", supplement.Deployments[2].Checksum)
	})
}

// TestParseRPMOStreeStatusEmptyDeploymentsIsSuccess pins the distinction the
// caller needs: "rpm-ostree read fine and had nothing to add" is a success
// with no deployments, not an error. TestParseRPMOStreeStatusMalformed covers
// the other side of that distinction.
func TestParseRPMOStreeStatusEmptyDeploymentsIsSuccess(t *testing.T) {
	supplement, err := ParseRPMOStreeStatus([]byte(`{"deployments": [], "transaction": null}`))
	require.NoError(t, err, "an empty deployment list is a readable status, not a parse failure")
	assert.Empty(t, supplement.Deployments)
	assert.Equal(t, rpmOStreeSupplement{}, supplement)
}

// TestParseRPMOStreeStatusMalformed asserts the error contract: a non-nil
// error and a zero supplement, never partial data.
//
// The cases enumerate both halves of "structurally malformed" -- syntax
// (non-JSON, truncated, wrong JSON type) and substance (no `deployments` array
// at all, or one of the wrong type). rpm-ostree's document has no
// apiVersion/kind discriminator, so the required `deployments` array is the
// only thing separating its status output from any other JSON a caller might
// capture; a payload without it must fail rather than decode into a confident,
// empty success that the caller would then report as "rpm-ostree had nothing
// to add."
func TestParseRPMOStreeStatusMalformed(t *testing.T) {
	cases := []struct {
		input string
		name  string
	}{
		{name: "empty input", input: ``},
		{name: "not JSON at all", input: `error: Unknown command 'status --json'`},
		{name: "truncated JSON", input: `{"deployments": [{"booted": true`},
		{name: "JSON array", input: `[{"booted": true}]`},
		{name: "JSON string", input: `"deployments"`},
		{name: "JSON null", input: `null`},
		{name: "empty JSON object", input: `{}`},
		{name: "unrelated JSON object", input: `{"some":"json"}`},
		{name: "an error object from another tool", input: `{"error":"cannot connect to rpm-ostreed"}`},
		{name: "null deployments", input: `{"deployments": null}`},
		{name: "deployments of the wrong type", input: `{"deployments": {"booted": true}}`},
		{name: "deployment entry of the wrong type", input: `{"deployments": ["stable"]}`},
		{name: "version of the wrong type", input: `{"deployments": [{"booted": true, "version": 41}]}`},
		{name: "booted flag of the wrong type", input: `{"deployments": [{"booted": "yes"}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			supplement, err := ParseRPMOStreeStatus([]byte(testCase.input))
			require.Error(t, err)
			assert.Equal(t, rpmOStreeSupplement{}, supplement, "a failed parse must return a zero supplement, not partial data")
		})
	}
}

// TestMergeHostImageBootcOnly proves the no-rpm-ostree host is untouched: the
// merged result is byte-for-byte bootc's own parse, with the supplementary
// fields empty and -- critically -- the rpm-ostree availability fields left
// alone. MergeHostImage receives an already-parsed supplement and cannot know
// whether rpm-ostree failed, reported nothing, or was never run, so it must
// not fabricate an answer for a source it was never given.
func TestMergeHostImageBootcOnly(t *testing.T) {
	for _, fixture := range []struct {
		input string
		name  string
	}{
		{name: "booted only", input: bootcBootedOnly},
		{name: "booted and staged", input: bootcBootedStaged},
		{name: "booted, staged, and rollback", input: bootcBootedStagedRollback},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			bootc, err := ParseBootcStatus([]byte(fixture.input))
			require.NoError(t, err)

			merged := MergeHostImage(bootc, rpmOStreeSupplement{})

			assert.Equal(t, bootc, merged, "with no rpm-ostree data the merge must change nothing")
			assert.False(t, merged.RPMOStreeAvailable, "the merge was never given rpm-ostree data")
			assert.Empty(t, merged.RPMOStreeError, "no rpm-ostree data is not an rpm-ostree error")
			for name, deployment := range map[string]*Deployment{"booted": merged.Booted, "staged": merged.Staged, "rollback": merged.Rollback} {
				if deployment == nil {
					continue
				}
				assert.Empty(t, deployment.Version, "%s version has no source without rpm-ostree", name)
				assert.Empty(t, deployment.Checksum, "%s checksum has no source without rpm-ostree", name)
			}
		})
	}
}

// TestMergeHostImageBootcAndRPMOStree is the uCore-shaped case: both sources
// describe the same host, so every slot gains rpm-ostree's version and
// checksum while every field bootc owns -- image, digest, which slots exist,
// and soft-reboot eligibility -- comes through untouched.
func TestMergeHostImageBootcAndRPMOStree(t *testing.T) {
	bootc, err := ParseBootcStatus([]byte(bootcBootedStagedRollbackSoftReboot))
	require.NoError(t, err)
	supplement, err := ParseRPMOStreeStatus([]byte(rpmOStreeBootedStagedRollback))
	require.NoError(t, err)

	merged := MergeHostImage(bootc, supplement)

	require.NotNil(t, merged.Booted)
	assert.Equal(t, "quay.io/frostyard/snosi:stable", merged.Booted.Image, "bootc owns the image reference")
	assert.Equal(t, "sha256:1111111111111111111111111111111111111111111111111111111111111111", merged.Booted.Digest)
	assert.Equal(t, "41.20260701.0", merged.Booted.Version)
	assert.Equal(t, "aaaa111111111111111111111111111111111111111111111111111111111111", merged.Booted.Checksum)

	require.NotNil(t, merged.Staged, "bootc alone decides which slots exist")
	assert.Equal(t, "quay.io/frostyard/snosi:next", merged.Staged.Image)
	assert.Equal(t, "sha256:2222222222222222222222222222222222222222222222222222222222222222", merged.Staged.Digest)
	assert.Equal(t, "41.20260715.0", merged.Staged.Version)
	assert.Equal(t, "bbbb222222222222222222222222222222222222222222222222222222222222", merged.Staged.Checksum)

	require.NotNil(t, merged.Rollback, "rpm-ostree flags no rollback, so this slot is matched by bootc's digest")
	assert.Equal(t, "quay.io/frostyard/snosi:previous", merged.Rollback.Image)
	assert.Equal(t, "sha256:3333333333333333333333333333333333333333333333333333333333333333", merged.Rollback.Digest)
	assert.Equal(t, "41.20260610.0", merged.Rollback.Version)
	assert.Equal(t, "cccc333333333333333333333333333333333333333333333333333333333333", merged.Rollback.Checksum)

	require.NotNil(t, merged.SoftRebootCapable, "soft-reboot eligibility is bootc's and survives the merge")
	assert.True(t, *merged.SoftRebootCapable)
	assert.True(t, merged.BootcAvailable)
	assert.Empty(t, merged.BootcError)
	assert.False(t, merged.RPMOStreeAvailable, "recording rpm-ostree availability is the caller's job, not the merge's")
	assert.Empty(t, merged.RPMOStreeError)
}

// TestMergeHostImageConflictKeepsBootc is the precedence test the spec's
// round-3 clarification asks for. rpm-ostree describes the booted deployment
// as a different image than bootc does; bootc wins outright and the
// rpm-ostree value must not reach any field of the result -- neither by
// overwriting an authoritative field nor by having its version/checksum
// attached to a deployment it evidently does not describe.
func TestMergeHostImageConflictKeepsBootc(t *testing.T) {
	cases := []struct {
		name      string
		rpmOStree string
	}{
		{
			name: "digest disagrees",
			rpmOStree: `{"deployments": [{"booted": true,
			  "container-image-reference": "ostree-unverified-registry:quay.io/frostyard/snosi:stable",
			  "container-image-reference-digest": "sha256:9999999999999999999999999999999999999999999999999999999999999999",
			  "checksum": "dddd999999999999999999999999999999999999999999999999999999999999",
			  "version": "41.20260101.0"}]}`,
		},
		{
			name: "image reference disagrees, neither side reports a digest",
			rpmOStree: `{"deployments": [{"booted": true,
			  "container-image-reference": "ostree-unverified-registry:quay.io/someone-else/other:stable",
			  "checksum": "dddd999999999999999999999999999999999999999999999999999999999999",
			  "version": "41.20260101.0"}]}`,
		},
		{
			name: "both disagree",
			rpmOStree: `{"deployments": [{"booted": true,
			  "container-image-reference": "ostree-unverified-registry:quay.io/someone-else/other:stable",
			  "container-image-reference-digest": "sha256:9999999999999999999999999999999999999999999999999999999999999999",
			  "checksum": "dddd999999999999999999999999999999999999999999999999999999999999",
			  "version": "41.20260101.0"}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// The no-digest case needs a bootc payload without one too, so
			// the image reference is what the comparison actually falls to.
			bootcInput := bootcBootedOnly
			if testCase.name == "image reference disagrees, neither side reports a digest" {
				bootcInput = `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"quay.io/frostyard/snosi:stable"}}}}}`
			}
			bootc, err := ParseBootcStatus([]byte(bootcInput))
			require.NoError(t, err)
			supplement, err := ParseRPMOStreeStatus([]byte(testCase.rpmOStree))
			require.NoError(t, err)

			merged := MergeHostImage(bootc, supplement)

			require.NotNil(t, merged.Booted)
			assert.Equal(t, bootc.Booted.Image, merged.Booted.Image, "bootc is authoritative for the image reference")
			assert.Equal(t, bootc.Booted.Digest, merged.Booted.Digest, "bootc is authoritative for the digest")
			assert.Equal(t, bootc, merged, "a conflicting entry is dropped whole: nothing from it reaches any field")

			// Serialized rather than formatted with %+v: HostImageStatus is
			// a struct of pointers, so %+v would print addresses and the
			// no-leak assertion below would be vacuously true.
			encoded, err := json.Marshal(merged)
			require.NoError(t, err)
			rendered := string(encoded)
			for _, leaked := range []string{
				"sha256:9999999999999999999999999999999999999999999999999999999999999999",
				"quay.io/someone-else/other:stable",
				"dddd999999999999999999999999999999999999999999999999999999999999",
				"41.20260101.0",
			} {
				assert.NotContains(t, rendered, leaked, "no rpm-ostree value from a conflicting entry may appear anywhere in the merged result")
			}
		})
	}
}

// TestMergeHostImageMatchesRollbackByIdentityOnly guards the one slot
// rpm-ostree does not flag. An unflagged entry that is not the deployment
// bootc reports as the rollback must not have its detail attached to it just
// for being the only candidate left.
func TestMergeHostImageMatchesRollbackByIdentityOnly(t *testing.T) {
	bootc, err := ParseBootcStatus([]byte(bootcBootedStagedRollback))
	require.NoError(t, err)
	supplement, err := ParseRPMOStreeStatus([]byte(`{"deployments": [
	  {"booted": true,
	   "container-image-reference-digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
	   "checksum": "aaaa111111111111111111111111111111111111111111111111111111111111",
	   "version": "41.20260701.0"},
	  {"booted": false, "staged": false,
	   "container-image-reference-digest": "sha256:8888888888888888888888888888888888888888888888888888888888888888",
	   "checksum": "eeee888888888888888888888888888888888888888888888888888888888888",
	   "version": "41.20260505.0"}
	]}`))
	require.NoError(t, err)

	merged := MergeHostImage(bootc, supplement)

	require.NotNil(t, merged.Booted)
	assert.Equal(t, "41.20260701.0", merged.Booted.Version, "the booted entry still matches by role")
	require.NotNil(t, merged.Rollback)
	assert.Empty(t, merged.Rollback.Version, "an unflagged entry with a different digest is not this rollback")
	assert.Empty(t, merged.Rollback.Checksum)
	require.NotNil(t, merged.Staged)
	assert.Empty(t, merged.Staged.Version, "rpm-ostree flagged nothing staged")
}

// TestMergeHostImageComparesNormalizedImageReferences covers the case where
// identity has to fall back to the image reference: rpm-ostree spells it with
// the ostree transport it deployed through, bootc spells it bare, and the two
// describe the same deployment. Treating that spelling difference as a
// conflict would drop supplementary detail on every real rpm-ostree host whose
// bootc status omits a digest.
func TestMergeHostImageComparesNormalizedImageReferences(t *testing.T) {
	bootc, err := ParseBootcStatus([]byte(`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","status":{"booted":{"image":{"image":{"image":"quay.io/frostyard/snosi:stable","transport":"registry"}}}}}`))
	require.NoError(t, err)
	require.Empty(t, bootc.Booted.Digest, "this fixture exists to exercise the reference comparison")

	for _, reference := range []string{
		"quay.io/frostyard/snosi:stable",
		"ostree-unverified-registry:quay.io/frostyard/snosi:stable",
		"ostree-image-signed:docker://quay.io/frostyard/snosi:stable",
		"ostree-remote-image:frostyard:docker://quay.io/frostyard/snosi:stable",
	} {
		t.Run(reference, func(t *testing.T) {
			supplement, err := ParseRPMOStreeStatus([]byte(fmt.Sprintf(
				`{"deployments": [{"booted": true, "container-image-reference": %q, "checksum": "aaaa", "version": "41.20260701.0"}]}`, reference)))
			require.NoError(t, err)

			merged := MergeHostImage(bootc, supplement)

			require.NotNil(t, merged.Booted)
			assert.Equal(t, "quay.io/frostyard/snosi:stable", merged.Booted.Image, "bootc's spelling is the one reported")
			assert.Equal(t, "41.20260701.0", merged.Booted.Version)
			assert.Equal(t, "aaaa", merged.Booted.Checksum)
		})
	}
}

// TestMergeHostImageNeverInventsDeployments pins the other half of "bootc owns
// deployment identity": rpm-ostree can add detail to a slot bootc reported, but
// it can never conjure a slot bootc did not report -- including on a host where
// bootc failed entirely and the caller is about to record BootcError.
func TestMergeHostImageNeverInventsDeployments(t *testing.T) {
	supplement, err := ParseRPMOStreeStatus([]byte(rpmOStreeBootedStagedRollback))
	require.NoError(t, err)

	merged := MergeHostImage(HostImageStatus{BootcError: "bootc: exec failed"}, supplement)

	assert.Nil(t, merged.Booted, "rpm-ostree may not report a deployment bootc did not")
	assert.Nil(t, merged.Staged)
	assert.Nil(t, merged.Rollback)
	assert.False(t, merged.BootcAvailable)
	assert.Equal(t, "bootc: exec failed", merged.BootcError, "the caller's bootc failure record survives the merge")
	assert.False(t, merged.RPMOStreeAvailable)
	assert.Empty(t, merged.RPMOStreeError)
}

// TestMergeHostImageDoesNotMutateItsInputs proves the result shares no memory
// with either argument: HostImageStatus is a struct of pointers, so a merge
// that wrote through them would silently rewrite the caller's own bootc parse.
func TestMergeHostImageDoesNotMutateItsInputs(t *testing.T) {
	bootc, err := ParseBootcStatus([]byte(bootcBootedStagedRollbackSoftReboot))
	require.NoError(t, err)
	supplement, err := ParseRPMOStreeStatus([]byte(rpmOStreeBootedStagedRollback))
	require.NoError(t, err)

	merged := MergeHostImage(bootc, supplement)
	require.NotNil(t, merged.Booted)
	require.NotEmpty(t, merged.Booted.Version)

	assert.Empty(t, bootc.Booted.Version, "the merge must not write into the caller's bootc status")
	assert.Empty(t, bootc.Staged.Version)
	assert.Empty(t, bootc.Rollback.Version)

	merged.Booted.Image = "quay.io/frostyard/mutated:latest"
	*merged.SoftRebootCapable = false
	assert.Equal(t, "quay.io/frostyard/snosi:stable", bootc.Booted.Image)
	require.NotNil(t, bootc.SoftRebootCapable)
	assert.True(t, *bootc.SoftRebootCapable)
	assert.Equal(t, "41.20260701.0", supplement.Deployments[1].Version, "the supplement is read, never written")
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
