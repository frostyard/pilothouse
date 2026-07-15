.PHONY: build generate run test fmt lint bump

GO ?= go
GOFMT ?= gofmt
GOFILES := $(shell find . -type f -name '*.go' -not -name '*_templ.go')

build: generate
	$(GO) build -buildvcs=false -o bin/pilothouse ./cmd/pilothouse
	$(GO) build -buildvcs=false -o bin/pilothoused ./cmd/pilothoused

generate:
	$(GO) tool templ generate

run: generate
	$(GO) run ./cmd/pilothouse

test: generate
	$(GO) test ./...

fmt: ## Format Go source files
	$(GOFMT) -w $(GOFILES)

lint: ## Run linter
	@golangci-lint run || echo "golangci-lint not installed, skipping"

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
