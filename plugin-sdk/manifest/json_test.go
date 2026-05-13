package manifest_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// jsonFilterFixture is the filter struct used across json_test.go tests.
type jsonFilterFixture struct {
	Pattern manifest.RegexField    `json:"pattern"`
	Channel manifest.EqualsField   `json:"channel,omitempty"`
}

// buildTestManifest builds a Manifest via MustAddEventKind with a typed-filter
// struct to exercise the gen-manifest code path.
func buildTestManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	m := &manifest.Manifest{
		SchemaVersion: "v1",
		Name:          "jsontest",
		Version:       "0.0.1",
		Services:      manifest.Services{Trigger: "v1"},
		Auth:          manifest.AuthDecl{Mode: "instance_credentials", Strategy: "none"},
	}
	m.MustAddEventKind("event_a", "An event", jsonFilterFixture{}, nil)
	return m
}

// genManifestEquivalent simulates gen-manifest's jsonToCanonicalYAML logic:
// JSON → generic any → YAML bytes → manifest.Unmarshal → manifest.Marshal.
func genManifestEquivalent(t *testing.T, jsonData []byte) []byte {
	t.Helper()
	var generic any
	if err := json.Unmarshal(jsonData, &generic); err != nil {
		t.Fatalf("genManifestEquivalent: json.Unmarshal: %v", err)
	}
	rawYAML, err := yaml.Marshal(generic)
	if err != nil {
		t.Fatalf("genManifestEquivalent: yaml.Marshal: %v", err)
	}
	var m manifest.Manifest
	if err := manifest.Unmarshal(rawYAML, &m); err != nil {
		t.Fatalf("genManifestEquivalent: manifest.Unmarshal: %v", err)
	}
	out, err := manifest.Marshal(&m)
	if err != nil {
		t.Fatalf("genManifestEquivalent: manifest.Marshal: %v", err)
	}
	return out
}

// TestManifestMarshalJSON_DeterministicAndLossless verifies that
// json.Marshal(m) → genManifestEquivalent produces byte-equal output to
// direct manifest.Marshal(m). This is the gen-manifest path acceptance test.
func TestManifestMarshalJSON_DeterministicAndLossless(t *testing.T) {
	m := buildTestManifest(t)

	// Direct canonical YAML.
	direct, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("manifest.Marshal: %v", err)
	}

	// Via json.Marshal (the serve.EmitManifest path) → canonical YAML.
	js, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	viaJSON := genManifestEquivalent(t, js)

	if !bytes.Equal(direct, viaJSON) {
		t.Fatalf("gen-manifest path not byte-equal to direct manifest.Marshal:\ndirect:\n%s\nvia JSON:\n%s", direct, viaJSON)
	}
}

// TestManifestMarshalJSON_RoundTripLossless verifies that JSON round-tripping
// a Manifest preserves the binding_schema content: parse JSON → re-marshal →
// equal to direct manifest.Marshal of original.
func TestManifestMarshalJSON_RoundTripLossless(t *testing.T) {
	m := buildTestManifest(t)

	js, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Convert back to Manifest via the gen-manifest path.
	var generic any
	if err := json.Unmarshal(js, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	rawYAML, err := yaml.Marshal(generic)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	var parsed manifest.Manifest
	if err := manifest.Unmarshal(rawYAML, &parsed); err != nil {
		t.Fatalf("manifest.Unmarshal: %v", err)
	}

	reparsed, err := manifest.Marshal(&parsed)
	if err != nil {
		t.Fatalf("second manifest.Marshal: %v", err)
	}
	direct, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("direct manifest.Marshal: %v", err)
	}

	if !bytes.Equal(direct, reparsed) {
		t.Fatalf("round-trip lossless check failed:\ndirect:\n%s\nreparsed:\n%s", direct, reparsed)
	}
}
