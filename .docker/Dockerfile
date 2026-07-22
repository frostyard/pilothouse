ARG GO_VERSION=1.26.5
FROM golang:${GO_VERSION}-bookworm

ARG GOLANGCI_LINT_VERSION=v2.11.4
ARG SVU_VERSION=v3.4.1

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends libpam0g-dev libsystemd-dev \
    && rm -rf /var/lib/apt/lists/* \
    && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && go install github.com/caarlos0/svu/v3@${SVU_VERSION} \
    && install -d -m 1777 /cache/go-build /cache/go-mod /cache/golangci-lint

WORKDIR /workspace
