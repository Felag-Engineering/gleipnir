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
# Prerequisites: make conformance-stub-bundle slack-v2-bundle ntfy-bundle
#
# Environment:
#   CONTAINER_ENGINE        docker or podman. Default: docker if its daemon
#                           answers, else podman.
#   DOOD_RUNNER_IMAGE       image the suite runs in; it also runs the isolation
#                           probes, so it needs a shell and nc (default:
#                           mirror.gcr.io/library/golang:1.26-alpine, the
#                           stub's build base from a mirror: Docker Hub
#                           rate-limits shared CI runners).
#   DOOD_PROBE_IMAGE        run the isolation probes in a different local image.
#   DOOD_SOCKET             runtime socket on this host (default: the engine's
#                           own, /var/run/docker.sock for docker, the rootless
#                           user socket for podman).
#   GLEIPNIR_CONTAINER_RUNTIME_MODE
#                           docker or rootless-podman (default: from the engine).
#   CONFORMANCE_DIST_DIR    where the bundles are, ntfy.tgz included (default:
#                           <repo>/dist).
#   DOOD_TEST_ARGS          extra words for the test binary (-test.v is already
#                           set), e.g. -test.run=...

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
dist_dir=${CONFORMANCE_DIST_DIR:-$repo_root/dist}
runner_image=${DOOD_RUNNER_IMAGE:-mirror.gcr.io/library/golang:1.26-alpine}

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
    # The rootless user socket is where the CI action starts it and where the
    # suite's DetectPosture looks. Prefer it over asking podman, whose answer
    # depends on a running user session this script cannot assume.
    if [ -z "${DOOD_SOCKET:-}" ] && [ -S "${XDG_RUNTIME_DIR:-/nonexistent}/podman/podman.sock" ]; then
        DOOD_SOCKET=$XDG_RUNTIME_DIR/podman/podman.sock
    fi
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

for bundle in all dood-events dood-request; do
    if [ ! -f "$dist_dir/conformance-stub-$bundle.tgz" ]; then
        echo "error: $dist_dir/conformance-stub-$bundle.tgz is missing; run: make conformance-stub-bundle" >&2
        exit 1
    fi
done
if [ ! -f "$dist_dir/ntfy.tgz" ]; then
    echo "error: $dist_dir/ntfy.tgz is missing; run: make ntfy-bundle" >&2
    exit 1
fi

if [ ! -f "$dist_dir/slack-v2.tgz" ]; then
    echo "error: $dist_dir/slack-v2.tgz is missing; run: make slack-v2-bundle" >&2
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

# Everything needed to debug a red run, printed before cleanup removes the
# runner. Best effort throughout: a diagnostic that fails must not mask the
# suite's own exit status. The suite's labels: the reconciler stamps
# gleipnir.managed=true on the containers and networks it creates, and the
# isolation probes carry gleipnir.dood-test-run.
dump_diagnostics() {
    echo "::group::dood diagnostics"
    echo "--- $engine ps -a"
    "$engine" ps -a --format '{{.ID}} {{.Names}} {{.Status}} {{.Image}}' || true
    echo "--- $engine network ls"
    "$engine" network ls || true
    echo "--- runner state"
    "$engine" inspect --format '{{.Name}} status={{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}} error={{.State.Error}}' "$runner" || true
    local filter id
    for filter in label=gleipnir.managed=true label=gleipnir.dood-test-run; do
        for id in $("$engine" ps -aq --filter "$filter" 2>/dev/null || true); do
            echo "--- logs of container $id ($filter)"
            "$engine" logs --tail 200 "$id" 2>&1 || true
        done
    done
    echo "--- runner logs (tail)"
    "$engine" logs --tail 100 "$runner" 2>&1 || true
    echo "::endgroup::"
}

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
    --label gleipnir.dood-runner=1
    --sysctl net.ipv4.ip_forward=0
    -v "$socket:/run/dood/runtime.sock"
    -e GLEIPNIR_CONTAINER_RUNTIME_MODE="$mode"
    -e GLEIPNIR_CONTAINER_SOCKET=/run/dood/runtime.sock
    -e DOOD_BUNDLE_DIR=/dood/bundles
)
# Rootless podman starts a container on slirp4netns unless told otherwise,
# and podman refuses to connect a slirp4netns container to a bridge network —
# which is exactly what self-attach does to each instance network. So the
# runner starts on podman's default bridge, the way a compose-deployed
# Gleipnir sits on its project's bridge network.
if [ "$engine" = podman ]; then
    runner_args+=(--network podman)
fi
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
if [ "$status" -ne 0 ]; then
    echo "==> suite failed (exit $status)"
    dump_diagnostics
fi
exit "$status"
