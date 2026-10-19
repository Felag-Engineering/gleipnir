#!/usr/bin/env bash
set -euo pipefail

# Build plugins/slack-v2 as one signed v2 OCI bundle: dist/slack-v2.tgz, the
# bundle the DooD smoke (internal/plugin/dood/slack_smoke_test.go) installs.
#
# It drives plugins/slack-v2's own `make bundle` (image -> archive -> stamp the
# real config digest into a copy of manifest.yaml -> sign), so the bundle the
# suite installs is built by the same path a release uses; the only difference
# is the key, which here is ephemeral and lives in a mktemp directory removed on
# exit. It is never written into the work tree.
#
# Intermediate files (the image archive, the packaging tools) go to a temp
# directory too, not dist/: run-dood-suite.sh copies all of dist/ into the
# runner container and an image archive has no business there.
#
# Environment:
#   CONTAINER_ENGINE   docker or podman. Default: docker if its daemon answers,
#                      else podman.
#   CONFORMANCE_DIST_DIR  output directory (default: <repo>/dist); the same
#                      variable build-conformance-stub.sh and run-dood-suite.sh
#                      use, so the three agree on where bundles live.

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
dist_dir=${CONFORMANCE_DIST_DIR:-$repo_root/dist}

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

work=$(mktemp -d "${TMPDIR:-/tmp}/slack-v2-bundle.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo "==> engine: $engine"

echo "==> generating ephemeral signing keypair"
(
    cd "$repo_root/plugin-sdk"
    GOWORK=off go build -o "$work/gleipnir-plugin" ./cmd/gleipnir-plugin
)
keys=$work/keys
GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=ephemeral \
    "$work/gleipnir-plugin" keygen --out-dir "$keys" --name signing >/dev/null

echo "==> building the slack-v2 bundle"
GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=ephemeral \
    make -C "$repo_root/plugins/slack-v2" bundle \
    CONTAINER_ENGINE="$engine" \
    SIGNING_KEY="$keys/signing.key" \
    BUILD_DIR="$work/build" \
    DIST_DIR="$work/out"

produced=("$work"/out/*.tar.gz)
if [ ! -f "${produced[0]}" ]; then
    echo "error: make bundle produced no .tar.gz in $work/out" >&2
    exit 1
fi
mkdir -p "$dist_dir"
cp "${produced[0]}" "$dist_dir/slack-v2.tgz"
echo "==> produced $dist_dir/slack-v2.tgz"
