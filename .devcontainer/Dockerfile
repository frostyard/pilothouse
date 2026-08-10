# Dev image for building and testing pilothouse locally.
#
# pilothoused is a cgo binary: it links libpam (PAM auth in internal/auth/pam)
# and, when built with `-tags sdjournal`, libsystemd (the journal reader in
# internal/modules/services/journal). Both dev headers are installed here so
# `make build` (which passes -tags sdjournal) works out of the box.
#
# The Go version tracks go.mod / CI (`setup-go` uses 1.26.5).
FROM golang:1.26.5-bookworm

ARG GOLANGCI_LINT_VERSION=v2.11.4
ARG GOVULNCHECK_VERSION=v1.6.0

# System build dependencies:
#   build-essential  - C toolchain for cgo
#   libpam0g-dev     - PAM headers/lib (pilothoused, always)
#   libsystemd-dev   - systemd journal headers (pilothoused, -tags sdjournal)
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential \
      libpam0g-dev \
      libsystemd-dev \
      git \
      curl \
      ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# CI-parity tooling. Go's module checksum database verifies both pinned tools.
# templ is provided by the module's `go tool` directive
# (`go tool templ generate`), so it needs no install here.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
 && go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}

# Keep the Go toolchain and go-installed tools (golangci-lint, govulncheck) on
# PATH for login shells too. `bash -lc` re-sources /etc/profile, which resets
# PATH and would otherwise drop the image's ENV PATH.
RUN printf 'export PATH=/usr/local/go/bin:%s/bin:$PATH\n' "$(go env GOPATH)" \
      > /etc/profile.d/go-path.sh
