#!/usr/bin/env bash
set -euo pipefail

# Build the ntfy plugin (plugins/ntfy) as one signed v2 OCI bundle for the DooD
# suite: dist/ntfy.tgz.
#
# Same flow as build-conformance-stub.sh: build the image -> save it to an
# archive -> stamp a temp copy of manifest.yaml with the archive's real config
# digest -> `gleipnir-plugin package --image-archive`, signed with an ephemeral
# keypair. The committed manifest is never modified, and the key lives only in a
# mktemp directory removed on exit.
#
# The image archive travels inside the bundle and is deliberately NOT left in
# dist/: scripts/run-dood-suite.sh copies the whole of dist/ into the runner.
#
# Environment:
#   CONTAINER_ENGINE   docker or podman. Default: docker if its daemon answers,
#                      else podman.
#   NTFY_DIST_DIR      output directory (default: <repo>/dist)
#   NTFY_DOCKERFILE    Dockerfile to build (default: plugins/ntfy/Dockerfile)
#   NTFY_BUILD_ARGS    extra words for `<engine> build` (e.g. a proxy)

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
plugin_dir=$repo_root/plugins/ntfy
dist_dir=${NTFY_DIST_DIR:-$repo_root/dist}
dockerfile=${NTFY_DOCKERFILE:-$plugin_dir/Dockerfile}

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

work=$(mktemp -d "${TMPDIR:-/tmp}/ntfy-bundle.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo "==> engine: $engine"

echo "==> building gleipnir-plugin and imagedigest"
(
    cd "$repo_root/plugin-sdk"
    GOWORK=off go build -o "$work/gleipnir-plugin" ./cmd/gleipnir-plugin
    GOWORK=off go build -o "$work/imagedigest" ./internal/imagedigest
)

# The archive's tag must name the manifest's repository (the loader refuses a
# foreign tag), so it is derived from the committed manifest pin.
repository=$(sed -n 's/^  identifier: \(.*\)@sha256:.*/\1/p' "$plugin_dir/manifest.yaml")
if [ -z "$repository" ]; then
    echo "error: could not read package.identifier repository from $plugin_dir/manifest.yaml" >&2
    exit 1
fi
image="$repository:dood"

echo "==> building $image"
# BuildKit attaches provenance/SBOM attestations as extra manifests, turning the
# saved archive into a multi-manifest index the loader's single-image parser
# rejects. Podman has no attestations to turn off.
engine_build_flags=()
engine_save_flags=()
if [ "$engine" = docker ]; then
    engine_build_flags=(--provenance=false --sbom=false)
else
    # Podman's default docker-archive format stores a repeated layer as a
    # symlink, and the archive reader refuses link entries.
    engine_save_flags=(--format oci-archive)
fi
# NTFY_BUILD_ARGS is deliberately word-split.
# shellcheck disable=SC2086
"$engine" build "${engine_build_flags[@]}" ${NTFY_BUILD_ARGS:-} -f "$dockerfile" -t "$image" "$repo_root"

echo "==> saving image archive"
archive=$work/image.tar
"$engine" save "${engine_save_flags[@]}" -o "$archive" "$image"
digest=$("$work/imagedigest" "$archive")
echo "    config digest: $digest"

echo "==> signing"
keys=$work/keys
GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=ephemeral \
    "$work/gleipnir-plugin" keygen --out-dir "$keys" --name signing >/dev/null

manifest=$work/manifest.yaml
sed -e "s|^  identifier: \(.*\)@sha256:.*|  identifier: \1@$digest|" "$plugin_dir/manifest.yaml" >"$manifest"

GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=ephemeral \
    "$work/gleipnir-plugin" package \
    --manifest "$manifest" \
    --image-archive "$archive" \
    --key "$keys/signing.key" \
    --pubkey "$keys/signing.pub" \
    --out-dir "$work/out" >/dev/null

produced=("$work"/out/*.tar.gz)
mkdir -p "$dist_dir"
cp "${produced[0]}" "$dist_dir/ntfy.tgz"
echo "==> produced $dist_dir/ntfy.tgz (image config digest $digest)"
