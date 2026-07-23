package maintenance

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bootcKind is the only document kind `bootc status --json` emits, and
// bootcAPIVersionPrefix is the stable prefix of its apiVersion
// (`org.containers.bootc/v1` today). Both are required discriminators so a
// payload that is valid JSON but is not a bootc host status -- an error
// object from another tool, an empty object, a stray array -- is reported as
// a parse error instead of decoding into a silently empty result. Neither may
// be omitted: bootc always emits both, so a document missing either one is not
// bootc host status output and must not be accepted by default. Only the
// apiVersion's *value* is matched loosely, by prefix, so a future schema
// revision (`org.containers.bootc/v2`) still parses as long as the fields this
// package reads keep their meaning.
const (
	bootcAPIVersionPrefix = "org.containers.bootc/"
	bootcKind             = "BootcHost"
)

// Deployment is one host-image deployment slot (booted, staged, or rollback)
// as reported by the host's image stack. Image is the container image
// reference the deployment was provisioned from and Digest is its manifest
// digest; either may be empty when the source reports a deployment that did
// not come from a container image.
type Deployment struct {
	Digest string `json:"digest,omitempty"`
	Image  string `json:"image,omitempty"`
}

// HostImageStatus is the read-only host-image picture Pilothouse reports: which
// deployments exist and what image identifies each. It is purely descriptive
// -- it carries no reboot-required posture (that stays State's job) and no
// lifecycle action of any kind.
//
// Booted is always non-nil on a successfully parsed status: a payload that
// reports no booted deployment is rejected as malformed rather than returned
// as an empty success. Staged and Rollback are nil when the source does not
// report that deployment slot, which is the ordinary case on a host with
// nothing pending and nothing to roll back to.
//
// SoftRebootCapable is a three-state value: non-nil true or
// false when the host's bootc exposes soft-reboot eligibility, and nil when it
// does not (unknown/not exposed -- an older bootc, never an error).
//
// BootcAvailable reports whether bootc data was successfully obtained; it is
// set by ParseBootcStatus on a successful parse. BootcError is not set by the
// parser: a caller that runs bootc records the failure there (see the callers
// added in later chunks) so a bootc-level failure degrades this one source
// rather than failing the whole report.
type HostImageStatus struct {
	BootcAvailable    bool        `json:"bootc_available"`
	BootcError        string      `json:"bootc_error,omitempty"`
	Booted            *Deployment `json:"booted,omitempty"`
	Rollback          *Deployment `json:"rollback,omitempty"`
	SoftRebootCapable *bool       `json:"soft_reboot_capable,omitempty"`
	Staged            *Deployment `json:"staged,omitempty"`
}

// bootcHost mirrors the subset of `bootc status --json` this package reads.
// Unknown fields are ignored on purpose: bootc adds keys between releases and
// none of them should turn a readable status into a parse failure.
// Status is a pointer so an absent or null `status` object stays
// distinguishable from one that is present but reports nothing.
type bootcHost struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Status     *bootcHostStatus `json:"status"`
}

type bootcHostStatus struct {
	Booted   *bootcBootEntry `json:"booted"`
	Rollback *bootcBootEntry `json:"rollback"`
	Staged   *bootcBootEntry `json:"staged"`
}

// bootcBootEntry mirrors bootc's `BootEntry`. SoftRebootCapable is a pointer
// rather than a plain bool so "the key is absent" (an older bootc that does not
// report eligibility at all) stays distinguishable from an explicit false.
type bootcBootEntry struct {
	Image             *bootcImageStatus `json:"image"`
	SoftRebootCapable *bool             `json:"softRebootCapable"`
}

type bootcImageStatus struct {
	Image       *bootcImageReference `json:"image"`
	ImageDigest string               `json:"imageDigest"`
}

type bootcImageReference struct {
	Image string `json:"image"`
}

// ParseBootcStatus decodes `bootc status --json` output into a
// HostImageStatus. It runs nothing: the caller obtains the bytes -- only ever
// by running `bootc status --json` through an injected command runner, never a
// second bootc subcommand -- and hands them here. No such caller exists yet.
//
// On success the returned status has BootcAvailable set to true and one
// Deployment per slot bootc reported. On a structurally malformed payload it
// returns a non-nil error together with a zero HostImageStatus
// (BootcAvailable false), never a partially populated result. Deciding how to
// surface that failure (as HostImageStatus.BootcError on an otherwise usable
// report) belongs to the caller.
//
// "Structurally malformed" covers both syntax and substance, because a
// confident but empty success is exactly as wrong as a wrong value:
//
//   - non-JSON, truncated JSON, or a JSON value that is not an object;
//   - a field of the wrong type;
//   - a missing or non-bootc discriminator -- both apiVersion and kind are
//     required, and omitting either is a failure rather than a bypass;
//   - a document that passes the discriminators but omits the substance the
//     caller asked for: no `status` object, or a `status` that reports no
//     booted deployment. bootc always reports what the host is running, so
//     their absence means the payload is not usable host-image status even
//     though it claims the right shape.
//
// Only the staged and rollback slots are genuinely optional -- a host with
// nothing staged and nothing to roll back to is an ordinary, healthy host --
// so their absence yields a nil Deployment, not an error.
func ParseBootcStatus(data []byte) (HostImageStatus, error) {
	var host bootcHost
	if err := json.Unmarshal(data, &host); err != nil {
		return HostImageStatus{}, fmt.Errorf("parse bootc status: %w", err)
	}
	if host.Kind != bootcKind {
		return HostImageStatus{}, fmt.Errorf("parse bootc status: unexpected kind %q, want %q", host.Kind, bootcKind)
	}
	if !strings.HasPrefix(host.APIVersion, bootcAPIVersionPrefix) {
		return HostImageStatus{}, fmt.Errorf("parse bootc status: unexpected apiVersion %q, want prefix %q", host.APIVersion, bootcAPIVersionPrefix)
	}
	if host.Status == nil {
		return HostImageStatus{}, fmt.Errorf("parse bootc status: document reports no status object")
	}
	if host.Status.Booted == nil {
		return HostImageStatus{}, fmt.Errorf("parse bootc status: status reports no booted deployment")
	}
	return HostImageStatus{
		BootcAvailable:    true,
		Booted:            host.Status.Booted.deployment(),
		Rollback:          host.Status.Rollback.deployment(),
		SoftRebootCapable: host.Status.softRebootCapable(),
		Staged:            host.Status.Staged.deployment(),
	}, nil
}

// deployment converts one bootc boot entry into a Deployment, returning nil
// for a slot bootc did not report. An entry that exists but carries no image
// (a deployment not provisioned from a container image) still yields a
// non-nil Deployment: slot presence and image identity are independent facts.
func (e *bootcBootEntry) deployment() *Deployment {
	if e == nil {
		return nil
	}
	deployment := &Deployment{}
	if e.Image != nil {
		deployment.Digest = e.Image.ImageDigest
		if e.Image.Image != nil {
			deployment.Image = e.Image.Image.Image
		}
	}
	return deployment
}

// softRebootCapable returns a copy of the entry's eligibility flag (so the
// returned pointer never aliases decoded wire state), or nil for an absent
// entry or an entry that does not report the key.
func (e *bootcBootEntry) softRebootCapable() *bool {
	if e == nil || e.SoftRebootCapable == nil {
		return nil
	}
	value := *e.SoftRebootCapable
	return &value
}

// softRebootCapable resolves soft-reboot eligibility.
//
// The key was confirmed against bootc's published schema
// (github.com/bootc-dev/bootc, `crates/lib/src/spec.rs`): `BootEntry` carries
// `soft_reboot_capable: bool` under `#[serde(rename_all = "camelCase")]`, so it
// appears as `softRebootCapable` on a boot entry -- documented there as "true
// if (relative to the booted system) this is a possible target for a soft
// reboot". `HostStatus` itself has no such field, so nothing outside a boot
// entry is read.
//
// The staged entry is preferred: it is the pending deployment a soft reboot
// would activate, and it is the same fact the reboot-required posture keys on.
// With nothing staged, the booted entry's flag is used as the host-level
// answer to "are soft reboots possible here at all" -- upstream computes the
// flag per deployment via the same `has_soft_reboot_capability(sysroot,
// deployment)` call for every slot the JSON status reports, booted included,
// so reading it there is not a reinterpretation of a staged-only field.
//
// A bootc new enough to have the field always emits it (it is a plain `bool`
// with no `skip_serializing_if`, so it serializes even when false). Absence
// therefore means a bootc predating soft-reboot support, which yields nil
// here -- unknown, never an error and never false.
func (s bootcHostStatus) softRebootCapable() *bool {
	if capable := s.Staged.softRebootCapable(); capable != nil {
		return capable
	}
	return s.Booted.softRebootCapable()
}
