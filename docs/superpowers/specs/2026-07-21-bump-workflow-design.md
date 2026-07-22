# Reproducible Bump Workflow Design

## Objective

`make bump` releases only from a clean local `main` that exactly matches
`origin/main`, with authoritative synchronized tags and no host Git identity or
configuration exposed to Docker. The host owns Git authentication and tag
publication; Docker supplies pinned build, lint, and version-calculation tools.

## Architecture

- `scripts/bump.sh` performs host-side preflight, runs verification and version
  commands, validates the version, and creates and pushes the annotated tag.
- Preflight requires Git and Docker, `main`, a clean worktree, and `origin`.
  It fetches `origin/main` and tags, requires local `HEAD` to equal
  `refs/remotes/origin/main`, and compares sorted direct `object-id ref` tag
  sets from local refs and `git ls-remote --tags origin`. Peeled `^{}` entries
  are excluded. Any local-only, remote-only, or object-ID-mismatched tag fails
  preflight.
- `docker-bump-verify` creates a temporary clean clone, removes its `.git`, and
  mounts only that source into Docker. Verification therefore has no Git
  metadata, configuration, identity, or credentials from the host checkout.
- `docker-next-version` creates a temporary `git clone --mirror --no-local`,
  removes `origin` from that bare mirror, and mounts only the mirror read-only
  at `/repository`. Pinned `svu next` runs there. No live host `.git` or
  `.git/config` enters Docker.

## Workflow

1. Validate host commands, branch, clean worktree, and `origin`.
2. Fetch `origin/main` and tags; reject unequal main commits and unequal direct
   local/remote tag sets.
3. Build the development image and run generation, both builds, tests, format
   checking without rewrites, and mandatory golangci-lint in an isolated
   Git-less source clone.
4. Calculate the next version with pinned `svu` in the sanitized read-only bare
   mirror. Require exactly one stdout line matching
   `^v[0-9]+\.[0-9]+\.[0-9]+$`.
5. Reject existing local or remote proposed tags, create `Version <version>` on
   host `HEAD`, and push only that tag with host Git.
6. On reported push failure, query the remote tag directly. Treat a tag resolving
   to the intended commit as published; remove the new local tag only when the
   remote confirms absence; preserve it and report a remote tag conflict when a
   non-empty remote tag resolves elsewhere; preserve it as indeterminate when
   the remote cannot be queried.

## Testing And Verification

The shell harness uses temporary bare origins, temporary clones, and injectable
Git wrappers. It covers branch and cleanliness checks, tag parity including a
local-only semver tag, version validation, publication, and all push recovery
states. The missing-Docker case runs in a temporary repository, never the
developer checkout.

Final verification must run:

```sh
make test-bump
make docker-bump-verify
make docker-lint
make --silent docker-next-version
```

Capture the final command's stdout and verify it contains exactly one line and
that line matches `^v[0-9]+\.[0-9]+\.[0-9]+$`. These commands do not create or
push a release tag.

## Non-Goals

- Changing `.svu.yml` version policy.
- Passing host Git credentials, identity, configuration, or live Git metadata
  into Docker.
- Releasing from feature branches, detached HEADs, dirty worktrees, unequal
  `main` commits, or unequal tag state.
