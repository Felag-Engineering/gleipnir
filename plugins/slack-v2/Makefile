# Image, archive and signed-bundle targets for the slack-v2 plugin.
#
#   make image
#   make archive
#   make bundle SIGNING_KEY=/path/outside/the/repo/signing.key
#
# `bundle` signs with a key that lives OUTSIDE this repository (#947). The key
# passphrase is read from GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE, never from a
# file here. Nothing in this Makefile creates or stores a key; for a throwaway
# bundle, mint an ephemeral one with `gleipnir-plugin keygen` into a temp dir
# (scripts/build-conformance-stub.sh shows how).

CONTAINER_ENGINE ?= docker
REPO_ROOT := $(abspath $(CURDIR)/../..)
DIST_DIR ?= $(REPO_ROOT)/dist
BUILD_DIR ?= $(DIST_DIR)/slack-v2-build

IMAGE_REPOSITORY := ghcr.io/felag-engineering/gleipnir-plugin-slack-v2
IMAGE := $(IMAGE_REPOSITORY):build
ARCHIVE := $(BUILD_DIR)/image.tar

# BuildKit attestations turn the saved archive into a multi-manifest index the
# loader's single-image parser rejects.
ifeq ($(CONTAINER_ENGINE),docker)
BUILD_FLAGS := --provenance=false --sbom=false
else
SAVE_FLAGS := --format oci-archive
endif

.PHONY: image archive bundle test

test:
	go test -race ./...

image:
	$(CONTAINER_ENGINE) build $(BUILD_FLAGS) -f Dockerfile -t $(IMAGE) $(REPO_ROOT)

archive: image
	mkdir -p $(BUILD_DIR)
	$(CONTAINER_ENGINE) save $(SAVE_FLAGS) -o $(ARCHIVE) $(IMAGE)

# Stamps the archive's real config digest into a copy of manifest.yaml (the
# committed pin is a placeholder), then packages and signs it.
bundle: archive
	@test -n "$(SIGNING_KEY)" || { echo "error: set SIGNING_KEY=<path to .key outside the repo>" >&2; exit 1; }
	cd $(REPO_ROOT)/plugin-sdk && GOWORK=off go build -o $(BUILD_DIR)/gleipnir-plugin ./cmd/gleipnir-plugin
	cd $(REPO_ROOT)/plugin-sdk && GOWORK=off go build -o $(BUILD_DIR)/imagedigest ./internal/imagedigest
	digest=$$($(BUILD_DIR)/imagedigest $(ARCHIVE)) && \
		sed "s|^\(  identifier: .*\)@sha256:.*|\1@$$digest|" manifest.yaml > $(BUILD_DIR)/manifest.yaml
	$(BUILD_DIR)/gleipnir-plugin package \
		--manifest $(BUILD_DIR)/manifest.yaml \
		--image-archive $(ARCHIVE) \
		--key $(SIGNING_KEY) \
		--out-dir $(DIST_DIR)
