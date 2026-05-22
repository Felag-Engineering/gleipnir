package manifest_test

import (
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// secretConfigFixture mixes a SecretString field with a plain string field.
// Used to verify that the annotation appears only on the secret field.
type secretConfigFixture struct {
	Token  manifest.SecretString `json:"token"  jsonschema:"description=API token"`
	Region string                `json:"region" jsonschema:"description=Region"`
}

// secretOnlyFixture holds a single SecretString field for isolated tests.
type secretOnlyFixture struct {
	Token manifest.SecretString `json:"token"`
}

// TestSecretString_JSONSchema_DirectCall exercises the JSONSchema() method
// directly without going through the reflector.
func TestSecretString_JSONSchema_DirectCall(t *testing.T) {
	var s manifest.SecretString
	schema := s.JSONSchema()
	if schema == nil {
		t.Fatal("JSONSchema() returned nil")
	}
	if schema.Type != "string" {
		t.Errorf("Type = %q, want %q", schema.Type, "string")
	}
	val, ok := schema.Extras[manifest.SecretAnnotationKey]
	if !ok {
		t.Fatalf("Extras missing key %q", manifest.SecretAnnotationKey)
	}
	boolVal, isBool := val.(bool)
	if !isBool || !boolVal {
		t.Errorf("Extras[%q] = %v (%T), want true (bool)", manifest.SecretAnnotationKey, val, val)
	}
}

// TestSecretString_ReflectSchema_EmitsAnnotation verifies that ReflectSchema
// produces a *yaml.Node whose "token" property contains x-gleipnir-secret: true.
func TestSecretString_ReflectSchema_EmitsAnnotation(t *testing.T) {
	node, err := manifest.ReflectSchema(secretOnlyFixture{})
	if err != nil {
		t.Fatalf("ReflectSchema: %v", err)
	}
	if node == nil {
		t.Fatal("ReflectSchema returned nil node")
	}

	props := findMappingValue(node, "properties")
	if props == nil {
		t.Fatal("schema has no 'properties' key")
	}
	tokenProp := findMappingValue(props, "token")
	if tokenProp == nil {
		t.Fatal("properties has no 'token' key")
	}
	assertNodeValue(t, tokenProp, "type", "string")
	assertNodeValue(t, tokenProp, manifest.SecretAnnotationKey, "true")
}

// TestSecretString_ReflectSchema_PreservesDescription verifies that both
// x-gleipnir-secret and the jsonschema description tag survive reflection.
func TestSecretString_ReflectSchema_PreservesDescription(t *testing.T) {
	node, err := manifest.ReflectSchema(secretConfigFixture{})
	if err != nil {
		t.Fatalf("ReflectSchema: %v", err)
	}

	props := findMappingValue(node, "properties")
	if props == nil {
		t.Fatal("schema has no 'properties' key")
	}

	// Secret field: must have both the annotation and the description.
	tokenProp := findMappingValue(props, "token")
	if tokenProp == nil {
		t.Fatal("properties has no 'token' key")
	}
	assertNodeValue(t, tokenProp, manifest.SecretAnnotationKey, "true")
	assertNodeValue(t, tokenProp, "description", "API token")

	// Non-secret field: must NOT carry the annotation.
	regionProp := findMappingValue(props, "region")
	if regionProp == nil {
		t.Fatal("properties has no 'region' key")
	}
	if secretNode := findMappingValue(regionProp, manifest.SecretAnnotationKey); secretNode != nil {
		t.Errorf("non-secret 'region' field carries %s annotation (value %q), want absent",
			manifest.SecretAnnotationKey, secretNode.Value)
	}
}

// TestSecretAnnotationKey_IsCorrectLiteral is a compile-time guard; if the
// constant changes, tests that compare against the string literal will fail.
func TestSecretAnnotationKey_IsCorrectLiteral(t *testing.T) {
	if manifest.SecretAnnotationKey != "x-gleipnir-secret" {
		t.Errorf("SecretAnnotationKey = %q, want %q", manifest.SecretAnnotationKey, "x-gleipnir-secret")
	}
}
