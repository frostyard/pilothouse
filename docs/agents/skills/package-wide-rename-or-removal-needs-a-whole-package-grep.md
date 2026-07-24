# A package-wide rename or "no code may use X" ban needs a whole-package grep, not just the files the chunk description names

**When it applies:** A chunk's acceptance criteria state a package-wide
invariant — "rename `Feature` to `Extension` throughout the package," "no
non-test code in package P may import `os/exec` / call `CommandRunner`,"
or any similarly-scoped ban/rename that applies to *every* file in a
package — and the chunk's own title or plan text also lists specific files
expected to change (e.g. "rewrite module.go/helpers.go/views.templ"). It's
tempting to treat that file list as the scope of work and declare the
invariant satisfied once those named files are clean.

**What to do:** The file list in a chunk's title or plan text describes the
*primary* work, not the full verification scope of a package-wide
invariant. Before declaring such a chunk done, `grep -rn` the *entire*
package (all non-test `.go`/`.templ` files, including ones the chunk
description never mentions) for: the old identifier being renamed (as a
type name, a field name, a method name, and a return type — a rename that
fixes `type Feature` but leaves `Manager.List(ctx) []Feature` is only
half done), and the banned import/call being removed. Exported struct
fields are the easiest miss — a field like `AvailableUpdate.Feature` keeps
the old vocabulary alive in the public shape and in every view that renders
it (`update.Feature` in a table column) even after every local variable and
function in the "obviously relevant" files has been renamed. A file the
chunk didn't plan to touch (e.g. `manager.go`, when the plan named
`module.go`/`helpers.go`/`views.templ`) is exactly where a stale
type/import survives, because nobody expected to open it.

**Learned from:** mill run for issue #52, chunk 2 (sysext package
Feature→Extension rename + drop CommandRunner/os/exec from non-test code).
Round 1 raised two `reject`-severity objections at once: `manager.go` still
imported `os/exec` and used `CommandRunner`/`runner.Run` in production
methods, and `manager.go` still defined `type Feature` with
`Manager.List(ctx) []Feature`/`featureFor(ctx) Feature` — both because the
chunk's rewrite had been scoped to `module.go`/`helpers.go`/`views.templ`
as the plan described, leaving `manager.go` untouched. Round 3, after both
of those were fixed, found a third survivor of the same rename: the
exported `AvailableUpdate` model in `state.go` still exposed the owning
extension as `Feature`, and the new "Available updates" table rendered an
"Extension" column from `update.Feature` — the old vocabulary reached all
the way into the public query shape and the newest view path, three rounds
into the same rename.
