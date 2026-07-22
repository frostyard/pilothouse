# Reproducible Bump Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make plain `make bump` verify, calculate, create, and push the next release tag with only Docker and authenticated host Git installed.

**Architecture:** A focused `scripts/bump.sh` owns host-side Git preflight and safe tag publication. Make targets invoke the pinned development image for all Go, C-header, lint, formatting, and `svu` work; host Git alone fetches and mutates refs.

**Tech Stack:** GNU Make, POSIX shell, Git, Docker, Go 1.26.5, golangci-lint v2.11.4, svu v3.4.1.

## Global Constraints

- Plain `make bump` requires only Docker, Make, POSIX shell, and authenticated host Git; it must not require native Go, PAM/systemd headers, golangci-lint, or `svu`.
- Releases are allowed only from a clean local `main` whose `HEAD` exactly equals freshly fetched `origin/main`.
- Docker never receives SSH keys, Git identity, passwd/group mounts, or permission to create or push tags.
- All verification finishes before version calculation and tag creation.
- The proposed version must match `^v[0-9]+\.[0-9]+\.[0-9]+$` and must not already exist locally or remotely.
- Push-failure recovery must distinguish confirmed absence, accepted-but-reported-failed, and indeterminate remote state before altering the new local tag.
- Release formatting checks source without rewriting it, and release lint may never silently skip.
- Tests use temporary repositories and fake commands; they never mutate the developer checkout or contact the real origin.

---

## File Structure

- Create `scripts/bump.sh`: host-side preflight, verification/version command orchestration, version validation, tag creation, push, and push recovery.
- Create `scripts/bump_test.sh`: isolated shell test harness with temporary bare/local Git repositories and injected verification/version commands.
- Modify `Makefile`: pin `SVU_VERSION`, expose strict formatting/tool checks, add Docker bump targets, route `make bump` through `scripts/bump.sh`, and add `test-bump`.
- Modify `.docker/Dockerfile`: install pinned `svu` in the image's normal `PATH` beside golangci-lint.
- Modify `README.md`: document release prerequisites, safety checks, and publishing behavior.
- Modify `AGENTS.md`: require the supported bump entry point for release work.

### Task 1: Host Git Preflight

**Files:**
- Create: `scripts/bump.sh`
- Create: `scripts/bump_test.sh`

**Interfaces:**
- Consumes: host `git`; `DOCKER` environment variable defaulting to `docker`; repository remote `origin`.
- Produces: `scripts/bump.sh preflight`, exiting 0 only on clean synchronized `main`; reusable test helpers in `scripts/bump_test.sh`.

- [ ] **Step 1: Write failing preflight tests**

Create `scripts/bump_test.sh` with a temporary bare origin and checkout, a minimal assertion harness, and cases for the approved repository states:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
SCRIPT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/bump.sh
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PASS=0

fail() { printf 'not ok - %s\n' "$1" >&2; exit 1; }
pass() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }

new_repo() {
    local name=$1
    local remote="$ROOT/$name.git"
    local repo="$ROOT/$name"
    git init --bare --initial-branch=main "$remote" >/dev/null
    git clone "$remote" "$repo" >/dev/null 2>&1
    git -C "$repo" config user.name Test
    git -C "$repo" config user.email test@example.invalid
    printf 'initial\n' >"$repo/file.txt"
    git -C "$repo" add file.txt
    git -C "$repo" commit -m initial >/dev/null
    git -C "$repo" push -u origin main >/dev/null 2>&1
    printf '%s\n' "$repo"
}

run_preflight() {
    local repo=$1
    shift
    (cd "$repo" && DOCKER=true "$SCRIPT" preflight "$@")
}

repo=$(new_repo clean)
run_preflight "$repo" >/dev/null || fail 'accepts clean synchronized main'
pass 'accepts clean synchronized main'

git -C "$repo" switch -c feature >/dev/null
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects feature branch'; fi
grep -q 'switch to main' "$ROOT/out" || fail 'explains branch failure'
pass 'rejects feature branch'

git -C "$repo" switch main >/dev/null
printf 'dirty\n' >>"$repo/file.txt"
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects dirty tree'; fi
grep -q 'commit or stash' "$ROOT/out" || fail 'explains dirty failure'
git -C "$repo" restore file.txt
pass 'rejects dirty tree'

repo=$(new_repo detached)
git -C "$repo" checkout --detach >/dev/null
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects detached HEAD'; fi
grep -q 'detached HEAD' "$ROOT/out" || fail 'explains detached HEAD failure'
pass 'rejects detached HEAD'

repo=$(new_repo no-origin)
git -C "$repo" remote remove origin
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects missing origin'; fi
grep -q 'origin remote is missing' "$ROOT/out" || fail 'explains missing origin'
pass 'rejects missing origin'

repo=$(new_repo unreachable)
git -C "$repo" remote set-url origin "$ROOT/does-not-exist.git"
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects unreachable origin'; fi
grep -q 'synchronize origin' "$ROOT/out" || fail 'explains fetch failure'
pass 'rejects unreachable origin'

advance_remote() {
    local repo=$1
    local name=$2
    local peer="$ROOT/$name-peer"
    git clone "$(git -C "$repo" remote get-url origin)" "$peer" >/dev/null 2>&1
    git -C "$peer" config user.name Test
    git -C "$peer" config user.email test@example.invalid
    printf '%s\n' "$name" >>"$peer/file.txt"
    git -C "$peer" commit -am "$name" >/dev/null
    git -C "$peer" push origin main >/dev/null 2>&1
}

repo=$(new_repo behind)
advance_remote "$repo" behind
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects behind main'; fi
grep -q 'behind' "$ROOT/out" || fail 'explains behind state'
pass 'rejects behind main'

repo=$(new_repo ahead)
printf 'ahead\n' >>"$repo/file.txt"
git -C "$repo" commit -am ahead >/dev/null
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects ahead main'; fi
grep -q 'ahead' "$ROOT/out" || fail 'explains ahead state'
pass 'rejects ahead main'

repo=$(new_repo diverged)
printf 'local\n' >>"$repo/file.txt"
git -C "$repo" commit -am local >/dev/null
advance_remote "$repo" remote
if run_preflight "$repo" >"$ROOT/out" 2>&1; then fail 'rejects diverged main'; fi
grep -q 'diverged' "$ROOT/out" || fail 'explains diverged state'
pass 'rejects diverged main'

if DOCKER=definitely-missing "$SCRIPT" preflight >"$ROOT/out" 2>&1; then
    fail 'rejects missing Docker command'
fi
grep -q 'Docker command' "$ROOT/out" || fail 'explains missing Docker'
pass 'rejects missing Docker command'
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `bash scripts/bump_test.sh`

Expected: FAIL because `scripts/bump.sh` does not exist.

- [ ] **Step 3: Implement minimal preflight behavior**

Create `scripts/bump.sh` with these functions and exact command contract:

```sh
#!/bin/sh
set -eu

die() {
    printf 'bump: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "$2"
}

preflight() {
    require_command git 'Git is required.'
    require_command "${DOCKER:-docker}" 'Docker command is unavailable.'

    branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null) ||
        die 'detached HEAD is not releasable; switch to main.'
    [ "$branch" = main ] || die 'releases must run from main; switch to main.'
    [ -z "$(git status --porcelain)" ] ||
        die 'working tree is not clean; commit or stash changes.'
    git remote get-url origin >/dev/null 2>&1 || die 'origin remote is missing.'
    git fetch --tags origin '+refs/heads/main:refs/remotes/origin/main' >/dev/null 2>&1 ||
        die 'could not synchronize origin/main and tags.'

    local_head=$(git rev-parse HEAD)
    remote_head=$(git rev-parse refs/remotes/origin/main 2>/dev/null) ||
        die 'origin/main is unavailable after fetch.'
    [ "$local_head" = "$remote_head" ] && return 0

    if git merge-base --is-ancestor "$local_head" "$remote_head"; then
        die 'local main is behind origin/main; pull before bumping.'
    fi
    if git merge-base --is-ancestor "$remote_head" "$local_head"; then
        die 'local main is ahead of origin/main; push or reconcile before bumping.'
    fi
    die 'local main has diverged from origin/main; reconcile before bumping.'
}

case "${1:-release}" in
    preflight) preflight ;;
    release) die 'release orchestration is not implemented yet.' ;;
    *) die "unknown command: $1" ;;
esac
```

- [ ] **Step 4: Run preflight tests and syntax checks**

Run: `sh -n scripts/bump.sh && bash -n scripts/bump_test.sh && bash scripts/bump_test.sh`

Expected: all preflight cases print `ok`; exit 0.

- [ ] **Step 5: Commit the preflight slice**

```bash
git add scripts/bump.sh scripts/bump_test.sh
git commit -m "build: validate release repository state"
```

### Task 2: Safe Version Tag Publication

**Files:**
- Modify: `scripts/bump.sh`
- Modify: `scripts/bump_test.sh`

**Interfaces:**
- Consumes: `BUMP_VERIFY_COMMAND` and `BUMP_VERSION_COMMAND` shell command strings supplied by Make; `origin` authenticated through host Git.
- Produces: default `scripts/bump.sh release`; validated annotated tag `Version <version>`; remote-state-aware push recovery.

- [ ] **Step 1: Add failing release tests**

Extend `scripts/bump_test.sh` with helpers that create executable fake verification/version commands under `$ROOT/bin`, then test:

```bash
write_command() {
    local name=$1
    shift
    printf '#!/bin/sh\n%s\n' "$*" >"$ROOT/$name"
    chmod +x "$ROOT/$name"
    printf '%s\n' "$ROOT/$name"
}

repo=$(new_repo publish)
verify=$(write_command verify-success 'exit 0')
version=$(write_command version-success 'printf "%s\\n" v9.8.7')
(cd "$repo" && DOCKER=true BUMP_VERIFY_COMMAND="$verify" BUMP_VERSION_COMMAND="$version" "$SCRIPT") ||
    fail 'publishes valid version'
[ "$(git -C "$repo" rev-parse 'v9.8.7^{}')" = "$(git -C "$repo" rev-parse HEAD)" ] ||
    fail 'local tag points to HEAD'
git --git-dir="$ROOT/publish.git" rev-parse 'v9.8.7^{}' >/dev/null || fail 'remote tag exists'
[ "$(git -C "$repo" for-each-ref --format='%(contents)' refs/tags/v9.8.7)" = 'Version v9.8.7' ] ||
    fail 'tag has expected annotation'
pass 'publishes annotated tag'
```

Append these command and Git-wrapper helpers, then use them for the failure cases:

```bash
run_release() {
    local repo=$1 verify=$2 version=$3
    local git_command=${4:-git}
    (cd "$repo" && DOCKER=true BUMP_GIT_COMMAND="$git_command" \
        BUMP_VERIFY_COMMAND="$verify" BUMP_VERSION_COMMAND="$version" "$SCRIPT" release)
}

write_git_wrapper() {
    local name=$1 mode=$2
    local wrapper="$ROOT/$name"
    cat >"$wrapper" <<EOF
#!/bin/sh
set -eu
mode='$mode'
if [ "\$1" = push ]; then
    case "\$mode" in
        absent|indeterminate) exit 41 ;;
        accepted) git "\$@"; exit 41 ;;
    esac
fi
if [ "\$mode" = indeterminate ] && [ "\$1" = ls-remote ] &&
   git rev-parse --verify --quiet refs/tags/v9.8.7 >/dev/null; then
    exit 42
fi
exec git "\$@"
EOF
    chmod +x "$wrapper"
    printf '%s\n' "$wrapper"
}

repo=$(new_repo verify-failure)
marker="$ROOT/version-called"
verify=$(write_command verify-failure 'exit 23')
version=$(write_command version-marker "touch '$marker'; printf '%s\\n' v9.8.7")
if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
    fail 'stops after verification failure'
fi
[ ! -e "$marker" ] || fail 'version command ran after verification failure'
pass 'stops before version calculation when verification fails'

repo=$(new_repo missing-git-command)
if (cd "$repo" && DOCKER=true BUMP_GIT_COMMAND=definitely-missing "$SCRIPT" preflight) \
    >"$ROOT/out" 2>&1; then
    fail 'accepts missing Git command'
fi
grep -q 'Git is required' "$ROOT/out" || fail 'explains missing Git command'
pass 'rejects missing Git command'

for value in '' '1.2.3' 'v1.2.3-rc.1' $'v1.2.3\nv1.2.4'; do
    repo=$(new_repo "invalid-$RANDOM")
    verify=$(write_command "verify-$RANDOM" 'exit 0')
    version=$(write_command "version-$RANDOM" "printf '%s\\n' '$value'")
    if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
        fail "accepts invalid version: $value"
    fi
done
pass 'rejects empty, unprefixed, prerelease, and multiline versions'

repo=$(new_repo local-existing)
git -C "$repo" tag -a v9.8.7 -m existing
verify=$(write_command verify-local-existing 'exit 0')
version=$(write_command version-local-existing 'printf "%s\\n" v9.8.7')
if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
    fail 'accepts existing local tag'
fi
git -C "$repo" rev-parse v9.8.7 >/dev/null || fail 'deleted pre-existing local tag'
pass 'preserves and rejects existing local tag'

repo=$(new_repo remote-existing)
verify=$(write_command verify-remote-existing 'exit 0')
version=$(write_command version-remote-existing \
    'git tag -a v9.8.7 -m racing-release; git push origin v9.8.7 >/dev/null 2>&1; git tag -d v9.8.7 >/dev/null; printf "%s\\n" v9.8.7')
if run_release "$repo" "$verify" "$version" >"$ROOT/out" 2>&1; then
    fail 'accepts existing remote tag'
fi
pass 'rejects existing remote tag'

repo=$(new_repo push-absent)
verify=$(write_command verify-push-absent 'exit 0')
version=$(write_command version-push-absent 'printf "%s\\n" v9.8.7')
wrapper=$(write_git_wrapper git-push-absent absent)
if run_release "$repo" "$verify" "$version" "$wrapper" >"$ROOT/out" 2>&1; then
    fail 'reports failed push as success'
fi
if git -C "$repo" rev-parse --verify refs/tags/v9.8.7 >/dev/null 2>&1; then
    fail 'retains new tag after confirmed remote absence'
fi
pass 'rolls back new local tag after confirmed absence'

repo=$(new_repo push-accepted)
verify=$(write_command verify-push-accepted 'exit 0')
version=$(write_command version-push-accepted 'printf "%s\\n" v9.8.7')
wrapper=$(write_git_wrapper git-push-accepted accepted)
run_release "$repo" "$verify" "$version" "$wrapper" >"$ROOT/out" 2>&1 ||
    fail 'does not recover accepted push'
git --git-dir="$ROOT/push-accepted.git" rev-parse 'v9.8.7^{}' >/dev/null ||
    fail 'accepted remote tag missing'
pass 'recognizes accepted push despite transport failure'

repo=$(new_repo push-indeterminate)
verify=$(write_command verify-push-indeterminate 'exit 0')
version=$(write_command version-push-indeterminate 'printf "%s\\n" v9.8.7')
wrapper=$(write_git_wrapper git-push-indeterminate indeterminate)
if run_release "$repo" "$verify" "$version" "$wrapper" >"$ROOT/out" 2>&1; then
    fail 'reports indeterminate push as success'
fi
grep -q 'indeterminate' "$ROOT/out" || fail 'does not explain indeterminate push'
git -C "$repo" rev-parse v9.8.7 >/dev/null || fail 'deletes tag in indeterminate state'
pass 'preserves local tag when remote state is indeterminate'
```

- [ ] **Step 2: Run release tests and verify RED**

Run: `bash scripts/bump_test.sh`

Expected: FAIL with `release orchestration is not implemented yet`.

- [ ] **Step 3: Implement version validation and publication**

Refactor `scripts/bump.sh` so every Git call uses `git_command=${BUMP_GIT_COMMAND:-git}`. Add these functions:

```sh
run_git() {
    "$git_command" "$@"
}

validate_version() {
    version=$1
    case "$version" in
        v0|v0.*) ;;
        v[0-9]*.[0-9]*.[0-9]*) ;;
        *) die "svu returned invalid version: $version" ;;
    esac
    printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
        die "svu returned invalid version: $version"
    [ "$(printf '%s\n' "$version" | wc -l | tr -d ' ')" = 1 ] ||
        die 'svu returned multiple version lines.'
}

remote_tag_output() {
    run_git ls-remote --tags origin "refs/tags/$1" "refs/tags/$1^{}"
}

remote_tag_commit() {
    version=$1
    output=$2
    peeled=$(printf '%s\n' "$output" | awk -v ref="refs/tags/$version^{}" '$2 == ref { print $1; exit }')
    [ -n "$peeled" ] && { printf '%s\n' "$peeled"; return; }
    printf '%s\n' "$output" | awk -v ref="refs/tags/$version" '$2 == ref { print $1; exit }'
}
```

Use a temporary output file so command failure, empty output, and multiline output are distinguishable. Add the complete release function:

```sh
release() {
    preflight

    verify_command=${BUMP_VERIFY_COMMAND:-'make --no-print-directory docker-bump-verify'}
    version_command=${BUMP_VERSION_COMMAND:-'make --silent --no-print-directory docker-next-version'}
    printf 'Running release verification...\n'
    sh -c "$verify_command" || die 'release verification failed; no tag was created.'

    version_file=$(mktemp)
    trap 'rm -f "$version_file"' EXIT HUP INT TERM
    sh -c "$version_command" >"$version_file" ||
        die 'could not calculate the next version; no tag was created.'
    line_count=$(awk 'END { print NR }' "$version_file")
    [ "$line_count" -eq 1 ] || die 'svu must return exactly one version line.'
    version=$(sed -n '1p' "$version_file")
    validate_version "$version"

    if run_git rev-parse --verify --quiet "refs/tags/$version" >/dev/null; then
        die "tag $version already exists locally."
    fi
    if ! before=$(remote_tag_output "$version"); then
        die "could not determine whether $version exists on origin."
    fi
    [ -z "$before" ] || die "tag $version already exists on origin."

    intended=$(run_git rev-parse HEAD)
    run_git tag -a "$version" -m "Version $version" ||
        die "could not create tag $version."
    if run_git push origin "refs/tags/$version"; then
        printf 'Published %s.\n' "$version"
        return 0
    fi

    if ! after=$(remote_tag_output "$version"); then
        die "push failed and publication state is indeterminate; local tag $version was preserved."
    fi
    remote_commit=$(remote_tag_commit "$version" "$after")
    if [ "$remote_commit" = "$intended" ]; then
        printf 'Published %s despite a reported transport failure.\n' "$version"
        return 0
    fi
    if [ -z "$after" ]; then
        run_git tag -d "$version" >/dev/null ||
            die "push failed; origin lacks $version, but the local tag could not be removed."
        die "push failed; origin lacks $version and the new local tag was removed. Retry make bump."
    fi
    die "push failed and publication state is indeterminate; local tag $version was preserved."
}
```

Initialize `git_command=${BUMP_GIT_COMMAND:-git}` before dispatch. Change preflight to call `require_command "$git_command" 'Git is required.'` and replace each literal `git` invocation with `run_git`. Dispatch `release) release` and keep the temporary-file trap scoped to deleting only that file; never install a tag-deletion trap.

- [ ] **Step 4: Run all script tests**

Run: `sh -n scripts/bump.sh && bash -n scripts/bump_test.sh && bash scripts/bump_test.sh`

Expected: all preflight, validation, publication, and recovery cases pass; no real network access.

- [ ] **Step 5: Commit safe publication**

```bash
git add scripts/bump.sh scripts/bump_test.sh
git commit -m "build: publish release tags safely"
```

### Task 3: Reproducible Docker And Make Integration

**Files:**
- Modify: `.docker/Dockerfile`
- Modify: `Makefile`
- Modify: `scripts/bump_test.sh`

**Interfaces:**
- Consumes: `scripts/bump.sh release`; Docker build args `GO_VERSION`, `GOLANGCI_LINT_VERSION`, and `SVU_VERSION`.
- Produces: `make bump`, `make bump-preflight`, `make bump-verify`, `make docker-bump-verify`, `make docker-next-version`, `make docker-tools-check`, and `make test-bump`.

- [ ] **Step 1: Add failing Make/Docker contract tests**

Add a `make_contracts` test function to `scripts/bump_test.sh` that asserts:

```bash
grep -q '^SVU_VERSION ?= v3\.4\.1$' "$REPO_ROOT/Makefile" || fail 'pins svu version'
grep -q '^bump-preflight:' "$REPO_ROOT/Makefile" || fail 'exposes bump preflight'
grep -q '^bump-verify:' "$REPO_ROOT/Makefile" || fail 'exposes strict release verification'
grep -q '^docker-bump-verify:' "$REPO_ROOT/Makefile" || fail 'exposes Docker verification'
grep -q '^docker-next-version:' "$REPO_ROOT/Makefile" || fail 'exposes Docker svu calculation'
grep -q '^docker-tools-check:' "$REPO_ROOT/Makefile" || fail 'exposes Docker tool smoke check'
grep -q '^test-bump:' "$REPO_ROOT/Makefile" || fail 'runs bump harness'
grep -q 'ARG SVU_VERSION' "$REPO_ROOT/.docker/Dockerfile" || fail 'Dockerfile accepts svu version'
grep -q 'github.com/caarlos0/svu/v3@${SVU_VERSION}' "$REPO_ROOT/.docker/Dockerfile" ||
    fail 'Dockerfile installs pinned svu'
pass 'Make and Docker contracts are present'
```

- [ ] **Step 2: Run contract tests and verify RED**

Run: `bash scripts/bump_test.sh`

Expected: FAIL with `pins svu version`.

- [ ] **Step 3: Install pinned svu in the development image**

Modify `.docker/Dockerfile`:

```dockerfile
ARG GO_VERSION=1.26.5
FROM golang:${GO_VERSION}-bookworm

ARG GOLANGCI_LINT_VERSION=v2.11.4
ARG SVU_VERSION=v3.4.1

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends libpam0g-dev libsystemd-dev \
    && rm -rf /var/lib/apt/lists/* \
    && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && go install github.com/caarlos0/svu/v3@${SVU_VERSION} \
    && install -d -m 1777 /cache/go-build /cache/go-mod /cache/golangci-lint

WORKDIR /workspace
```

- [ ] **Step 4: Replace the native bump recipe with strict Docker orchestration**

Update `.PHONY`, add `SVU_VERSION`, pass its build argument, and replace the old bump recipe with:

```make
SVU_VERSION ?= v3.4.1

format-check: ## Verify Go source formatting without rewriting files
	@files="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

bump-preflight: ## Verify that main is clean and synchronized
	@DOCKER="$(DOCKER)" ./scripts/bump.sh preflight

bump-verify: ## Run strict release checks inside the development image
	@$(MAKE) build
	@$(MAKE) test
	@$(MAKE) format-check
	golangci-lint run

docker-bump-verify: docker-image ## Run all release checks in Docker
	$(DOCKER_RUN) make bump-verify

docker-next-version: ## Calculate the next version with pinned svu
	@$(MAKE) --no-print-directory docker-image >&2
	@$(DOCKER_RUN) svu next

docker-tools-check: docker-image ## Verify release tools are executable in Docker
	$(DOCKER_RUN) sh -c 'svu --version && golangci-lint version'

test-bump: ## Test release orchestration without publishing
	bash scripts/bump_test.sh

bump: ## Verify and publish the next version tag
	@DOCKER="$(DOCKER)" \
	BUMP_VERIFY_COMMAND='$(MAKE) --no-print-directory docker-bump-verify' \
	BUMP_VERSION_COMMAND='$(MAKE) --silent --no-print-directory docker-next-version' \
	./scripts/bump.sh release
```

Add this exact argument to the existing `docker build` recipe:

```make
		--build-arg SVU_VERSION=$(SVU_VERSION) \
```

The recursive `docker-image` invocation in `docker-next-version` redirects both its command and Docker build output to stderr, leaving only `svu next` on stdout for validation.

- [ ] **Step 5: Run script and Make contract tests**

Run: `make test-bump`

Expected: all tests pass.

- [ ] **Step 6: Prove both image tools are on PATH**

Run: `make docker-tools-check`

Expected: output includes svu `v3.4.1` and golangci-lint `v2.11.4`; exit 0.

- [ ] **Step 7: Run Docker release verification without tagging**

Run: `make docker-bump-verify`

Expected: both binaries build, all Go tests pass, formatting is clean, golangci-lint reports 0 issues, and no tag is created.

- [ ] **Step 8: Commit Docker/Make integration**

```bash
git add .docker/Dockerfile Makefile scripts/bump_test.sh
git commit -m "build: run bump workflow through Docker"
```

### Task 4: Documentation And End-To-End Safety Verification

**Files:**
- Modify: `README.md:32-56`
- Modify: `AGENTS.md:25-27`

**Interfaces:**
- Consumes: final Make targets from Task 3.
- Produces: documented developer/release workflow and final verified branch.

- [ ] **Step 1: Add release documentation**

Append after the Docker development-target paragraph in `README.md`:

```markdown
### Create a release

`make bump` verifies the project in the development container, calculates the
next semantic version with the container's pinned `svu`, creates an annotated
tag, and immediately pushes that tag to `origin`.

The host needs only Docker, Make, and authenticated Git access. Run it from a
clean `main` checkout that exactly matches `origin/main`; the target rejects
dirty, ahead, behind, divergent, feature-branch, and detached-HEAD states
before creating a tag.

```bash
make bump
```
```

Update `AGENTS.md` so release work explicitly uses `make bump`, not an ad hoc container or native fallback:

```markdown
Run releases with `make bump` from a clean, synchronized `main`. The target
uses the development image for build dependencies, lint, and `svu`, then uses
authenticated host Git to create and push the tag. Do not run the full bump
target inside an ad hoc container or pass Git credentials into the image.
```

- [ ] **Step 2: Run the isolated release harness**

Run: `make test-bump`

Expected: all shell tests pass and only temporary repositories receive test tags.

- [ ] **Step 3: Run complete project verification**

Run: `make docker-generate && make docker-fmt && make docker-build && make docker-test && make docker-lint && make docker-tools-check`

Expected: every command exits 0; generated output has no tracked diff; lint reports 0 issues; both pinned release tools execute.

- [ ] **Step 4: Verify the real checkout is protected**

Run: `make bump-preflight`

Expected on this feature branch: FAIL before Docker work with `releases must run from main; switch to main.` This is an intentional safety assertion.

Run: `git tag --points-at HEAD`

Expected: no new version tag from the implementation or verification steps.

- [ ] **Step 5: Review final diff and repository state**

Run: `git diff --check && git status --short && git diff main...HEAD --stat`

Expected: no whitespace errors; only the design, plan, scripts, Makefile, Dockerfile, README, and AGENTS changes are present.

- [ ] **Step 6: Commit documentation**

```bash
git add README.md AGENTS.md
git commit -m "docs: describe containerized bump workflow"
```

- [ ] **Step 7: Request final code review**

Use the requesting-code-review workflow against `main...HEAD`. Address correctness findings, rerun the affected test plus the complete verification command from Step 3, and create separate fix commits rather than amending.
