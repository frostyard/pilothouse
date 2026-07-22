# AGENTS

This project is derived from `housecat-inc/scratch` and follows its core stack:

- Idiomatic Go
- HTML via templ
- HTMX for focused interactivity
- Vanilla CSS and JavaScript sparingly

Keep management features isolated in `internal/modules/<name>`. Web modules may collect unprivileged read-only data locally. Privileged reads must use a fixed broker query, and mutations must use a fixed broker action. Register privileged implementations only in `cmd/pilothoused`; never add arbitrary command execution, filesystem access, or generic socket proxying to the broker protocol.

Run `make generate` after editing `*.templ`. Never hand-edit generated `*_templ.go` files.

When composing templ components with text, put the component invocation in its own template node. Do not embed calls such as `@web.Icon("chevron")` in a text node (`View all @web.Icon("chevron")` renders literally). For example:

```templ
<a class="card-link" href="/attention">
    View all
    @web.Icon("chevron")
</a>
```

For any new or changed templ component invocation, add or update a rendering test that asserts the rendered HTML contains the component output and does not contain the literal `@web.` call syntax.

Run `make build`, `make test`, `make fmt`, and `make lint` before handing off changes.

If native Go, PAM, or systemd build dependencies are unavailable, use the matching containerized targets: `make docker-build`, `make docker-test`, `make docker-fmt`, and `make docker-lint`. Use `make docker-generate` after templ changes. These targets build and reuse the repository's development image; do not assemble ad hoc build containers when they are available.

## Documentation

**update documentation** After any change to source code, update
relevant documentation in CLAUDE.md, README.md and the `yeti/` folder.
A task is not complete without reviewing and updating relevant
documentation.

**yeti/ directory** The `yeti/` directory contains documentation
written for AI consumption and context enhancement, not primarily for
humans. Jobs like `doc-maintainer` and `issue-worker` instruct the AI
to read `yeti/OVERVIEW.md` and related files for codebase context
before performing tasks. Write content in this directory to be
maximally useful to an AI agent understanding the codebase — detailed
architecture, patterns, and decision rationale rather than user-facing
guides.
