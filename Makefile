.PHONY: build generate run test race fmt format-check lint lint-version-check verify verify-template corrections-base check package bump bump-preflight bump-verify docker-bump-verify docker-next-version docker-tools-check test-bump docker-image docker-build docker-generate docker-run docker-test docker-race docker-fmt docker-lint verify-packages verify-package-install help

GO ?= go
GOFMT ?= gofmt
# GOFILES is expanded by the shell when a recipe runs, not by make while it
# reads this file, so `make -n` prints the find command instead of the
# repository's current source-file list. That keeps a dry run's text an honest,
# greppable record of what a gate invokes rather than of which files happen to
# exist. Overriding it on the command line still works, as scripts/bump_test.sh
# does when it drives format-check with a stub gofmt.
GOFILES = $$(find . -type f -name '*.go' -not -name '*_templ.go')
GO_VERSION ?= 1.27.3
# The Go release this module is built with, from go.mod's go line: the only Go
# pin (mise reads the same line). golangci-lint must be built with a Go at
# least this new, or its embedded gofmt and typechecker disagree with the
# toolchain.
GO_MOD_VERSION := $(strip $(shell sed -n 's/^go \(.*\)/\1/p' go.mod))
# Pinned golangci-lint release, read from mise.toml — mise.toml is the only pin:
# `mise install` provisions it locally and in CI (jdx/mise-action), verified
# against mise.lock. Bump it there, never here. The docker image's
# `go install ...@vX.Y.Z` build arg needs the `v` prefix, while
# `golangci-lint version --short` prints the bare "X.Y.Z"
# (GOLANGCI_LINT_PIN).
GOLANGCI_LINT_VERSION ?= v$(strip $(shell sed -n 's/^golangci-lint = "\(.*\)"/\1/p' mise.toml))
GOLANGCI_LINT_PIN = $(patsubst v%,%,$(GOLANGCI_LINT_VERSION))
# The major version of the Node on PATH (mise.toml pins it), checked by
# make verify-template so the template gate needs no package install.
NODE_MAJOR := $(shell node -p "process.versions.node.split('.')[0]" 2>/dev/null)
SVU_VERSION ?= v3.4.1
DOCKER ?= docker
DOCKER_IMAGE ?= pilothouse-dev:go$(GO_VERSION)
DOCKER_CACHE_PREFIX ?= pilothouse
# INSTALL_IMAGE is the container image `verify-package-install` installs the
# built artifacts inside. It has no default on purpose: the target refuses to
# assume a distro family and names the two digest-pinned references instead.
INSTALL_IMAGE ?=
ARTIFACT_DIR ?= dist
DOCKER_RUN = $(DOCKER) run --rm \
	--user "$(shell id -u):$(shell id -g)" \
	--env HOME=/tmp \
	--env GOCACHE=/cache/go-build \
	--env GOMODCACHE=/cache/go-mod \
	--env GOTOOLCHAIN=local \
	--env GOLANGCI_LINT_CACHE=/cache/golangci-lint \
	--mount "type=bind,source=$(CURDIR),target=/workspace" \
	--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-go-build,target=/cache/go-build" \
	--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-go-mod,target=/cache/go-mod" \
	--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-golangci-lint,target=/cache/golangci-lint" \
	--workdir /workspace \
	$(DOCKER_IMAGE)

build: generate
	$(GO) build -buildvcs=false -o bin/pilothouse ./cmd/pilothouse
	$(GO) build -tags sdjournal -buildvcs=false -o bin/pilothoused ./cmd/pilothoused

generate:
	$(GO) tool templ generate

run: generate
	$(GO) run ./cmd/pilothouse

test: generate
	$(GO) test ./...
	$(GO) test -tags sdjournal ./internal/modules/logs/journal/...

race: generate
	$(GO) test -race -short ./...

fmt: ## Format Go source files
	$(GOFMT) -w $(GOFILES)

format-check: ## Verify Go source formatting without rewriting files
	@files="$$($(GOFMT) -l $(GOFILES))" || exit $$?; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

# Fail unless the installed golangci-lint is the mise.toml pin and was built
# with a Go no older than go.mod's go line. The patch level is not compared:
# golangci-lint releases are built with the Go patch current at their release,
# while go.mod's go line is a patch-level release (1.27.3). A patch difference
# does not change gofmt's output.
lint-version-check: ## Fail unless golangci-lint is the mise.toml pin, built with a Go no older than go.mod's
	@test -n "$(GOLANGCI_LINT_VERSION)" || { echo "mise.toml pins no golangci-lint"; exit 1; }
	@installed="$$(golangci-lint version --short 2>/dev/null)" || { \
		echo "golangci-lint $(GOLANGCI_LINT_PIN) is required (not installed; run: mise install)"; exit 1; }; \
	if [ "$$installed" != "$(GOLANGCI_LINT_PIN)" ]; then \
		echo "expected golangci-lint $(GOLANGCI_LINT_PIN), found $$installed (run: mise install)"; \
		exit 1; \
	fi; \
	built="$$(golangci-lint version 2>/dev/null | sed -n 's/.*built with go\([0-9.]*\).*/\1/p')"; \
	built_minor="$$(printf '%s' "$$built" | cut -d. -f1,2)"; \
	module_minor="$$(printf '%s' "$(GO_MOD_VERSION)" | cut -d. -f1,2)"; \
	if [ -n "$$built" ] && [ "$$(printf '%s\n%s\n' "$$module_minor" "$$built_minor" | sort -V | head -1)" != "$$module_minor" ]; then \
		echo "golangci-lint $(GOLANGCI_LINT_PIN) was built with go$$built, older than go.mod's go$(GO_MOD_VERSION): bump golangci-lint first"; \
		exit 1; \
	fi

lint: ## Run linter
	@if ! command -v golangci-lint >/dev/null 2>&1; then echo "golangci-lint not found on PATH; run 'mise install' or 'make docker-lint'." >&2; exit 1; fi
	@$(MAKE) --no-print-directory lint-version-check
	golangci-lint run

# Check the agent surfaces, docs index, risk paths, corrections and vendored
# copies (needs Node.js 22 or later, from mise.toml)
verify-template: ## Check agent surfaces, docs index, risk paths, corrections and vendored copies (needs Node 22+)
	@if ! command -v node >/dev/null 2>&1; then echo "FAIL: verify-template requires Node.js 22 or later (run: mise install)"; exit 1; fi
	@if [ "$(NODE_MAJOR)" = "" ] || [ "$(NODE_MAJOR)" -lt 22 ]; then echo "FAIL: verify-template requires Node.js 22 or later (found: $$(node --version 2>/dev/null || echo missing)); run: mise install"; exit 1; fi
	node scripts/check-template.mjs
	node --test test/*.test.mjs

docker-image: ## Build the development image used by docker-* targets
	$(DOCKER) build \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION) \
		--build-arg SVU_VERSION=$(SVU_VERSION) \
		--tag $(DOCKER_IMAGE) \
		--file .docker/Dockerfile \
		.docker

docker-build: docker-image ## Build both binaries in Docker with PAM and systemd headers
	$(DOCKER_RUN) make build

docker-generate: docker-image ## Generate templ output in Docker
	$(DOCKER_RUN) make generate

docker-run: docker-image ## Run the web process in Docker using host networking
	$(DOCKER) run --rm \
		--user "$(shell id -u):$(shell id -g)" \
		--network host \
		--env HOME=/tmp \
		--env GOCACHE=/cache/go-build \
		--env GOMODCACHE=/cache/go-mod \
		--env GOTOOLCHAIN=local \
		--mount "type=bind,source=$(CURDIR),target=/workspace" \
		--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-go-build,target=/cache/go-build" \
		--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-go-mod,target=/cache/go-mod" \
		--workdir /workspace \
		$(DOCKER_IMAGE) \
		make run

docker-test: docker-image ## Run the test suite in Docker
	$(DOCKER_RUN) make test

docker-race: docker-image ## Run the race detector suite in Docker
	$(DOCKER_RUN) make race

docker-fmt: docker-image ## Format Go source files in Docker
	$(DOCKER_RUN) make fmt

docker-lint: docker-image ## Run golangci-lint in Docker
	$(DOCKER_RUN) golangci-lint run

verify: generate ## Non-mutating gate: tidy diff, vet, format check, lint, template gate, govulncheck, tests
	@echo "==> go mod tidy check" && go mod tidy -diff
	@echo "==> go vet" && go vet ./...
	@echo "==> format check" && $(MAKE) format-check
	@echo "==> lint" && $(MAKE) lint
	@echo "==> template gate" && $(MAKE) verify-template
	@echo "==> govulncheck" && { command -v govulncheck >/dev/null 2>&1 && govulncheck ./... || go run golang.org/x/vuln/cmd/govulncheck@latest ./...; }
	@echo "==> tests" && $(MAKE) test

# One command mirrors CI's credential-free gates: verify (the tidy diff, vet,
# the gofmt check, the pinned golangci-lint, the template gate, govulncheck and
# the tests), then the corrections check against BASE (corrections-base), then
# the race detector, then the build, in fail-fast order.
ci: verify ## Run every CI gate that runs without credentials (lint, vuln, tidy, vet, fmt, template gate, corrections, test, race, build); the packaging.yml gate needs GORELEASER_KEY and cannot run here
	$(MAKE) --no-print-directory corrections-base
	@echo "==> race" && $(MAKE) race
	@echo "==> build" && $(MAKE) build
	@echo "all CI gates passed"

# The corrections check compares .memory/corrections.jsonl with BASE: lines are
# append-only (only an empty promoted_to may be filled in). The contract starts
# with the gate: a BASE that does not hold scripts/check-corrections.mjs had no
# such rule, so the comparison is skipped with a notice. A BASE that does not
# resolve to a commit is an error, never an old base: a typo or an unfetched ref
# must not turn the comparison into a silent pass.
corrections-base: ## Check that .memory/corrections.jsonl is append-only against BASE
	@if [ -z "$(BASE)" ]; then exit 0; fi; \
	if ! git rev-parse --verify --quiet "$(BASE)^{commit}" >/dev/null; then \
		echo "FAIL: BASE $(BASE) does not resolve to a commit (fetch it first)"; \
		exit 1; \
	fi; \
	if git cat-file -e "$(BASE):scripts/check-corrections.mjs" 2>/dev/null; then \
		node scripts/check-corrections.mjs --base "$(BASE)"; \
	else \
		echo "==> corrections: BASE $(BASE) predates the corrections gate; append-only comparison skipped"; \
	fi

check: fmt verify ## Developer gate: format, then verify

# docker-ci passes BASE into the container, so `make docker-ci BASE=origin/main`
# runs the same corrections comparison as `make ci BASE=origin/main`. The
# container sees only the bind-mounted checkout: BASE must resolve there (a
# clone whose .git is a directory), or corrections-base fails rather than
# skipping. In a git worktree, run `make corrections-base BASE=...` on the host.
docker-ci: docker-image ## Run every CI gate that runs without credentials inside the development image; the packaging.yml gate needs GORELEASER_KEY and cannot run here
	$(DOCKER_RUN) make ci BASE="$(BASE)"

bump-preflight: ## Verify that main is clean and synchronized
	@DOCKER="$(DOCKER)" ./scripts/bump.sh preflight

bump-verify: ## Run strict release checks inside the development image
	@$(MAKE) build
	@$(MAKE) test
	@$(MAKE) format-check
	golangci-lint run

docker-bump-verify: docker-image ## Run all release checks in Docker
	@set -eu; \
	source=$$(mktemp -d); \
	trap 'rm -rf "$$source"' EXIT HUP INT TERM; \
	if ! git clone --no-local "$(CURDIR)" "$$source" >/dev/null; then \
		printf '%s\n' 'bump: could not prepare isolated verification source.' >&2; \
		exit 1; \
	fi; \
	rm -rf "$$source/.git"; \
	$(DOCKER) run --rm \
		--user "$(shell id -u):$(shell id -g)" \
		--env HOME=/tmp \
		--env GOCACHE=/cache/go-build \
		--env GOMODCACHE=/cache/go-mod \
		--env GOTOOLCHAIN=local \
		--env GOLANGCI_LINT_CACHE=/cache/golangci-lint \
		--mount "type=bind,source=$$source,target=/workspace" \
		--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-go-build,target=/cache/go-build" \
		--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-go-mod,target=/cache/go-mod" \
		--mount "type=volume,source=$(DOCKER_CACHE_PREFIX)-golangci-lint,target=/cache/golangci-lint" \
		--workdir /workspace \
		$(DOCKER_IMAGE) \
		make bump-verify

docker-next-version: ## Calculate the next version with pinned svu
	@set -eu; \
	$(MAKE) --no-print-directory docker-image >&2; \
	repo=$$(mktemp -d); \
	trap 'rm -rf "$$repo"' EXIT HUP INT TERM; \
	if ! git clone --mirror --no-local "$(CURDIR)" "$$repo" >/dev/null; then \
		printf '%s\n' 'bump: could not prepare isolated version repository.' >&2; \
		exit 1; \
	fi; \
	if ! git -C "$$repo" remote remove origin; then \
		printf '%s\n' 'bump: could not remove remote configuration from version repository.' >&2; \
		exit 1; \
	fi; \
	$(DOCKER) run --rm \
		--user "$(shell id -u):$(shell id -g)" \
		--env HOME=/tmp \
		--mount "type=bind,source=$$repo,target=/repository,readonly" \
		--workdir /repository \
		$(DOCKER_IMAGE) \
		svu next

docker-tools-check: docker-image ## Verify release and packaging tools are executable in Docker
	$(DOCKER_RUN) sh -c 'svu --version && golangci-lint version && for t in dpkg-deb rpm rpmbuild rpm2archive tar; do command -v $$t || exit 1; done && echo "PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=$$PILOTHOUSE_REQUIRE_PACKAGING_TOOLS"'

package: ## Build snapshot .deb/.rpm into dist/ with goreleaser Pro v2 (publishes nothing, needs no tag)
	@set -u; \
	required='make package: the goreleaser Pro distribution at major version 2 is required; see https://goreleaser.com/pro/'; \
	if ! command -v goreleaser >/dev/null 2>&1; then \
		printf '%s\n%s\n' 'make package: goreleaser was not found on PATH.' "$$required" >&2; \
		exit 1; \
	fi; \
	if ! banner=$$(goreleaser --version 2>&1); then \
		printf '%s\n%s\n' 'make package: the goreleaser version could not be determined: `goreleaser --version` failed.' "$$required" >&2; \
		exit 1; \
	fi; \
	if ! printf '%s\n' "$$banner" | grep -qE '(^|[^-[:alnum:]])goreleaser-pro([^-[:alnum:]]|$$)'; then \
		printf '%s\n%s\n' 'make package: the goreleaser found on PATH is not the Pro distribution.' "$$required" >&2; \
		exit 1; \
	fi; \
	found=$$(printf '%s\n' "$$banner" | sed -n 's/^GitVersion:[[:space:]]*v\{0,1\}\([^[:space:]][^[:space:]]*\).*/\1/p' | head -n 1); \
	major=$${found%%.*}; \
	case "$$major" in \
	''|*[!0-9]*) \
		printf 'make package: the goreleaser version could not be determined from `goreleaser --version` (GitVersion: %s).\n%s\n' "$${found:-<absent>}" "$$required" >&2; \
		exit 1;; \
	esac; \
	if [ "$$major" != "2" ]; then \
		printf 'make package: the goreleaser Pro binary on PATH reports version %s, whose major version is %s.\n%s\n' "$$found" "$$major" "$$required" >&2; \
		exit 1; \
	fi
	goreleaser release --snapshot --clean

verify-packages: ## Report contract findings for built .deb/.rpm artifacts in dist/ (outside ci; fails when dist/ is empty)
	$(GO) run ./cmd/$@

# verify-package-install pins two container details that are easy to drop and
# hard to debug. --platform linux/amd64 is required because both digest-pinned
# references are multi-architecture image indexes (the Debian one carries 15
# architectures); on an ARM host Docker would otherwise select the ARM variant
# and try to install amd64 artifacts into the wrong userland. --user 0:0 is
# required because `docker run` honours an image's configured USER, while
# installing packages, creating the service account and reading PAM stacks all
# need root; an INSTALL_IMAGE declaring a non-root USER would fail confusingly
# instead of validating anything.
verify-package-install: ## Install the built .deb/.rpm inside INSTALL_IMAGE and validate the result (outside ci; needs Docker, the network and artifacts in ARTIFACT_DIR)
	@if [ -z '$(INSTALL_IMAGE)' ]; then \
		printf '%s\n' 'make verify-package-install: INSTALL_IMAGE is unset and this target assumes no image.' 'A container image reference is required; these two are the digest-pinned images this validation targets:' '  make verify-package-install INSTALL_IMAGE=debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc' '  make verify-package-install INSTALL_IMAGE=fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814' >&2; \
		exit 1; \
	fi
	@if [ ! -d '$(ARTIFACT_DIR)' ]; then \
		printf '%s\n' 'make verify-package-install: the artifact directory $(ARTIFACT_DIR) does not exist; `make package` is the local producer that fills dist/.' >&2; \
		exit 1; \
	fi
	$(DOCKER) run --rm \
		--platform linux/amd64 \
		--user 0:0 \
		--mount "type=bind,source=$(CURDIR)/packaging,target=/packaging,readonly" \
		--mount "type=bind,source=$(abspath $(ARTIFACT_DIR)),target=/artifacts,readonly" \
		--workdir / \
		$(INSTALL_IMAGE) \
		/packaging/verify-install.sh /artifacts

help: ## Print every target that carries a description comment
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test-bump: ## Test release orchestration without publishing
	bash scripts/bump_test.sh

bump: ## Verify and publish the next version tag
	@DOCKER="$(DOCKER)" \
	BUMP_VERIFY_COMMAND='$(MAKE) --no-print-directory docker-bump-verify' \
	BUMP_VERSION_COMMAND='$(MAKE) --silent --no-print-directory docker-next-version' \
	./scripts/bump.sh release
