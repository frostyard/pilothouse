ARG GO_VERSION=1.27.4
FROM golang:${GO_VERSION}-bookworm

ARG GOLANGCI_LINT_VERSION=v2.15.0
ARG SVU_VERSION=v3.4.1
ARG GOVULNCHECK_VERSION=latest

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends cpio jq libpam0g-dev libsystemd-dev rpm shellcheck systemd \
    && rm -rf /var/lib/apt/lists/* \
    && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && go install github.com/caarlos0/svu/v3@${SVU_VERSION} \
    && go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION} \
    && install -d -m 1777 /cache/go-build /cache/go-mod /cache/golangci-lint

# Node runs the template gate (make verify-template) inside `make docker-ci`,
# at the release mise.toml pins; the index digest pins the exact image.
COPY --from=docker.io/library/node:24.22.2-bookworm-slim@sha256:01d566e8d94d8c619591d78652babfccd3c6992a3dffa9ed5ab39e11cf6c70e9 /usr/local/bin/node /usr/local/bin/node

# This image guarantees the packaging tools installed above, so anything that
# would otherwise degrade to a skip when a tool is missing must fail here
# instead. Reaches every docker-* target through the Makefile's DOCKER_RUN.
ENV PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1

WORKDIR /workspace
