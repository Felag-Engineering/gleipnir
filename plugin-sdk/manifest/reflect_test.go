package manifest_test

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// filterFixture is a non-trivial struct used across reflect tests.
// It exercises named filter primitives, a nested struct, and a slice.
type filterFixture struct {
	Pattern  manifest.RegexField    `json:"pattern"  jsonschema:"title=Pattern,description=regex filter"`
	Contains manifest.ContainsField `json:"contains" jsonschema:"title=Contains"`
	Tags     []string               `json:"tags,omitempty"`
	Nested   nestedFixture          `json:"nested,omitempty"`
}

type nestedFixture struct {
	Channel manifest.EqualsField `json:"channel"`
	Glob    manifest.GlobField   `json:"glob,omitempty"`
}

// manifestWithSchema builds a minimal Manifest whose first EventKindDecl has
// the given BindingSchema, so we can normalise via manifest.Marshal.
func manifestWithSchema(t *testing.T, schema *yaml.Node) *manifest.Manifest {
	t.Helper()
	m := &manifest.Manifest{
		SchemaVersion: "v1",
		Name:          "reflecttest",
		Version:       "0.0.1",
		Services:      manifest.Services{Trigger: "v1"},
		Auth:          manifest.AuthDecl{Mode: "instance_credentials", Strategy: "none"},
		EventKinds: []manifest.EventKindDecl{
			{Kind: "test_event", BindingSchema: schema},
		},
	}
	return m
}

// normalise wraps schema in a minimal Manifest and returns the canonical YAML
// bytes from manifest.Marshal. This strips yaml.Node style-bits, making
// byte-equality comparisons meaningful.
func normalise(t *testing.T, schema *yaml.Node) []byte {
	t.Helper()
	b, err := manifest.Marshal(manifestWithSchema(t, schema))
	if err != nil {
		t.Fatalf("manifest.Marshal: %v", err)
	}
	return b
}

// TestReflectSchema_Determinism_EmitTwice calls ReflectSchema twice on the
// same type and verifies that both outputs produce byte-equal canonical YAML.
func TestReflectSchema_Determinism_EmitTwice(t *testing.T) {
	n1, err := manifest.ReflectSchema(filterFixture{})
	if err != nil {
		t.Fatalf("first ReflectSchema: %v", err)
	}
	n2, err := manifest.ReflectSchema(filterFixture{})
	if err != nil {
		t.Fatalf("second ReflectSchema: %v", err)
	}

	b1 := normalise(t, n1)
	b2 := normalise(t, n2)

	if !bytes.Equal(b1, b2) {
		t.Fatalf("ReflectSchema not deterministic:\nfirst:\n%s\nsecond:\n%s", b1, b2)
	}
}

// TestReflectSchema_RoundTrip_Stable verifies Marshal → Unmarshal → Marshal
// byte-equality for a manifest containing a reflected schema.
func TestReflectSchema_RoundTrip_Stable(t *testing.T) {
	n, err := manifest.ReflectSchema(filterFixture{})
	if err != nil {
		t.Fatalf("ReflectSchema: %v", err)
	}
	m := manifestWithSchema(t, n)

	first, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("first marshal: %v", err)
	}

	var parsed manifest.Manifest
	if err := manifest.Unmarshal(first, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	second, err := manifest.Marshal(&parsed)
	if err != nil {
		t.Fatalf("second marshal: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Fatalf("round-trip not stable:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestReflectSchema_TypedFilters walks the reflected *yaml.Node mapping and
// asserts that the typed filter primitives produce the expected JSON Schema
// fragments.
func TestReflectSchema_TypedFilters(t *testing.T) {
	n, err := manifest.ReflectSchema(filterFixture{})
	if err != nil {
		t.Fatalf("ReflectSchema: %v", err)
	}

	// The reflected node is a mapping; find "properties" → look up "pattern"
	// and "contains".
	props := findMappingValue(n, "properties")
	if props == nil {
		t.Fatal("schema has no 'properties' key")
	}

	patternProp := findMappingValue(props, "pattern")
	if patternProp == nil {
		t.Fatal("properties has no 'pattern' key")
	}
	assertNodeValue(t, patternProp, "type", "string")
	assertNodeValue(t, patternProp, "format", "regex")

	containsProp := findMappingValue(props, "contains")
	if containsProp == nil {
		t.Fatal("properties has no 'contains' key")
	}
	assertNodeValue(t, containsProp, "type", "string")
}

// TestReflectSchema_NoDollarSchema is a regression guard ensuring the
// "$schema" URL is not present in the reflected output.
func TestReflectSchema_NoDollarSchema(t *testing.T) {
	n, err := manifest.ReflectSchema(filterFixture{})
	if err != nil {
		t.Fatalf("ReflectSchema: %v", err)
	}

	if findMappingValue(n, "$schema") != nil {
		t.Error("reflected schema contains '$schema' key — it should have been stripped")
	}
}

// --- helpers ---

// findMappingValue searches a yaml.MappingNode for the given key and returns
// its value node, or nil if not found.
func findMappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// assertNodeValue fails the test if key is not found in the mapping n or its
// scalar value does not equal want.
func assertNodeValue(t *testing.T, n *yaml.Node, key, want string) {
	t.Helper()
	v := findMappingValue(n, key)
	if v == nil {
		t.Errorf("mapping has no %q key (want value %q)", key, want)
		return
	}
	if v.Value != want {
		t.Errorf("%q = %q, want %q", key, v.Value, want)
	}
}
