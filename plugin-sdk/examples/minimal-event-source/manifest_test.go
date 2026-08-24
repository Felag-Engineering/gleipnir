package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// TestManifestYAMLIsCanonical mirrors minimal-tool's own test: manifest.yaml
// on disk must be the byte-exact canonical projection of pluginManifest
// (manifest.go), and must itself round-trip through Parse/Marshal
// unchanged. See minimal-tool/README.md's "Manifest" section for the
// regeneration steps this example follows too.
func TestManifestYAMLIsCanonical(t *testing.T) {
	diskBytes, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest.yaml: %v", err)
	}

	parsed, err := manifestv2.Parse(diskBytes)
	if err != nil {
		t.Fatalf("parse manifest.yaml: %v", err)
	}
	roundTripped, err := manifestv2.Marshal(parsed)
	if err != nil {
		t.Fatalf("re-marshal after parse: %v", err)
	}
	if !bytes.Equal(diskBytes, roundTripped) {
		t.Errorf("manifest.yaml is not canonical YAML (round-trip mismatch):\ndisk:\n%s\nre-marshalled:\n%s",
			diskBytes, roundTripped)
	}

	fromGo, err := manifestv2.Marshal(&pluginManifest)
	if err != nil {
		t.Fatalf("marshal pluginManifest: %v", err)
	}
	if !bytes.Equal(diskBytes, fromGo) {
		t.Errorf("manifest.yaml does not match pluginManifest in manifest.go:\ndisk:\n%s\nfrom Go:\n%s",
			diskBytes, fromGo)
	}
}

// TestManifestEventKindsMatchHandlerKinds is the drift check the issue
// asks for: the manifest.yaml an admin reviews at install time must attest
// exactly the kinds the running handler (main.go's eventKinds, wired into
// events.NewHandler) actually discovers. Parsing from disk rather than
// comparing the two Go declarations directly is deliberate — it is the
// artifact an admin actually reads that has to agree, not just the
// in-memory value before marshaling.
func TestManifestEventKindsMatchHandlerKinds(t *testing.T) {
	diskBytes, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest.yaml: %v", err)
	}
	parsed, err := manifestv2.Parse(diskBytes)
	if err != nil {
		t.Fatalf("parse manifest.yaml: %v", err)
	}

	if parsed.Gleipnir.Profiles.EventSource == nil {
		t.Fatal("manifest.yaml does not declare the event_source profile")
	}

	manifestKinds := make(map[string]bool, len(parsed.Gleipnir.EventKinds))
	for _, k := range parsed.Gleipnir.EventKinds {
		manifestKinds[k.Kind] = true
	}

	if len(manifestKinds) != len(eventKinds) {
		t.Fatalf("manifest.yaml attests %d event kinds, the handler declares %d", len(manifestKinds), len(eventKinds))
	}
	for _, k := range eventKinds {
		if !manifestKinds[k.Kind] {
			t.Errorf("handler declares kind %q, which manifest.yaml does not attest", k.Kind)
		}
	}
}
