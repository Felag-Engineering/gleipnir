.PHONY: build test manifest validate clean

# build compiles the plugin binary.
build:
	go build -o slack .

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
