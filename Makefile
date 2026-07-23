.PHONY: build generate run test race fmt format-check lint bump bump-preflight bump-verify docker-bump-verify docker-next-version docker-tools-check test-bump docker-image docker-build docker-generate docker-run docker-test docker-race docker-fmt docker-lint

GO ?= go
GOFMT ?= gofmt
GOFILES := $(shell find . -type f -name '*.go' -not -name '*_templ.go')
GO_VERSION ?= 1.26.5
GOLANGCI_LINT_VERSION ?= v2.11.4
SVU_VERSION ?= v3.4.1
DOCKER ?= docker
DOCKER_IMAGE ?= pilothouse-dev:go$(GO_VERSION)
DOCKER_CACHE_PREFIX ?= pilothouse
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

race: generate
	$(GO) test -race -short ./internal/... -run "^Test[^I]" -skip "Integration"

fmt: ## Format Go source files
	$(GOFMT) -w $(GOFILES)

format-check: ## Verify Go source formatting without rewriting files
	@files="$$($(GOFMT) -l $(GOFILES))" || exit $$?; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

lint: ## Run linter
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed, skipping"; fi

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

ci: generate ## Run every gate CI runs (lint, vuln, tidy, vet, fmt, test, race, build)
	@echo "==> go mod tidy check" && go mod tidy -diff
	@echo "==> go vet" && go vet ./...
	@echo "==> format check" && $(MAKE) format-check
	@echo "==> lint" && $(MAKE) lint
	@echo "==> govulncheck" && { command -v govulncheck >/dev/null 2>&1 && govulncheck ./... || go run golang.org/x/vuln/cmd/govulncheck@latest ./...; }
	@echo "==> tests" && $(MAKE) test
	@echo "==> race" && $(MAKE) race
	@echo "==> build" && $(MAKE) build
	@echo "all CI gates passed"

docker-ci: docker-image ## Run every CI gate inside the development image
	$(DOCKER_RUN) make ci

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

docker-tools-check: docker-image ## Verify release tools are executable in Docker
	$(DOCKER_RUN) sh -c 'svu --version && golangci-lint version'

test-bump: ## Test release orchestration without publishing
	bash scripts/bump_test.sh

bump: ## Verify and publish the next version tag
	@DOCKER="$(DOCKER)" \
	BUMP_VERIFY_COMMAND='$(MAKE) --no-print-directory docker-bump-verify' \
	BUMP_VERSION_COMMAND='$(MAKE) --silent --no-print-directory docker-next-version' \
	./scripts/bump.sh release
