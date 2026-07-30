# Stage 1: Build the React frontend
# Pinned to BUILDPLATFORM so the npm build runs natively on the build host
# (an amd64 CI runner) rather than under QEMU — its output (frontend/dist) is
# static JS/CSS and architecture-neutral, so there is nothing to gain from
# running it on the target arch.
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# Stage 2: Build the Go binary
# Pinned to BUILDPLATFORM so the Go toolchain itself runs natively on the build
# host and CROSS-COMPILES to the target arch via GOOS/GOARCH. This is the fast
# path enabled by CGO_ENABLED=0 + pure-Go SQLite (modernc.org/sqlite): no cgo
# C-toolchain and no QEMU are needed to produce an arm64 binary on an amd64
# runner. TARGETOS/TARGETARCH are injected automatically by buildx.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
# plugin-sdk is a sub-module pulled in via a `replace` directive in the root
# go.mod; its go.mod/go.sum must exist on disk before `go mod download` can
# resolve the replacement target.
COPY go.mod go.sum ./
COPY plugin-sdk/go.mod plugin-sdk/go.sum ./plugin-sdk/
RUN go mod download
COPY . .
# Overwrite frontend/dist with the freshly built assets so go:embed picks them up.
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /gleipnir . && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /gleipnirctl ./cmd/gleipnirctl

# Stage 3: Minimal runtime image
# NOT pinned to BUILDPLATFORM — this stage resolves to TARGETPLATFORM, so the
# runtime base matches the target arch and receives the cross-built binary.
FROM alpine:3.20
RUN apk --no-cache add ca-certificates
COPY --from=builder /gleipnir /usr/local/bin/gleipnir
COPY --from=builder /gleipnirctl /usr/local/bin/gleipnirctl
RUN mkdir -p /data
EXPOSE 8080
# Health check is public (no auth required) — see internal/http/api/router.go.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/api/v1/health || exit 1
CMD ["/usr/local/bin/gleipnir"]
