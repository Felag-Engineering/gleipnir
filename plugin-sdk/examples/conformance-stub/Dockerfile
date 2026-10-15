# Build from the repository root (the build needs plugin-sdk, which this module
# resolves through a workspace, not a published version):
#
#   docker build -f plugin-sdk/examples/conformance-stub/Dockerfile -t conformance-stub .
#
# Pinned to BUILDPLATFORM, like the root Dockerfile: the toolchain runs natively
# and cross-compiles, which CGO_ENABLED=0 makes possible.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY plugin-sdk/go.mod plugin-sdk/go.sum ./plugin-sdk/
COPY plugin-sdk/examples/conformance-stub/go.mod ./plugin-sdk/examples/conformance-stub/
# Dependencies are downloaded for plugin-sdk on its own (GOWORK=off): its go.sum
# already pins everything the stub adds (yaml.v3), and the stub's requirement on
# plugin-sdk@v0.0.0 is a local replace (see its go.mod), never a download.
RUN go work init ./plugin-sdk ./plugin-sdk/examples/conformance-stub && \
    cd plugin-sdk && GOWORK=off go mod download
COPY plugin-sdk/ ./plugin-sdk/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /conformance-stub ./plugin-sdk/examples/conformance-stub
# The runtime image has no shell or mkdir, so the data directory is made here
# and handed to the nonroot user (uid 65532).
RUN mkdir -p /data/events /data/tasks && chown -R 65532:65532 /data

# Distroless static: no shell, no package manager, runs as nonroot by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /conformance-stub /conformance-stub
COPY --from=builder --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080
# Serves until signalled; configuration is environment-only (see README.md).
ENTRYPOINT ["/conformance-stub"]
