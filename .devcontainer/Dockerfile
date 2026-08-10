# Dev image for building and testing pilothouse locally.
#
# pilothoused is a cgo binary: it links libpam (PAM auth in internal/auth/pam)
# and, when built with `-tags sdjournal`, libsystemd (the journal reader in
# internal/modules/services/journal). Both dev headers are installed here so
# `make build` (which passes -tags sdjournal) works out of the box.
#
# The Go version tracks go.mod / CI (`setup-go` uses 1.26.5).
FROM golang:1.26.5-bookworm

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

# CI-parity tooling. golangci-lint is installed from the official prebuilt
# binary (matches the golangci-lint-action, and avoids source-build flakiness).
# Pin the installer to the v2.11.4 commit and verify it before execution; the
# installer then verifies the release archive against its published checksum.
# govulncheck matches the Security Scan job. templ is provided by the module's
# `go tool` directive (`go tool templ generate`), so it needs no install here.
RUN curl -sSfL -o /tmp/golangci-lint-install.sh \
      https://raw.githubusercontent.com/golangci/golangci-lint/8f3b0c7ed018e57905fbd873c697e0b1ede605a5/install.sh \
 && echo "edfa587f31bde70db161d1e5b783e086a1627d7e2f7c91de5f7cca79bcdf8631  /tmp/golangci-lint-install.sh" \
      | sha256sum -c - \
 && sh /tmp/golangci-lint-install.sh -b "$(go env GOPATH)/bin" v2.11.4 \
 && rm -f /tmp/golangci-lint-install.sh \
 && go install golang.org/x/vuln/cmd/govulncheck@v1.6.0

# Keep the Go toolchain and go-installed tools (golangci-lint, govulncheck) on
# PATH for login shells too. `bash -lc` re-sources /etc/profile, which resets
# PATH and would otherwise drop the image's ENV PATH.
RUN printf 'export PATH=/usr/local/go/bin:%s/bin:$PATH\n' "$(go env GOPATH)" \
      > /etc/profile.d/go-path.sh
