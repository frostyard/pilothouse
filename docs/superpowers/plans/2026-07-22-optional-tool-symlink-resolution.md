# Optional Tool Symlink Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow `pilothoused` to start when fixed optional storage tools such as `pvs`, `vgs`, and `lvs` are safe symlinks to a validated executable.

**Architecture:** Keep optional-tool policy in `internal/modules/storage/tools.go`. Inspect the candidate itself with `Lstat` to distinguish a missing path from a broken link, inspect the fully resolved target with `Stat`, validate the target's type, owner, and permissions, and return the original candidate path so multicall tools retain their entry-point name.

**Tech Stack:** Go 1.26, standard-library `os` and `syscall`, testify, Markdown documentation.

## Global Constraints

- Only fixed storage-tool candidates may be resolved; do not add PATH lookup or caller-supplied executable paths.
- The resolved target must be a regular file, root-owned, and not group- or world-writable.
- Missing candidates remain unsupported without error; broken or unsafe present candidates remain startup errors.
- Return the original candidate path so LVM multicall dispatch through `pvs`, `vgs`, and `lvs` remains intact.
- Production `newStorageManager` passes this resolver to `NewToolsetWithResolver`, so its fixed core `lsblk` and `findmnt` candidates also accept safe symlinks. Do not change candidate lists, the separate `resolveSystemTool` behavior, storage command arguments, broker protocols, or authentication behavior.
- Do not commit changes unless the user explicitly requests a commit.

---

### Task 1: Resolve And Validate Optional Tool Targets

**Files:**
- Modify: `internal/modules/storage/tools.go:86-89,164-195`
- Test: `internal/modules/storage/tools_test.go:58-90`

**Interfaces:**
- Consumes: fixed `[]string` candidate paths and `os.Lstat`/`os.Stat`-compatible functions.
- Produces: `ResolveOptionalTool(candidates []string, lstat, stat func(string) (os.FileInfo, error)) (string, bool, error)` and the unchanged `ToolResolver` behavior used by `newStorageManager`.

- [ ] **Step 1: Replace the symlink-rejection test with a failing safe-target regression test**

Update the helper call sites to pass both `os.Lstat` and `os.Stat`, then replace `TestResolveOptionalToolRejectsExistingSymlink` with:

```go
func TestResolveOptionalToolAcceptsSymlinkToSafeTarget(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.Symlink("/usr/bin/lsblk", link))

	path, supported, err := resolveOptionalTool([]string{link}, os.Lstat, os.Stat)

	require.NoError(t, err)
	assert.True(t, supported)
	assert.Equal(t, link, path)
}
```

Also update existing calls:

```go
path, supported, err := resolveOptionalTool([]string{"/does/not/exist"}, os.Lstat, os.Stat)
```

```go
_, supported, err := resolveOptionalTool([]string{"/usr/bin/lsblk", link}, os.Lstat, os.Stat)
```

- [ ] **Step 2: Run the focused test and confirm the current implementation rejects the safe symlink**

Run: `go test ./internal/modules/storage -run '^TestResolveOptionalToolAcceptsSymlinkToSafeTarget$'`

Expected: build failure because `resolveOptionalTool` accepts only two arguments, proving the production interface does not yet support separate candidate and target inspection.

- [ ] **Step 3: Add failing broken-link and unsafe-target coverage**

Add `syscall` and `time` to the test imports, then add these tests and the
deterministic file-info stub after the safe-target test:

```go
func TestResolveOptionalToolRejectsBrokenSymlink(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.Symlink(filepath.Join(directory, "missing"), link))

	_, supported, err := resolveOptionalTool([]string{link}, os.Lstat, os.Stat)

	assert.ErrorContains(t, err, "resolve optional tool")
	assert.False(t, supported)
}

func TestResolveOptionalToolRejectsUnsafeResolvedTarget(t *testing.T) {
	directory := t.TempDir()
	unsafe := filepath.Join(directory, "unsafe")
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.WriteFile(unsafe, []byte("tool"), 0o777))
	require.NoError(t, os.Symlink(unsafe, link))

	_, supported, err := resolveOptionalTool([]string{link}, os.Lstat, os.Stat)

	assert.Error(t, err)
	assert.False(t, supported)
}

func TestResolveOptionalToolRejectsNonRootOwnedResolvedTarget(t *testing.T) {
	link := optionalToolFileInfo{mode: os.ModeSymlink | 0o777, uid: 0}
	target := optionalToolFileInfo{mode: 0o755, uid: 1000}

	_, supported, err := resolveOptionalTool(
		[]string{"/usr/sbin/tool"},
		func(string) (os.FileInfo, error) { return link, nil },
		func(string) (os.FileInfo, error) { return target, nil },
	)

	assert.ErrorContains(t, err, "not root-owned")
	assert.False(t, supported)
}

func TestResolveOptionalToolRejectsNonRegularResolvedTarget(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "tool")
	require.NoError(t, os.Symlink(directory, link))

	_, supported, err := resolveOptionalTool([]string{link}, os.Lstat, os.Stat)

	assert.ErrorContains(t, err, "not a regular file")
	assert.False(t, supported)
}

type optionalToolFileInfo struct {
	mode os.FileMode
	uid  uint32
}

func (info optionalToolFileInfo) Name() string       { return "tool" }
func (info optionalToolFileInfo) Size() int64        { return 0 }
func (info optionalToolFileInfo) Mode() os.FileMode  { return info.mode }
func (info optionalToolFileInfo) ModTime() time.Time { return time.Time{} }
func (info optionalToolFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info optionalToolFileInfo) Sys() any           { return &syscall.Stat_t{Uid: info.uid} }
```

These tests use a guaranteed group/world-writable target for the permission
case and an injected `syscall.Stat_t` for the ownership case, so their outcomes
do not depend on the user running the suite.

- [ ] **Step 4: Implement separate candidate and resolved-target inspection**

Change the production resolver to:

```go
func NewOptionalToolResolver() ToolResolver {
	return func(candidates []string) (string, bool, error) {
		return ResolveOptionalTool(candidates, os.Lstat, os.Stat)
	}
}
```

```go
func resolveOptionalTool(candidates []string, lstat, stat func(string) (os.FileInfo, error)) (string, bool, error) {
	return ResolveOptionalTool(candidates, lstat, stat)
}

func ResolveOptionalTool(candidates []string, lstat, stat func(string) (os.FileInfo, error)) (string, bool, error) {
	var resolved string
	for _, path := range candidates {
		_, err := lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("inspect optional tool %s: %w", path, err)
		}
		info, err := stat(path)
		if err != nil {
			return "", false, fmt.Errorf("resolve optional tool %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", false, fmt.Errorf("optional tool %s is not a regular file", path)
		}
		identity, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return "", false, fmt.Errorf("inspect %s ownership", path)
		}
		if identity.Uid != 0 {
			return "", false, fmt.Errorf("optional tool %s is not root-owned", path)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return "", false, fmt.Errorf("optional tool %s is group- or world-writable", path)
		}
		if resolved == "" {
			resolved = path
		}
	}
	return resolved, resolved != "", nil
}
```

- [ ] **Step 5: Run resolver tests and confirm all policy cases pass**

Run: `go test ./internal/modules/storage -run '^TestResolveOptionalTool'`

Expected: PASS for missing candidates, safe symlink targets, broken links,
non-regular targets, unsafe permissions, unsafe ownership, and unsafe candidates
after a safe candidate.

- [ ] **Step 6: Run the storage and broker composition tests**

Run: `go test ./internal/modules/storage ./cmd/pilothoused`

Expected: both packages PASS, including `newStorageManager` behavior.

### Task 2: Document Startup Validation And Troubleshooting

**Files:**
- Modify: `docs/modules.md:119-123`
- Modify: `docs/authentication.md:25-31`
- Modify: `README.md:32-56`
- Modify: `yeti/OVERVIEW.md:86-113`
- Review only: `CLAUDE.md` (not present; no update possible)
- Existing design: `docs/superpowers/specs/2026-07-22-optional-tool-symlink-resolution-design.md`

**Interfaces:**
- Consumes: the resolver behavior implemented in Task 1.
- Produces: operator-facing troubleshooting and AI-facing executable-validation context.

- [ ] **Step 1: Document optional-tool symlink validation in module architecture**

Append this paragraph after the storage inventory paragraph in `docs/modules.md`:

```markdown
Optional storage tools are selected only from fixed absolute candidates. A
candidate may be a symbolic link, as is common for LVM's `pvs`, `vgs`, and
`lvs` entry points, but its fully resolved target must be a root-owned regular
file that is not writable by group or others. Missing tools degrade their
backend to unsupported; a present broken or unsafe candidate fails broker
startup.
```

- [ ] **Step 2: Add broker-first login troubleshooting guidance**

Append this paragraph to the Login section of `docs/authentication.md`:

```markdown
If local sign-in fails while `pilothoused` is restarting, diagnose the broker
startup failure first with `systemctl status pilothoused` and
`journalctl -u pilothoused`. The web process cannot authenticate without the
broker; a stale login page from a previous web-process instance can also submit
an obsolete login CSRF token and show `invalid csrf token` as a secondary
symptom.
```

- [ ] **Step 3: Add concise operator troubleshooting to the README**

After the Docker development-target paragraph in `README.md`, add:

```markdown
If local sign-in is unavailable, verify the privileged broker before debugging
the browser: `systemctl status pilothoused` and `journalctl -u pilothoused`.
The broker validates fixed storage-tool paths at startup; distro-provided
symlinks such as `pvs -> lvm` are accepted only when the resolved executable is
a safe root-owned regular file.
```

- [ ] **Step 4: Record the resolver invariant in AI architecture context**

Add this bullet under "The broker is the only privilege boundary" in `yeti/OVERVIEW.md`:

```markdown
- **Storage executable validation.** Core and optional storage commands use
  fixed absolute candidates. Optional candidates may be symlinks for distro
  multicall tools such as LVM, but the broker validates the fully resolved
  target as a root-owned, non-group/world-writable regular file while executing
  the original entry-point path. Broken or unsafe present candidates fail
  startup; absent optional tools degrade only their backend to unsupported.
```

- [ ] **Step 5: Run formatting before repository-wide verification**

Run: `make fmt`

Expected: command exits 0 and formats all non-generated Go source.

- [ ] **Step 6: Run the required build and test targets**

Run: `make build`

Expected: both `bin/pilothouse` and `bin/pilothoused` build successfully.

Run: `make test`

Expected: all Go tests PASS.

- [ ] **Step 7: Run the required lint target**

Run: `make lint`

Expected: golangci-lint reports no findings. If native PAM or systemd dependencies are unavailable, run `make docker-build`, `make docker-test`, `make docker-fmt`, and `make docker-lint` instead and require all four commands to exit 0.

- [ ] **Step 8: Inspect the final diff for scope and generated-file safety**

Run: `git status --short && git diff --check && git diff -- internal/modules/storage/tools.go internal/modules/storage/tools_test.go docs/modules.md docs/authentication.md README.md yeti/OVERVIEW.md docs/superpowers/specs/2026-07-22-optional-tool-symlink-resolution-design.md docs/superpowers/plans/2026-07-22-optional-tool-symlink-resolution.md`

Expected: only the intended resolver, tests, and documentation are changed; `git diff --check` emits no errors; no generated `*_templ.go` file is modified.
