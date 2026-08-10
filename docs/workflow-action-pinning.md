# Workflow action pinning

Every external `uses:` reference under `.github/workflows/` is an executable
supply-chain dependency and must have an immutable identity:

- GitHub actions and reusable workflows use a full 40-character commit SHA.
- Container actions use a `sha256` digest.
- Repository-local actions beginning with `./` are already bound to the checked
  out repository and are exempt.

Keep the reviewed version or source ref in a trailing comment so updates remain
readable, for example:

```yaml
uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
```

Resolve and review a new upstream commit before changing a pin; never replace a
pin with a tag, branch, short SHA, expression, or container tag for convenience.
`internal/workflowcheck/actions_test.go` parses all `.yml` and `.yaml` files
recursively below `.github/workflows/` and rejects those mutable forms. Run
`make ci` (or `make docker-ci`) after any workflow action update.
