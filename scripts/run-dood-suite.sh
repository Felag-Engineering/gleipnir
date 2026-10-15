#!/usr/bin/env bash
set -euo pipefail

# Run the DooD suite (internal/plugin/dood, issue #985): the one way CI and
# developers both run it.
#
# Under rootless Podman a process on the runner host cannot reach an instance
# gateway or a plugin container's IP, so the suite's test binary runs INSIDE a
# container that has the runtime socket mounted, and the assembly self-attaches
# that container to each instance network (#958). That is the same shape as a
# production DooD deployment.
#
# Flow: compile the suite on the host (`go test -c`, static) -> create a runner
# container with the socket mounted -> copy the binary and the signed
# conformance-stub bundles in -> run it there and propagate its exit status.
#
# The runner needs no network and no toolchain, and nothing is pulled at suite
# time: the bundles carry their own image archives (make conformance-stub-bundle)
# and the isolation probes reuse the runner image.
#
# Prerequisite: make conformance-stub-bundle
#
# Environment:
#   CONTAINER_ENGINE        docker or podman. Default: docker if its daemon
#                           answers, else podman.
#   DOOD_RUNNER_IMAGE       image the suite runs in; it also runs the isolation
#                           probes, so it needs a shell and nc (default:
#                           docker.io/library/golang:1.26-alpine, the base the
#                           stub build already uses).
#   DOOD_PROBE_IMAGE        run the isolation probes in a different local image.
#   DOOD_SOCKET             runtime socket on this host (default: the engine's
#                           own, /var/run/docker.sock for docker, the rootless
#                           user socket for podman).
#   GLEIPNIR_CONTAINER_RUNTIME_MODE
#                           docker or rootless-podman (default: from the engine).
#   CONFORMANCE_DIST_DIR    where the bundles are (default: <repo>/dist).
#   DOOD_TEST_ARGS          extra words for the test binary (-test.v is already
#                           set), e.g. -test.run=...

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
dist_dir=${CONFORMANCE_DIST_DIR:-$repo_root/dist}
runner_image=${DOOD_RUNNER_IMAGE:-docker.io/library/golang:1.26-alpine}

detect_engine() {
    if [ -n "${CONTAINER_ENGINE:-}" ]; then
        command -v "$CONTAINER_ENGINE" >/dev/null ||
            { echo "error: CONTAINER_ENGINE=$CONTAINER_ENGINE not found on PATH" >&2; return 1; }
        echo "$CONTAINER_ENGINE"
        return
    fi
    if command -v docker >/dev/null && docker info >/dev/null 2>&1; then
        echo docker
        return
    fi
    if command -v podman >/dev/null; then
        echo podman
        return
    fi
    echo "error: no usable container engine (docker daemon not answering, podman not installed); set CONTAINER_ENGINE" >&2
    return 1
}

engine=$(detect_engine)

case "$engine" in
docker)
    mode=${GLEIPNIR_CONTAINER_RUNTIME_MODE:-docker}
    socket=${DOOD_SOCKET:-/var/run/docker.sock}
    ;;
podman)
    mode=${GLEIPNIR_CONTAINER_RUNTIME_MODE:-rootless-podman}
    socket=${DOOD_SOCKET:-$(podman info --format '{{.Host.RemoteSocket.Path}}')}
    socket=${socket#unix://}
    ;;
*)
    echo "error: unsupported CONTAINER_ENGINE=$engine (want docker or podman)" >&2
    exit 1
    ;;
esac
if [ ! -S "$socket" ]; then
    echo "error: no runtime socket at $socket; set DOOD_SOCKET (podman needs: systemctl --user start podman.socket)" >&2
    exit 1
fi

if [ ! -f "$dist_dir/conformance-stub-all.tgz" ]; then
    echo "error: $dist_dir/conformance-stub-all.tgz is missing; run: make conformance-stub-bundle" >&2
    exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/dood-suite.XXXXXX")
mkdir "$work/stage"
runner=
cleanup() {
    if [ -n "$runner" ]; then
        "$engine" rm -f "$runner" >/dev/null 2>&1 || true
    fi
    rm -rf "$work"
}
trap cleanup EXIT

echo "==> engine: $engine (mode $mode, socket $socket)"

# Static, so the runner image's libc does not matter. -tags dood,substratev2 is
# what selects the suite: without them the package compiles to nothing.
echo "==> compiling the suite"
(
    cd "$repo_root"
    CGO_ENABLED=0 go test -c -tags dood,substratev2 -o "$work/stage/dood.test" ./internal/plugin/dood/
)
cp -R "$dist_dir" "$work/stage/bundles"

# The container's own sysctls must satisfy the forwarding check the reconciler
# runs before self-attach (forwarding off, IPv6 off): a container attached to
# two isolated networks with forwarding on could route between them. A kernel
# without IPv6 has no such sysctls to set, and the check skips them too.
echo "==> creating the runner ($runner_image)"
runner_args=(
    --security-opt label=disable
    --sysctl net.ipv4.ip_forward=0
    -v "$socket:/run/dood/runtime.sock"
    -e GLEIPNIR_CONTAINER_RUNTIME_MODE="$mode"
    -e GLEIPNIR_CONTAINER_SOCKET=/run/dood/runtime.sock
    -e DOOD_BUNDLE_DIR=/dood/bundles
)
if [ -d /proc/sys/net/ipv6 ]; then
    runner_args+=(
        --sysctl net.ipv6.conf.all.disable_ipv6=1
        --sysctl net.ipv6.conf.default.disable_ipv6=1
    )
fi
for var in DOOD_PROBE_IMAGE DOOD_SUBNET_POOL; do
    if [ -n "${!var:-}" ]; then
        runner_args+=(-e "$var=${!var}")
    fi
done
# DOOD_TEST_ARGS is deliberately word-split.
# shellcheck disable=SC2086
runner=$("$engine" create "${runner_args[@]}" "$runner_image" /dood/dood.test -test.v -test.timeout 10m ${DOOD_TEST_ARGS:-})

# Copied rather than bind-mounted: this script may itself run in a container,
# where its paths mean nothing to the daemon.
"$engine" cp "$work/stage" "$runner:/dood" >/dev/null

echo "==> running the suite"
status=0
"$engine" start -a "$runner" || status=$?
exit "$status"
