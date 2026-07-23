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
//
// Image and Digest are bootc's to report: ParseBootcStatus sets them and
// MergeHostImage never overwrites them. Version and Checksum are the opposite
// -- purely supplementary rpm-ostree detail (the human-readable version string
// and the ostree commit checksum, neither of which bootc's status reports), so
// ParseBootcStatus always leaves them empty and only MergeHostImage ever fills
// them in. Both stay empty on a host without rpm-ostree.
type Deployment struct {
	Checksum string `json:"checksum,omitempty"`
	Digest   string `json:"digest,omitempty"`
	Image    string `json:"image,omitempty"`
	Version  string `json:"version,omitempty"`
}

// clone returns an independent copy of the deployment (nil for a nil
// receiver), so merging supplementary detail into a slot can never write
// through a pointer the caller still holds.
func (d *Deployment) clone() *Deployment {
	if d == nil {
		return nil
	}
	copied := *d
	return &copied
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
// parser: the caller that runs bootc (HostImageManager.Status) records the
// failure there, so a bootc-level failure degrades this one source rather
// than failing the whole report.
//
// RPMOStreeAvailable and RPMOStreeError are the exact symmetric pair for the
// second source, so a report can say "bootc answered, rpm-ostree did not" (or
// the reverse) instead of collapsing both into one availability bit. Nothing
// in this file sets either: ParseRPMOStreeStatus returns a supplement rather
// than a status, and MergeHostImage deliberately leaves both at their zero
// value (see its doc comment). A caller that runs `rpm-ostree status --json`
// records the outcome there, exactly as it does for bootc -- today that
// caller is HostImageManager.Status in hostimage_manager.go, which is also
// the only thing that sets BootcError.
type HostImageStatus struct {
	BootcAvailable     bool        `json:"bootc_available"`
	BootcError         string      `json:"bootc_error,omitempty"`
	Booted             *Deployment `json:"booted,omitempty"`
	RPMOStreeAvailable bool        `json:"rpm_ostree_available"`
	RPMOStreeError     string      `json:"rpm_ostree_error,omitempty"`
	Rollback           *Deployment `json:"rollback,omitempty"`
	SoftRebootCapable  *bool       `json:"soft_reboot_capable,omitempty"`
	Staged             *Deployment `json:"staged,omitempty"`
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
// second bootc subcommand -- and hands them here. That caller is
// HostImageManager.Status in hostimage_manager.go.
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

// copyBool returns an independent copy of a three-state flag, preserving nil.
func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// rpmOStreeSupplement is everything this package takes from
// `rpm-ostree status --json`: for each deployment rpm-ostree reports, only the
// detail bootc does not provide. It is intentionally not a HostImageStatus and
// intentionally unexported -- rpm-ostree is the supplementary source, so its
// output can never stand on its own as a host-image report, and giving it a
// separate shape makes that impossible to do by accident.
//
// A zero rpmOStreeSupplement (no deployments) is the correct value for a host
// where rpm-ostree was never run, ran and reported nothing, or failed: in all
// three cases there is simply no supplementary detail to attach. Telling
// "failed" apart from "reported nothing" is the caller's job, via the error
// ParseRPMOStreeStatus returns and the HostImageStatus.RPMOStreeAvailable /
// RPMOStreeError fields.
type rpmOStreeSupplement struct {
	Deployments []rpmOStreeDeployment
}

// rpmOStreeDeployment is one entry of rpm-ostree's deployment list.
//
// Version and Checksum are the supplementary payload -- the only fields that
// are ever merged onto a Deployment. Image, Digest, Booted, and Staged are
// carried solely so an entry can be matched to the deployment bootc already
// identified, and are never merged into the result: bootc owns deployment
// identity outright.
type rpmOStreeDeployment struct {
	Booted   bool
	Checksum string
	Digest   string
	Image    string
	Staged   bool
	Version  string
}

// rpmOStreeStatus mirrors the subset of `rpm-ostree status --json` this
// package reads. Unlike bootc's document it carries no apiVersion/kind
// discriminator, so the presence of the `deployments` array is the only
// available proof that the payload is rpm-ostree status output at all; the
// field is therefore a pointer, keeping "the key is absent" (not rpm-ostree
// status output, or an error object from some other tool that happens to be
// valid JSON) distinguishable from an empty list.
type rpmOStreeStatus struct {
	Deployments *[]rpmOStreeStatusDeployment `json:"deployments"`
}

// rpmOStreeStatusDeployment mirrors one element of that array. Unknown keys
// are ignored: rpm-ostree reports far more per deployment (origin, osname,
// pinned, package layering, timestamps) and none of it should turn a readable
// status into a parse failure.
type rpmOStreeStatusDeployment struct {
	Booted   bool   `json:"booted"`
	Checksum string `json:"checksum"`
	Digest   string `json:"container-image-reference-digest"`
	Image    string `json:"container-image-reference"`
	Staged   bool   `json:"staged"`
	Version  string `json:"version"`
}

// ParseRPMOStreeStatus decodes `rpm-ostree status --json` output into the
// supplementary detail MergeHostImage can attach to a bootc-sourced report. It
// runs nothing: the caller obtains the bytes -- only ever by running
// `rpm-ostree status --json` through an injected command runner -- and hands
// them here. That caller is HostImageManager.Status in
// hostimage_manager.go.
//
// A structurally malformed payload returns a non-nil error and a zero
// supplement. That failure is deliberately distinct from a successful parse of
// a document whose deployment list is empty, which returns no error and a
// supplement with no deployments: the first means rpm-ostree ran but its
// output could not be read, the second means it read fine and had nothing to
// add. The caller needs that distinction to decide whether to record
// RPMOStreeError.
//
// "Structurally malformed" covers, as for bootc, both syntax and substance:
// non-JSON, truncated JSON, a JSON value that is not an object, a field of the
// wrong type, or a document with no `deployments` array at all. The last is
// what stands in for bootc's apiVersion/kind check -- rpm-ostree's status
// document has no discriminator, and always emits `deployments`, so a payload
// without it is not rpm-ostree status output and must not decode into a
// confident, empty success.
func ParseRPMOStreeStatus(data []byte) (rpmOStreeSupplement, error) {
	var status rpmOStreeStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return rpmOStreeSupplement{}, fmt.Errorf("parse rpm-ostree status: %w", err)
	}
	if status.Deployments == nil {
		return rpmOStreeSupplement{}, fmt.Errorf("parse rpm-ostree status: document reports no deployments array")
	}
	entries := *status.Deployments
	if len(entries) == 0 {
		return rpmOStreeSupplement{}, nil
	}
	supplement := rpmOStreeSupplement{Deployments: make([]rpmOStreeDeployment, 0, len(entries))}
	for _, entry := range entries {
		// The wire and domain shapes carry the same fields in the same order
		// and differ only in their JSON tags, so the conversion is exact; the
		// two types stay separate because only one of them is wire-shaped.
		supplement.Deployments = append(supplement.Deployments, rpmOStreeDeployment(entry))
	}
	return supplement, nil
}

// MergeHostImage combines a bootc-sourced host-image status with rpm-ostree's
// supplementary detail. bootc is authoritative: every deployment slot that
// exists in the result exists because bootc reported it, and Image, Digest,
// SoftRebootCapable, and the presence of booted/staged/rollback come from
// bootc alone. rpm-ostree can only ever add Version and Checksum to a slot
// bootc already identified -- it can never add a slot, remove one, rename one,
// or change what image a slot runs. A supplement handed to a status with no
// bootc deployments therefore merges to that same empty status.
//
// Matching is by role where rpm-ostree reports one (it flags the booted and
// staged deployments itself) and by identity otherwise, which is how the
// rollback slot is matched: rpm-ostree has no rollback flag, so the only
// honest match for that slot is an entry rpm-ostree does not flag at all whose
// digest (or image reference) is the one bootc already reported. Each
// rpm-ostree entry is attached to at most one slot.
//
// On conflict -- a role-matched entry whose digest, or whose image reference
// when neither side reports a digest, disagrees with what bootc says that slot
// is running -- bootc wins outright: the entry is dropped whole. Its version
// and checksum are not attached either, because a deployment rpm-ostree
// describes differently from bootc is not evidently the same deployment, and
// attaching its detail would smuggle a second source of truth into the slot
// through the back door. The failure direction is always less detail, never
// wrong detail.
//
// The returned status shares no memory with either argument, so merging never
// mutates the caller's values.
//
// RPMOStreeAvailable and RPMOStreeError are left at their zero value, and an
// incoming value for either is not carried over. MergeHostImage only ever
// receives an already-successfully-parsed supplement, so it has no way to know
// whether rpm-ostree ran, failed, or was never attempted -- the caller that
// runs the command owns those two fields and sets them after merging, exactly
// as it does BootcAvailable/BootcError. Returning them zero keeps a successful
// merge from being mistaken for evidence about rpm-ostree's availability.
func MergeHostImage(bootc HostImageStatus, rpmOstree rpmOStreeSupplement) HostImageStatus {
	merged := HostImageStatus{
		BootcAvailable:    bootc.BootcAvailable,
		BootcError:        bootc.BootcError,
		Booted:            bootc.Booted.clone(),
		Rollback:          bootc.Rollback.clone(),
		SoftRebootCapable: copyBool(bootc.SoftRebootCapable),
		Staged:            bootc.Staged.clone(),
	}
	pool := rpmOStreePool{claimed: make([]bool, len(rpmOstree.Deployments)), entries: rpmOstree.Deployments}
	if merged.Booted != nil {
		supplementDeployment(merged.Booted, pool.take(func(entry rpmOStreeDeployment) bool {
			return entry.Booted
		}))
	}
	if merged.Staged != nil {
		supplementDeployment(merged.Staged, pool.take(func(entry rpmOStreeDeployment) bool {
			return entry.Staged
		}))
	}
	if merged.Rollback != nil {
		rollback := merged.Rollback
		supplementDeployment(rollback, pool.take(func(entry rpmOStreeDeployment) bool {
			return !entry.Booted && !entry.Staged && compareIdentity(rollback, entry) == identityMatch
		}))
	}
	return merged
}

// rpmOStreePool hands each rpm-ostree entry to at most one deployment slot, so
// a malformed list (one entry flagged both booted and staged, say) cannot have
// its detail copied into two slots at once.
type rpmOStreePool struct {
	claimed []bool
	entries []rpmOStreeDeployment
}

// take claims and returns the first unclaimed entry the predicate accepts, or
// nil when rpm-ostree reports no such deployment.
func (p *rpmOStreePool) take(matches func(rpmOStreeDeployment) bool) *rpmOStreeDeployment {
	for index := range p.entries {
		if p.claimed[index] || !matches(p.entries[index]) {
			continue
		}
		p.claimed[index] = true
		return &p.entries[index]
	}
	return nil
}

// supplementDeployment attaches rpm-ostree's version and checksum to a
// bootc-identified deployment. It writes only fields bootc left empty, and
// writes nothing at all when the entry's identity conflicts with bootc's.
func supplementDeployment(deployment *Deployment, entry *rpmOStreeDeployment) {
	if deployment == nil || entry == nil {
		return
	}
	if compareIdentity(deployment, *entry) == identityConflict {
		return
	}
	if deployment.Version == "" {
		deployment.Version = entry.Version
	}
	if deployment.Checksum == "" {
		deployment.Checksum = entry.Checksum
	}
}

// identityVerdict is the result of comparing what bootc and rpm-ostree each
// say a deployment is.
type identityVerdict int

const (
	// identityUnknown means the two sources report nothing comparable, so
	// neither agreement nor disagreement can be established.
	identityUnknown identityVerdict = iota
	// identityMatch means they name the same deployment.
	identityMatch
	// identityConflict means they name different deployments.
	identityConflict
)

// compareIdentity compares a bootc-reported deployment with an rpm-ostree
// entry. The manifest digest is preferred: it is the strongest identity either
// source reports and is spelled identically by both. Image references are only
// compared when a digest is missing on either side, because the two tools
// spell the same reference differently (see normalizeImageRef).
func compareIdentity(deployment *Deployment, entry rpmOStreeDeployment) identityVerdict {
	if deployment.Digest != "" && entry.Digest != "" {
		if deployment.Digest == entry.Digest {
			return identityMatch
		}
		return identityConflict
	}
	if deployment.Image != "" && entry.Image != "" {
		if normalizeImageRef(deployment.Image) == normalizeImageRef(entry.Image) {
			return identityMatch
		}
		return identityConflict
	}
	return identityUnknown
}

// normalizeImageRef strips the ostree transport decoration rpm-ostree puts in
// front of a container image reference so it can be compared with the bare
// reference bootc reports. rpm-ostree's `container-image-reference` carries the
// ostree transport it was deployed through -- forms such as
// `ostree-unverified-registry:quay.io/org/image:tag`,
// `ostree-image-signed:docker://quay.io/org/image:tag`, or
// `ostree-remote-image:remote:docker://quay.io/org/image:tag` -- while bootc
// reports `quay.io/org/image:tag` with the transport in a separate field. A
// plain reference passes through unchanged.
//
// The normalization is deliberately conservative rather than exhaustive: a
// decoration it does not recognize simply leaves the two spellings unequal,
// which reads as a conflict and drops supplementary detail. Losing a version
// string is the acceptable failure; attributing one deployment's detail to
// another is not.
func normalizeImageRef(reference string) string {
	if strings.HasPrefix(reference, "ostree-") {
		if _, rest, found := strings.Cut(reference, ":"); found {
			reference = rest
		}
	}
	if _, rest, found := strings.Cut(reference, "docker://"); found {
		reference = rest
	}
	return reference
}
