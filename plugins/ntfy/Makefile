.PHONY: image archive manifest-digest bundle test clean

# Flow: image -> archive (docker save) -> manifest-digest (stamp the archive's
# image config digest into a copy of manifest.yaml) -> bundle (sign + package).
# The committed manifest.yaml keeps a placeholder digest; only the copy under
# $(OUT) is stamped, so a build never dirties the work tree.

CONTAINER_ENGINE ?= docker
REPO_ROOT := $(abspath ../..)
REPOSITORY := ghcr.io/felag-engineering/gleipnir-ntfy
IMAGE ?= $(REPOSITORY):dev
OUT ?= dist

# The signing key is held outside the repository. Its passphrase, if any, is
# read from GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE; the .pub file is expected
# next to the key.
SIGNING_KEY ?=

TOOLS := $(OUT)/tools
ARCHIVE := $(OUT)/image.tar
STAMPED_MANIFEST := $(OUT)/manifest.yaml

# BuildKit attaches provenance/SBOM attestations as extra manifests, turning the
# saved archive into a multi-manifest index the loader's single-image parser
# rejects.
ifeq ($(CONTAINER_ENGINE),docker)
BUILD_FLAGS := --provenance=false --sbom=false
else
SAVE_FLAGS := --format oci-archive
endif

image:
	$(CONTAINER_ENGINE) build $(BUILD_FLAGS) $(BUILD_ARGS) -f Dockerfile -t $(IMAGE) $(REPO_ROOT)

archive: image
	mkdir -p $(OUT)
	$(CONTAINER_ENGINE) save $(SAVE_FLAGS) -o $(ARCHIVE) $(IMAGE)

# One sentinel for both binaries, so a parallel make builds them once.
$(TOOLS)/.built:
	mkdir -p $(TOOLS)
	cd $(REPO_ROOT)/plugin-sdk && GOWORK=off go build -o $(abspath $(TOOLS))/gleipnir-plugin ./cmd/gleipnir-plugin
	cd $(REPO_ROOT)/plugin-sdk && GOWORK=off go build -o $(abspath $(TOOLS))/imagedigest ./internal/imagedigest
	touch $(TOOLS)/.built

# manifest-digest writes $(STAMPED_MANIFEST): manifest.yaml with the archive's
# image config digest as the package pin.
manifest-digest: archive $(TOOLS)/.built
	digest=$$($(TOOLS)/imagedigest $(ARCHIVE)) && \
	sed -e "s|^  identifier: \(.*\)@sha256:.*|  identifier: \1@$$digest|" manifest.yaml > $(STAMPED_MANIFEST)

bundle: manifest-digest
	@test -n "$(SIGNING_KEY)" || { echo "error: set SIGNING_KEY=<path to .key> (kept outside the repository)" >&2; exit 1; }
	$(TOOLS)/gleipnir-plugin package \
		--manifest $(STAMPED_MANIFEST) \
		--image-archive $(ARCHIVE) \
		--key $(SIGNING_KEY) \
		--out-dir $(OUT)/bundle

test:
	go vet ./... && go test -race ./...

clean:
	rm -rf $(OUT)
