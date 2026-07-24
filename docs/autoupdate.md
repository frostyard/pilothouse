# Automatic-update reporting

Pilothouse reports how a host's automatic-update mechanisms are configured. It
never invokes, enables, disables, triggers, or reconfigures them: everything on
this page is read-only reporting, and no broker action exists — or will exist —
to change any of it.

## What exists today

This document currently covers exactly what
`internal/modules/maintenance/autoupdate.go` contains:

- the response domain types `AutoUpdateStatus`, `BootcAutoUpdate`, and
  `RPMOStreeAutoUpdate`; and
- `NormalizeBootcAutoUpdatePolicy`, a pure, zero-I/O classifier for bootc's
  automatic-update policy.

There is no broker query, no daemon-side manager, and no web surface for
automatic-update status yet. `NormalizeBootcAutoUpdatePolicy` has no production
caller in the tree at this commit; the code that reads live systemd state and
feeds it the drop-in path lists lands in a later change. The rpm-ostree policy
vocabulary and its normalizer are likewise not part of this change —
`RPMOStreeAutoUpdate.Policy` is defined here, but nothing populates it yet, and
its value mapping will be documented alongside the reader that produces it.

## Response schema

`AutoUpdateStatus` carries one availability boolean plus one optional payload
pointer per updater, following the convention `HostImageStatus` uses in
`internal/modules/maintenance/hostimage.go` (a per-source availability bool
beside its detail) rather than copying that type's flat shape:

| Field | JSON | Meaning |
| --- | --- | --- |
| `BootcConfigured` | `bootc_configured` | the bootc updater units are present on the host |
| `Bootc` | `bootc` | bootc payload; absent unless configured |
| `RPMOStreeConfigured` | `rpm_ostree_configured` | the rpm-ostree updater units are present |
| `RPMOStree` | `rpm_ostree` | rpm-ostree payload; absent unless configured |

The zero value — both booleans `false`, both pointers `nil` — is the canonical
"no automatic updater is configured" report. That is a valid answer on an
image-based host, not an error and not an empty response.

`BootcAutoUpdate` and `RPMOStreeAutoUpdate` carry the same field set but are
deliberately separate types, because their `Policy` vocabularies are separate
(see the spec's "normalize per updater — do not force a shared enum"):

| Field | JSON | Meaning |
| --- | --- | --- |
| `TimerActiveState` | `timer_active_state` | systemd `ActiveState` of the updater's timer unit |
| `TimerUnitFileState` | `timer_unit_file_state` | systemd `UnitFileState` of the timer unit |
| `NextTrigger` | `next_trigger` | the timer's next scheduled run; zero time when there is none |
| `ServiceActiveState` | `service_active_state` | systemd `ActiveState` of the updater's service unit |
| `ServiceResult` | `service_result` | systemd `Result` of the service unit's last run |
| `Policy` | `policy` | normalized policy string (bootc's vocabulary is below) |
| `TimerDropinsPresent` | `timer_dropins_present` | the timer unit has one or more effective drop-ins |
| `ServiceDropinsPresent` | `service_dropins_present` | the service unit has one or more effective drop-ins |

Drop-in presence is two independent booleans, never one merged flag and never a
path list: a drop-in on the timer changes *when* the updater runs, while a
drop-in on the service changes *what it does*, and a reader needs to tell those
apart. The three state fields — `TimerActiveState`, `TimerUnitFileState`, and
`ServiceResult` — carry systemd's own `ActiveState`, `UnitFileState`, and
`Result` property values, the same vocabulary `internal/modules/backups`'s
`Timer` already reports (`active_state`, `unit_file_state`, `result`), so both
surfaces name the same systemd facts the same way.

Absent sub-values inside a configured payload are the zero value rather than
omitted, so a configured updater always reports the same field set.

## Bootc policy: the decision table

`NormalizeBootcAutoUpdatePolicy(serviceDropInPaths, timerDropInPaths []string)`
returns `(policy string, serviceDropinsPresent, timerDropinsPresent bool)`.

Its **only** inputs are the drop-in *paths* systemd reports for
`bootc-fetch-apply-updates.service` and `bootc-fetch-apply-updates.timer`. It
never opens a drop-in file, and it never reads the units' start command line —
the spec forbids deriving policy from it, and a mechanical test
(`TestAutoUpdateNeverReferencesTheUnitStartCommand`) fails if the property's
name appears anywhere in `autoupdate.go`, comments included.

| `bootc-fetch-apply-updates.service` drop-ins | `bootc-fetch-apply-updates.timer` drop-ins | `service_dropins_present` | `timer_dropins_present` | `policy` |
| --- | --- | --- | --- | --- |
| none | none | `false` | `false` | `apply` |
| one or more | none | `true` | `false` | `custom/unknown` |
| none | one or more | `false` | `true` | `custom/unknown` |
| one or more | one or more | `true` | `true` | `custom/unknown` |

The two booleans are exactly `len(slice) > 0` for their own unit, reported
independently. A `nil` slice and an empty slice mean the same thing: no
drop-ins.

The policy vocabulary is closed and holds exactly three values: `apply`,
`stage-only`, `custom/unknown`.

### Why "no drop-ins" means `apply`

Upstream bootc ships `bootc-fetch-apply-updates.service` with a single
one-shot start command:

```ini
ExecStart=/usr/bin/bootc upgrade --apply --quiet
```

(`systemd/bootc-fetch-apply-updates.service` in `github.com/bootc-dev/bootc`.)
`bootc-fetch-apply-updates.service.5.md` — bootc's man page for that unit —
documents the update process as three steps that "can be decoupled; they are:
`bootc upgrade --check`, `bootc upgrade`, `bootc upgrade --apply`", i.e. check
only, fetch-and-stage, and fetch-stage-and-apply. Those decoupled verbs are
where this enum's meanings come from: `apply` is the shipped unit's behavior,
and `stage-only` is the middle verb's.

So when neither unit carries an effective drop-in, nothing local has overridden
the units, and the host is provably running the shipped fetch-and-apply
default. That conclusion is drawn from the *absence* of any override, not from
reading the shipped unit's contents — which is why it does not violate the "never
infer from the unit's start command" rule.

Conversely, once any drop-in exists on either unit, an administrator has
changed something, and what they changed cannot be determined from a filename.
Pilothouse reports `custom/unknown` rather than guessing.

### `stage-only` is defined but the classifier cannot produce it

**Read this literally: `stage-only` is a value the classifier never returns
today.** It is defined in Go, covered by a JSON round-trip test
(`TestBootcAutoUpdateStageOnlyPolicyIsRepresentable`), and representable on the
wire — and none of that means any real host can currently be classified as
`stage-only`. "Defined" here does not mean "produced." No input to
`NormalizeBootcAutoUpdatePolicy` yields it; the decision table above is
complete, and `stage-only` appears in none of its rows.

The reason is a genuine gap upstream, not an omission here. As of this writing,
bootc exposes **no documented non-`ExecStart` systemd property and no drop-in
filename convention** that signals the apply-versus-stage-only distinction. The
man page's decoupled verbs are switched by changing which `bootc upgrade`
invocation the unit runs — that is, by editing the unit's start command, which
is precisely the signal this classifier is forbidden to read. There is no
`Environment=` contract, no documented conditional, and no reserved drop-in
name that a properties-and-paths-only reader could key on.

Given that, the honest options were:

1. invent a "recognized drop-in filename" convention no real deployment
   follows, producing a classifier that looks more capable than it is; or
2. keep `stage-only` as a reserved, tested, wire-representable value and say
   plainly that nothing produces it yet.

Pilothouse takes option 2. If a future bootc release ships a documented,
non-command-line signal for staging, the classifier's drop-in path handling is
the extension point: a recognized-path check would go in front of the generic
"any drop-in ⇒ `custom/unknown`" branch, and this table and this section would
gain the new row. Until then, the only two policies a real host can be assigned
are `apply` and `custom/unknown`.

## Testing

The classifier is pure, so it is tested with Go literals — no fixture images and
no systemd session are involved. `internal/modules/maintenance/autoupdate_test.go`
covers the full drop-in presence matrix (neither unit, service only, timer only,
both, plus the nil-versus-empty and multi-path spellings), the JSON wire shape of
all three types, the not-configured zero value, the `stage-only` round trip, and
two mechanical guards: an import allowlist proving the file can reach neither a
command nor the host, and the source-text ban on the unit start-command property.
