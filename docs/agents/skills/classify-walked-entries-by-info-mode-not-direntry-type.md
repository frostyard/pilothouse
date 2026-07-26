# Classify walked filesystem entries with `info.Mode()`, not `fs.DirEntry.IsDir()`/`.Type()`, once you've already called `.Info()`

**When it applies:** Code that walks a directory tree (`filepath.WalkDir`,
`fs.WalkDir`, or manually iterating `os.ReadDir` results) and branches on
entry kind — directory vs. regular file vs. symlink/device/fifo — in order
to decide what to record, read, or skip (e.g. mapping an extracted archive
payload onto a model's `Entries`).

**What to do:** `fs.DirEntry.IsDir()` and `fs.DirEntry.Type()` are cheap,
partial-mode values some platforms populate directly from the raw
`readdir()` result without a full `stat`/`lstat`. On filesystems or entries
where the type bits aren't known up front, they can come back as the zero
`fs.FileMode` — and a zero `fs.FileMode` satisfies `.IsRegular()` (it has
none of the type bits set, which is exactly what a regular file's mode
looks like). A classification `switch` written as `case d.IsDir(): ...
case d.Type().IsRegular(): ... default: /* drop */` can therefore silently
misroute or misclassify: a directory or an irregular entry (symlink,
device, fifo) whose type came back zero is treated as a regular file
instead of being emitted as a directory or correctly dropped. If the code
already calls `d.Info()` (or `os.Lstat`) to get a full `os.FileInfo` for
other fields (size, mode bits for the entry, mtime), classify from that
same authoritative `info.Mode()` (`info.Mode().IsDir()` /
`info.Mode().IsRegular()`) instead of the lighter-weight `DirEntry`
accessors — don't mix a fast, potentially-zero classification with a
stat-backed one obtained one line above it.

**Learned from:** mill run for issue #73 (`packaging/extract`'s Deb
payload walker, `debEntries`). The reviewer objected that entry
classification used `d.IsDir()`/`d.Type().IsRegular()` even though
`info.Mode()` was already available from `d.Info()` on the line above,
and that a zero-valued `DirEntry.Type()` on an unknown-type filesystem
would satisfy `IsRegular()`, misclassifying directories or dropped-type
entries as regular files.
