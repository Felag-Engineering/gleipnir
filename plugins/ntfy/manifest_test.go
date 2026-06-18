package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// TestManifestAuthStrategyIsKnownConstant verifies that the auth strategy
// declared in the Go manifest matches manifest.AuthStrategyStaticAPIKey.
// This catches literal-vs-constant drift that would break credential setup:
// requireOneOfStrategies, BuildSeedCredentials, and SetStaticAPIKey all
// compare against the SDK constant, so the manifest must match it exactly.
func TestManifestAuthStrategyIsKnownConstant(t *testing.T) {
	if pluginManifest.Auth.Strategy != manifest.AuthStrategyStaticAPIKey {
		t.Errorf("Auth.Strategy = %q, want %q (manifest.AuthStrategyStaticAPIKey)",
			pluginManifest.Auth.Strategy, manifest.AuthStrategyStaticAPIKey)
	}
}

// TestManifestYAMLIsCanonical verifies that manifest.yaml on disk is the
// byte-exact canonical projection of the pluginManifest Go declaration.
//
// Two sub-assertions:
//  1. Round-trip: Unmarshal(disk) → Marshal produces the same bytes as disk.
//  2. Go-source-of-truth: Marshal(pluginManifest) produces the same bytes as disk.
//
// If either assertion fails, the committed manifest.yaml has drifted from
// manifest.go — see the "Manifest" section of README.md for regeneration steps.
//
// Note: os.ReadFile("manifest.yaml") uses a relative path because `go test`
// runs with the package directory as the working directory, so the path
// resolves correctly without any special setup.
func TestManifestYAMLIsCanonical(t *testing.T) {
	diskBytes, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest.yaml: %v", err)
	}

	// Sub-assertion 1: round-trip stability.
	var parsed manifest.Manifest
	if err := manifest.Unmarshal(diskBytes, &parsed); err != nil {
		t.Fatalf("unmarshal manifest.yaml: %v", err)
	}
	roundTripped, err := manifest.Marshal(&parsed)
	if err != nil {
		t.Fatalf("re-marshal after unmarshal: %v", err)
	}
	if !bytes.Equal(diskBytes, roundTripped) {
		t.Errorf("manifest.yaml is not canonical YAML (round-trip mismatch):\ndisk:\n%s\nre-marshalled:\n%s",
			diskBytes, roundTripped)
	}

	// Sub-assertion 2: Go declaration matches disk.
	fromGo, err := manifest.Marshal(&pluginManifest)
	if err != nil {
		t.Fatalf("marshal pluginManifest: %v", err)
	}
	if !bytes.Equal(diskBytes, fromGo) {
		t.Errorf("manifest.yaml does not match pluginManifest in manifest.go:\ndisk:\n%s\nfrom Go:\n%s",
			diskBytes, fromGo)
	}
}
