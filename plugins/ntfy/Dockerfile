# Build from the repository root (the build needs plugin-sdk, which this module
# resolves through a workspace, not a published version):
#
#   docker build -f plugins/ntfy/Dockerfile -t ghcr.io/felag-engineering/gleipnir-ntfy:dev .
#
# Pinned to BUILDPLATFORM, like the root Dockerfile: the toolchain runs natively
# and cross-compiles, which CGO_ENABLED=0 makes possible.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY plugin-sdk/go.mod plugin-sdk/go.sum ./plugin-sdk/
COPY plugins/ntfy/go.mod plugins/ntfy/go.sum ./plugins/ntfy/
# Dependencies are downloaded for plugin-sdk on its own (GOWORK=off): its go.sum
# already pins everything this module needs, and the requirement on
# plugin-sdk@v0.0.0 is a local replace (see go.mod), never a download.
RUN go work init ./plugin-sdk ./plugins/ntfy && \
    cd plugin-sdk && GOWORK=off go mod download
COPY plugin-sdk/ ./plugin-sdk/
COPY plugins/ntfy/ ./plugins/ntfy/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /ntfy ./plugins/ntfy

# Distroless static: no shell, no package manager, runs as nonroot by default.
# No HEALTHCHECK: the host probes the MCP endpoint itself.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /ntfy /ntfy
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/ntfy"]
