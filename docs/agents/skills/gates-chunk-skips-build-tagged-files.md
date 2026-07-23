# gates_chunk's go vet/test does not compile build-tag-gated files

**When it applies:** Writing or reviewing per-chunk gates for this repo
(`.mill/config.json`'s `gates_chunk`), or any chunk that adds or edits a file
behind a `//go:build` constraint (e.g. an `sdjournal`-tagged file used by
`cmd/pilothoused`).

**What to do:** `gates_chunk` runs `go vet ./...` and `go test ./...` with
the default build tag set. Per the Makefile, the real `pilothoused` binary is
built with `-tags sdjournal` (`make build` / `make docker-build`), which
AGENTS.md requires before handoff — but the mill's chunk-level gates never
pass that tag. A chunk can add or change a tag-gated file, pass
`gates_chunk` cleanly, and still never have been compiled, because the
default `go build`/`go vet`/`go test` invocation silently excludes files
behind an unsatisfied build tag. When a chunk touches a build-tag-gated file,
add an explicit tagged build step (`go build -tags sdjournal ./...`, or
`make build`/`make docker-build`) to that chunk's acceptance/gate instead of
relying on `gates_chunk` alone to prove it compiles.

**Learned from:** mill run for issue #50, plan revision round 4 — chunk c3
added an `sdjournal`-tagged file, but the per-chunk gates
(`make generate`, `gofmt`, `go vet ./...`, `go test ./...`) plus the deep
gate (`make docker-test`) never exercised the `sdjournal` build path that
`make build`/`docker-build` actually compiles.
