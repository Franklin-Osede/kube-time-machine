# syntax=docker/dockerfile:1.7
# Multi-stage build for the in-cluster agent.
# Result: ~41 MB distroless image (static client-go binary), runs as UID
# 65532, no shell, no apt.

FROM golang:1.27.1-alpine AS builder

# VERSION is injected by the release workflow via --build-arg so that
# `ktm-agent --version` reports the real tag inside the container image.
# Defaults to "dev" for local `docker build` runs without the arg.
ARG VERSION=dev

WORKDIR /src

# Cache module downloads — only invalidates when go.mod / go.sum move.
COPY go.mod go.sum ./
RUN go mod download

# Source last so unrelated edits don't bust the mod cache.
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

# CGO off so the binary is statically linked and runnable in distroless
# static. -trimpath gives reproducible paths in stack traces.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/ktm-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot

# distroless-nonroot already declares USER 65532:65532. The Deployment
# pins the same UID via securityContext.runAsUser so the PVC's fsGroup
# can be set correctly.
COPY --from=builder /out/ktm-agent /ktm-agent

ENTRYPOINT ["/ktm-agent"]
