#!/usr/bin/env bash
set -euo pipefail

# Build the conformance stub (plugin-sdk/examples/conformance-stub) as one
# signed v2 OCI bundle per mode manifest: dist/conformance-stub-<mode>.tgz,
# plus one per suite scenario: dist/conformance-stub-<scenario>.tgz (below).
#
# Flow: build the image -> save it to one archive -> for each manifest in
# manifests/, stamp a temp copy with the archive's real config digest and a
# bundle-safe name -> `gleipnir-plugin package --image-archive`, signed with an
# ephemeral keypair.
#
# "Never pull at DooD time": this script is the only place that talks to a
# registry (the Dockerfile's base images). Each bundle carries its own image
# archive, so the host-side suite installs it with `docker load` and never
# pulls.
#
# Environment:
#   CONTAINER_ENGINE      docker or podman. Default: docker if its daemon
#                         answers, else podman.
#   CONFORMANCE_DIST_DIR  output directory (default: <repo>/dist)
#   CONFORMANCE_DOCKERFILE  Dockerfile to build (default: the stub's own)
#   CONFORMANCE_BUILD_ARGS  extra words for `<engine> build` (e.g. a proxy)
#
# The signing key lives only in a mktemp directory removed on exit. It is never
# written into the work tree, so nothing here can be committed by accident.

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
stub_dir=$repo_root/plugin-sdk/examples/conformance-stub
dist_dir=${CONFORMANCE_DIST_DIR:-$repo_root/dist}
dockerfile=${CONFORMANCE_DOCKERFILE:-$stub_dir/Dockerfile}

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

work=$(mktemp -d "${TMPDIR:-/tmp}/conformance-stub.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo "==> engine: $engine"

echo "==> building gleipnir-plugin and imagedigest"
(
    cd "$repo_root/plugin-sdk"
    GOWORK=off go build -o "$work/gleipnir-plugin" ./cmd/gleipnir-plugin
    GOWORK=off go build -o "$work/imagedigest" ./internal/imagedigest
)

# The archive's tags must name the manifests' repository (the loader refuses a
# foreign tag), so the tag is derived from the committed manifest pin.
first_manifest=$(find "$stub_dir/manifests" -name '*.yaml' | sort | head -n 1)
repository=$(sed -n 's/^  identifier: \(.*\)@sha256:.*/\1/p' "$first_manifest")
if [ -z "$repository" ]; then
    echo "error: could not read package.identifier repository from $first_manifest" >&2
    exit 1
fi
image="$repository:conformance"

echo "==> building $image"
# BuildKit attaches provenance/SBOM attestations as extra manifests, turning the
# saved archive into a multi-manifest index the loader's single-image parser
# rejects. Podman has no attestations to turn off.
engine_build_flags=()
if [ "$engine" = docker ]; then
    engine_build_flags=(--provenance=false --sbom=false)
fi
# CONFORMANCE_BUILD_ARGS is deliberately word-split.
# shellcheck disable=SC2086
"$engine" build "${engine_build_flags[@]}" ${CONFORMANCE_BUILD_ARGS:-} -f "$dockerfile" -t "$image" "$repo_root"

echo "==> saving image archive"
archive=$work/image.tar
# Podman's default docker-archive format stores a repeated layer as a symlink,
# and the archive reader refuses link entries; an OCI archive needs none.
engine_save_flags=()
if [ "$engine" = podman ]; then
    engine_save_flags=(--format oci-archive)
fi
"$engine" save "${engine_save_flags[@]}" -o "$archive" "$image"
digest=$("$work/imagedigest" "$archive")
echo "    config digest: $digest"

echo "==> generating ephemeral signing keypair"
keys=$work/keys
GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=ephemeral \
    "$work/gleipnir-plugin" keygen --out-dir "$keys" --name signing >/dev/null

mkdir -p "$dist_dir"
bundles=()

# package_bundle NAME MANIFEST ARCHIVE DIGEST: sign one bundle into
# $dist_dir/NAME.tgz.
package_bundle() {
    local name=$1 src=$2 archive=$3 digest=$4
    local manifest=$work/manifest-$name.yaml

    # The committed manifests carry a placeholder digest and the registry-style
    # name io.example/conformance-stub, whose '/' package rejects as a bundle
    # path component. Both are rewritten in a copy; the committed files are
    # untouched.
    sed -e "s|^name: .*|name: $name|" \
        -e "s|^  identifier: \(.*\)@sha256:.*|  identifier: \1@$digest|" \
        "$src" >"$manifest"

    local outdir=$work/out-$name
    GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=ephemeral \
        "$work/gleipnir-plugin" package \
        --manifest "$manifest" \
        --image-archive "$archive" \
        --key "$keys/signing.key" \
        --pubkey "$keys/signing.pub" \
        --out-dir "$outdir" >/dev/null

    local produced=("$outdir"/*.tar.gz)
    cp "${produced[0]}" "$dist_dir/$name.tgz"
    bundles+=("$dist_dir/$name.tgz")
}

for src in "$stub_dir"/manifests/*.yaml; do
    mode=$(basename "$src" .yaml)
    package_bundle "conformance-stub-$mode" "$src" "$archive" "$digest"
done

# Scenario bundles. The stub is configured by environment only, and a plugin
# instance has no way to carry environment, so a suite that needs anything but
# the defaults gets an image with its settings baked in: a one-layer image FROM
# the stub plus ENV, saved and digested on its own, packaged with the manifest
# of the mode whose profiles it serves. Each is its own plugin to the host.
#
#   <scenario>|<mode manifest>|<ENV assignments>
scenarios=(
    # DooD suite part 2 (#998): redelivery across a container restart.
    "dood-events|event-only|STUB_PROFILES=event_source STUB_EMIT_COUNT=5 STUB_EMIT_START=first_listener STUB_EMIT_INTERVAL_MS=2000"
    # DooD suite part 2 (#998): a policy-gated approval answered by the stub.
    "dood-request|all|STUB_AUTO_RESOLVE=first_option STUB_EMIT_COUNT=0"
)
for spec in "${scenarios[@]}"; do
    IFS='|' read -r scenario mode env_words <<<"$spec"
    scenario_image="$repository:$scenario"
    scenario_dir=$work/scenario-$scenario
    mkdir -p "$scenario_dir"
    printf 'FROM %s\nENV %s\n' "$image" "$env_words" >"$scenario_dir/Dockerfile"

    echo "==> building scenario $scenario ($env_words)"
    "$engine" build "${engine_build_flags[@]}" -t "$scenario_image" "$scenario_dir"
    scenario_archive=$scenario_dir/image.tar
    "$engine" save "${engine_save_flags[@]}" -o "$scenario_archive" "$scenario_image"
    scenario_digest=$("$work/imagedigest" "$scenario_archive")
    echo "    config digest: $scenario_digest"

    package_bundle "conformance-stub-$scenario" "$stub_dir/manifests/$mode.yaml" "$scenario_archive" "$scenario_digest"
done

echo "==> produced bundles (stub image config digest $digest)"
for b in "${bundles[@]}"; do
    echo "    $b"
done
