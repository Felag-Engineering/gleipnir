.PHONY: build build-arm64 test manifest validate clean

# Plugins are compiled Go binaries the host spawns as subprocesses, so the
# binary's GOARCH must match the HOST arch, not the build machine. Like the
# main server the plugin is CGO-free, so cross-compiling is a matter of setting
# GOARCH. Override GOOS/GOARCH to target a different host:
#   make build GOARCH=arm64        # build for an arm64 host
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# build compiles the plugin binary for the current (or overridden) GOOS/GOARCH.
build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o slack .

# build-arm64 is a convenience target for the common cross-build case
# (e.g. packaging a bundle for a Raspberry Pi / Graviton host from an amd64 box).
build-arm64:
	$(MAKE) build GOARCH=arm64

# test runs the unit test suite.
test:
	go test ./...

# manifest regenerates manifest.yaml from the compiled binary.
# Requires gleipnir-plugin on PATH or at ../../gleipnir-plugin.
manifest: build
	../../gleipnir-plugin gen-manifest --binary ./slack --out manifest.yaml

# validate checks that manifest.yaml matches what the binary emits.
validate: build
	../../gleipnir-plugin validate --binary ./slack --manifest manifest.yaml

# clean removes the compiled binary.
clean:
	rm -f slack
