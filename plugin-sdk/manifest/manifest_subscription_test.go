package manifest_test

import (
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"gopkg.in/yaml.v3"
)

// TestSubscriptionSchema_RoundTrip verifies that a Manifest with a
// SubscriptionSchema survives Marshal → Unmarshal with the schema intact.
// This guards against the UnmarshalYAML alias/Kind==0 edge case that affects
// all *yaml.Node fields in Manifest.
func TestSubscriptionSchema_RoundTrip(t *testing.T) {
	m := baseManifest()
	m.Services = manifest.Services{Trigger: "v1"}

	// Build a simple subscription schema node directly.
	schemaYAML := `
type: object
properties:
  channels:
    type: array
    items:
      type: string
required:
  - channels
`
	var schemaNode yaml.Node
	if err := yaml.Unmarshal([]byte(schemaYAML), &schemaNode); err != nil {
		t.Fatalf("parse schema YAML: %v", err)
	}
	// yaml.Unmarshal wraps in a DocumentNode; unwrap to the MappingNode.
	if schemaNode.Kind == yaml.DocumentNode && len(schemaNode.Content) > 0 {
		inner := schemaNode.Content[0]
		m.SubscriptionSchema = inner
	} else {
		m.SubscriptionSchema = &schemaNode
	}

	// Marshal to YAML bytes and unmarshal back.
	raw, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m2 manifest.Manifest
	if err := manifest.Unmarshal(raw, &m2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m2.SubscriptionSchema == nil {
		t.Fatal("SubscriptionSchema is nil after round-trip, want non-nil")
	}

	// Decode the round-tripped schema into a map and check shape.
	var decoded map[string]any
	if err := m2.SubscriptionSchema.Decode(&decoded); err != nil {
		t.Fatalf("Decode SubscriptionSchema: %v", err)
	}
	if decoded["type"] != "object" {
		t.Errorf("type = %v, want %q", decoded["type"], "object")
	}
	props, ok := decoded["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties is %T, want map[string]any", decoded["properties"])
	}
	if _, hasCh := props["channels"]; !hasCh {
		t.Error("properties.channels missing after round-trip")
	}
}

// TestSubscriptionSchema_NilWhenAbsent verifies that a manifest with no
// subscription_schema field produces a nil SubscriptionSchema after Unmarshal
// (the zero-value yaml.Node guard in UnmarshalYAML must fire correctly).
func TestSubscriptionSchema_NilWhenAbsent(t *testing.T) {
	raw, err := manifest.Marshal(baseManifest())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m manifest.Manifest
	if err := manifest.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.SubscriptionSchema != nil {
		t.Error("SubscriptionSchema should be nil when absent from YAML")
	}
}
