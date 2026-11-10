# syntax=docker/dockerfile:1.7
# Multi-stage build for the agent: a static binary on distroless, running as
# UID 65532 with no shell.

FROM golang:1.26.10-alpine AS builder

# Set by the release workflow so `ktm-agent --version` reports the tag.
ARG VERSION=dev

WORKDIR /src

# Module downloads are cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

# CGO off: distroless static needs a statically linked binary.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/ktm-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot

# The image runs as 65532:65532; the chart's runAsUser and fsGroup must match.
COPY --from=builder /out/ktm-agent /ktm-agent

ENTRYPOINT ["/ktm-agent"]
