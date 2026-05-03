package cmd

import (
	"fmt"
	"os"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// loadCanonicalManifest reads manifest.yaml from manifestPath and returns
// canonical YAML bytes (sorted keys, 2-space indent). This is the on-disk
// manifest leg; the binary --emit-manifest JSON leg uses jsonToCanonicalYAML.
func loadCanonicalManifest(manifestPath string) ([]byte, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	return canonicaliseYAML(data)
}

// parseManifestFromBytes parses canonical or non-canonical YAML bytes into a
// manifest.Manifest struct.
func parseManifestFromBytes(data []byte) (*manifest.Manifest, error) {
	var m manifest.Manifest
	if err := manifest.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}
