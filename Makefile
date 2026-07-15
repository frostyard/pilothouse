.PHONY: build generate run test

GO ?= go

build: generate
	$(GO) build -buildvcs=false -o bin/pilothouse ./cmd/pilothouse
	$(GO) build -buildvcs=false -o bin/pilothoused ./cmd/pilothoused

generate:
	$(GO) tool templ generate

run: generate
	$(GO) run ./cmd/pilothouse

test: generate
	$(GO) test ./...
