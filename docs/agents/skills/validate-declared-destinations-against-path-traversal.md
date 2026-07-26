# Validate declared destination paths against `..` traversal before staging them onto a real filesystem

**When it applies:** Writing or reviewing code that takes a
caller-declared or archive-declared relative path (a fixture `Spec`'s
`Dest`, a tar/zip/deb/rpm entry name, any "path within a tree" string) and
joins it onto a real staging/extraction root with `filepath.Join` before
creating directories, chmodding, or writing file content there.

**What to do:** A path-splitting helper that only strips empty and `"."`
components (e.g. `strings.Split(p, "/")` filtered for `part != "" && part
!= "."`) still passes `".."` components straight through. Joined onto a
root with `filepath.Join(tree, part)` one component at a time, or even in
one shot, `..` walks back out of the intended root — a declared
destination like `/../../etc/cron.d/x` (or an equivalent crafted archive
entry) then creates directories, chmods, or writes file content **outside**
the staging tree the caller assumes is contained. This applies even to
test-only fixture builders, not just production extractors: a fixture
package that stages a `Spec` onto disk is exactly as capable of escaping
its tree as a real archive extractor is, and reviewers hold it to the same
standard. Before joining a declared/untrusted path onto a root, reject or
neutralize `..` components explicitly (walk the split components and
reject any `".."`, or resolve with `filepath.Clean`/`filepath.Rel` and
verify the result still has the root as a prefix) so containment is
guaranteed rather than incidental.

**Learned from:** mill run for issue #73 (`internal/packagingtest`'s `.deb`
fixture builder). Round 1 objected that `splitPath` retains `..`
components, so a destination such as `/../../outside` in a `Spec` causes
`makeDirs`/`writeFileMode` to escape the staging tree and write files or
chmod directories outside it.
