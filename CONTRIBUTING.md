# Contributing to Pilothouse

Thank you for helping improve Pilothouse.

## Before you start

- Search the existing issues and pull requests before starting work.
- Open an issue before making a substantial change so its scope and design can
  be discussed.
- Keep each pull request focused and avoid unrelated changes.

## Development

Pilothouse requires Go 1.26 or newer. The application uses idiomatic Go, templ,
HTMX, and vanilla CSS and JavaScript. See the [development
instructions](README.md#develop) for local setup and commands.

Keep management features in `internal/modules/<name>`. Unprivileged web modules
may collect read-only data locally, but privileged reads and mutations must use
fixed broker queries and actions. Register privileged implementations only in
`cmd/pilothoused`; do not add arbitrary command execution, filesystem access,
or generic socket proxying to the broker protocol.

Add or update focused tests for behavioral changes. After editing a `*.templ`
file, run `make generate` and update its rendering tests. Do not edit generated
`*_templ.go` files.

Update relevant documentation when behavior, configuration, or architecture
changes.

## Classify the change

Before requesting review, select the highest applicable tier from
[`docs/risk-tiers.md`](docs/risk-tiers.md) and record the tier and rationale in
the pull request template. Classification follows potential impact rather than
diff size. Tier 3 and Tier 4 changes require additional failure-path and
rollback evidence; Tier 4 also requires explicit maintainer security review and
trust-boundary analysis.

Update the classification if the pull request's scope changes. The tier adds
review requirements and never replaces repository checks or invariants.

## Validate and submit

Run the credential-free CI checks before opening a pull request:

```sh
make ci
```

If your host lacks Go, PAM, or systemd development dependencies, use:

```sh
make docker-ci
```

In your pull request, describe the problem and solution, link the relevant
issue, and list the validation you performed.
