.PHONY: build generate run test race fmt lint bump docker-image docker-build docker-generate docker-run docker-test docker-race docker-fmt docker-lint

GO ?= go
GOFMT ?= gofmt
GOFILES := $(shell find . -type f -name '*.go' -not -name '*_templ.go')
GO_VERSION ?= 1.26.5
GOLANGCI_LINT_VERSION ?= v2.11.4
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

lint: ## Run linter
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed, skipping"; fi

docker-image: ## Build the development image used by docker-* targets
	$(DOCKER) build \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION) \
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

bump: ## generate a new version with svu
	@$(MAKE) build
	@$(MAKE) test
	@$(MAKE) fmt
	$(MAKE) lint
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "Working directory is not clean. Please commit or stash changes before bumping version."; \
		exit 1; \
	fi
	@echo "Creating new tag..."
	@version=$$(svu next); \
		git tag -a $$version -m "Version $$version"; \
		echo "Tagged version $$version"; \
		echo "Pushing tag $$version to origin..."; \
		git push origin $$version
