# Automatic-update reporting

Pilothouse reports how a host's automatic-update mechanisms are configured. It
never invokes, enables, disables, triggers, or reconfigures them: everything on
this page is read-only reporting, and no broker action exists — or will exist —
to change any of it.

## What exists today

This document currently covers exactly what these two files contain:

- `internal/modules/maintenance/autoupdate.go` — the response domain types
  `AutoUpdateStatus`, `BootcAutoUpdate`, and `RPMOStreeAutoUpdate`, plus
  `NormalizeBootcAutoUpdatePolicy`, a pure, zero-I/O classifier for bootc's
  automatic-update policy; and
- `internal/modules/maintenance/autoupdate_rpmostree.go` — the `RPMOStreePolicy*`
  vocabulary and `ParseRPMOStreeAutomaticUpdatePolicy`, a pure, zero-I/O parser
  that maps the *content* of rpm-ostree's daemon configuration file to a
  normalized policy string.

There is no broker query, no daemon-side manager, and no web surface for
automatic-update status yet. Neither normalizer has a production caller in the
tree at this commit: the code that reads live systemd state and feeds
`NormalizeBootcAutoUpdatePolicy` its drop-in path lists, and the code that reads
`/etc/rpm-ostreed.conf` off disk and hands the bytes to
`ParseRPMOStreeAutomaticUpdatePolicy`, both land in a later change. Nothing
populates `BootcAutoUpdate.Policy` or `RPMOStreeAutoUpdate.Policy` on a real
host yet — both normalizers are reachable only from their tests.

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
(see the spec's "normalize per updater — do not force a shared enum"). The two
vocabularies overlap only in `custom/unknown`; bootc's `apply` and rpm-ostree's
`apply` are a coincidence of spelling between two closed enums, not a shared
value, and bootc's `stage-only` is not rpm-ostree's `stage`:

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

## rpm-ostree policy: the value mapping

`ParseRPMOStreeAutomaticUpdatePolicy(config []byte) string` takes the *content*
of rpm-ostree's daemon configuration file — bytes the caller has already read —
and returns exactly one of five values. It performs no `os.ReadFile` and no
other I/O: `internal/modules/maintenance/autoupdate_rpmostree.go` imports
`strings` and nothing else, and a mechanical test
(`TestAutoUpdateRPMOStreeReadsNothing`) pins that import allowlist, so the file
cannot open a file, run `rpm-ostree`, or reach a daemon. A second mechanical
test (`TestAutoUpdateRPMOStreeNeverReferencesTheUnitStartCommand`) enforces the
spec's ban on deriving policy from a unit's start command line, the same way
`autoupdate.go`'s does.

| `[Daemon] AutomaticUpdatePolicy=` value | normalized `policy` |
| --- | --- |
| `none` | `none` |
| `off` | `none` |
| `check` | `check` |
| `stage` | `stage` |
| `ex-stage` | `stage` |
| `apply` | `apply` |
| any other value (e.g. `bogus`, `None`, empty) | `custom/unknown` |
| key absent, key outside `[Daemon]`, or empty/nil input | `custom/unknown` |

### Why a config reader and not `rpm-ostree status --json`

The spec offers two sources: "`AutomaticUpdatePolicy` via `rpm-ostree status
--json` (or a stable config reader)." The first does not exist. In upstream
rpm-ostree, `src/app/rpmostree-builtin-status.cxx`'s JSON builder adds exactly
four top-level members — `deployments`, `transaction`, `cached-update`, and
`update-driver`. `AutomaticUpdatePolicy` is not among them, and never enters the
JSON path at all: it is surfaced only by the *text-mode* status printer
(`print_daemon_state`), which reads it as a live D-Bus property on the Sysroot
proxy via `rpmostree_sysroot_get_automatic_update_policy`. So the parenthetical
"(or a stable config reader)" branch is the only implementable path, and
Pilothouse takes it — rather than adding a second D-Bus destination
(`org.projectatomic.rpmostree1`) to query the property live, which would be new
architectural surface this repo has no precedent for. Today it only ever shells
out to `rpm-ostree status --json` and never touches rpm-ostree's D-Bus API.

### Why a single-file line scan is sufficient

`src/daemon/rpmostreed-daemon.cxx` defines its configuration path as one fixed
macro:

```c
#define RPMOSTREED_CONF SYSCONFDIR "/rpm-ostreed.conf"
```

(i.e. `/etc/rpm-ostreed.conf`), and loads it with a single
`g_key_file_load_from_file (keyfile, RPMOSTREED_CONF, ...)` call. There is no
`conf.d` drop-in directory and no fragment merging anywhere in that loader, and
the policy is read from one group and one key — `DAEMON_CONFIG_GROUP "Daemon"`
and `AutomaticUpdatePolicy`. A hand-rolled `[Daemon] AutomaticUpdatePolicy=`
line scan therefore needs no drop-in-merge logic to be complete. It mirrors the
`/etc/os-release` line scanner `SystemManager.osVersion()` already uses in
`internal/modules/maintenance/manager.go` rather than adding an INI-parsing
dependency.

The scan follows the key-file behavior rpm-ostree inherits from GLib for the
small surface a one-key lookup can meet: blank lines and `#`-comments are
skipped, `[Group]` headers switch the active group (and switch it back on, so a
`[Daemon]` group after another group is still found), whitespace around the key
name and the value is ignored, the split is on the first `=` only, keys
appearing before any group header belong to no group and are ignored, and a key
repeated inside the group takes its last spelling — matching GLib's parser,
which replaces the earlier entry in its lookup map.

### Where the aliases come from

The value mapping is rpm-ostree's own, not an invention here:
`rpmostree_str_to_auto_update_policy` in `src/libpriv/rpmostree-util.cxx` maps
`"none"` **or** `"off"` to `RPMOSTREED_AUTOMATIC_UPDATE_POLICY_NONE`, `"check"`
to `..._CHECK`, `"stage"` **or** `"ex-stage"` (the backwards-compatibility
spelling) to `..._STAGE`, and `"apply"` to `..._APPLY`; anything else is thrown
as `"Invalid value for AutomaticUpdatePolicy: '%s'"`. Those two alias pairs —
`none`/`off` and `stage`/`ex-stage` — are the only reason this mapping is not a
pass-through. The comparisons use `g_str_equal`, so matching is exact and
case-sensitive: `None` is not `none` to the daemon, and it is not to Pilothouse
either.

### Why absent normalizes to `custom/unknown`, not `none`

rpm-ostree's own default when the key is absent is `none`. Pilothouse
deliberately does not adopt that default, per the spec's explicit instruction:
an absent file, an absent or misplaced key, an unreadable file, and an
unrecognized value all normalize to `custom/unknown`. The reason is that
Pilothouse cannot be certain it observed the value the running daemon actually
loaded — the daemon reads the file once at its own startup, and a report of
"automatic updates are off" carries more weight for a reader than "I could not
tell." Reporting `custom/unknown` says the weaker, true thing.

Because empty and nil input map to `custom/unknown` like any other unreadable
state, the caller that lands in a later change can pass the result of a failed
read straight through without a special case.

## Testing

Both normalizers are pure, so they are tested with Go literals — no fixture
images, no systemd session, and no `/etc` on the test host are involved.
`internal/modules/maintenance/autoupdate_test.go`
covers the full drop-in presence matrix (neither unit, service only, timer only,
both, plus the nil-versus-empty and multi-path spellings), the JSON wire shape of
all three types, the not-configured zero value, the `stage-only` round trip, and
two mechanical guards: an import allowlist proving the file can reach neither a
command nor the host, and the source-text ban on the unit start-command property.

`internal/modules/maintenance/autoupdate_rpmostree_test.go` covers the full
mapping matrix above from both directions — a table over every input spelling
(each native value and both alias pairs; an unrecognized value; a file with no
`AutomaticUpdatePolicy` key; empty and nil input; an empty value; a
wrong-case value; the key outside `[Daemon]`; the key before any group header; a
`[Daemon]` group following another group; comments; whitespace around the
separator; CRLF endings; a missing trailing newline; a repeated key; a
prefix-similar key; and a realistic whole configuration file), plus a
mapping-direction cross-check that each of rpm-ostree's six accepted spellings
reaches its constant. Every case also asserts the result is inside the
five-value vocabulary. Beyond that it pins input immutability, the closed
vocabulary's distinctness and its separation from bootc's, and the same two
mechanical guards — the `strings`-only import allowlist that proves the file
performs no `os.ReadFile` or other I/O, and the start-command source-text ban.
