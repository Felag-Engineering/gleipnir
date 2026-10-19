# Build from the repository root (the module resolves plugin-sdk through a
# local replace, so the build needs both directories):
#
#   docker build -f plugins/slack-v2/Dockerfile -t slack-v2 .
#
# Pinned to BUILDPLATFORM, like the root Dockerfile: the toolchain runs natively
# and cross-compiles, which CGO_ENABLED=0 makes possible.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY plugin-sdk/go.mod plugin-sdk/go.sum ./plugin-sdk/
COPY plugins/slack-v2/go.mod plugins/slack-v2/go.sum ./plugins/slack-v2/
# Dependencies are fetched against the plugin's own go.sum (GOWORK=off): the
# repository workspace file is not copied in, and the plugin-sdk requirement is
# a local replace, never a download.
RUN cd plugins/slack-v2 && GOWORK=off go mod download
COPY plugin-sdk/ ./plugin-sdk/
COPY plugins/slack-v2/ ./plugins/slack-v2/
RUN cd plugins/slack-v2 && GOWORK=off CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /slack-v2 .

# The runtime image has no shell or mkdir, so the data directory (durable
# channel/request tasks) is made here and handed to the nonroot user (uid 65532).
RUN mkdir -p /data && chown -R 65532:65532 /data

# Distroless static: no shell, no package manager, runs as nonroot by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /slack-v2 /slack-v2
COPY --from=builder --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080
# Configuration is environment-only (see README.md).
ENTRYPOINT ["/slack-v2"]
