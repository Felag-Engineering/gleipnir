package configvalidate

import (
	"encoding/json"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseSchemaNode is a test helper that parses a YAML string into a *yaml.Node
// (document-unwrapped), mirroring what the manifest loader produces.
func parseSchemaNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parseSchemaNode: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return &doc
}

// unmarshalJSON is a test helper that decodes a JSON string into v.
func unmarshalJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("unmarshalJSON: %v", err)
	}
}

// ── SecretPropertyNames tests ─────────────────────────────────────────────────

func TestSecretPropertyNames_NilSchema(t *testing.T) {
	names, err := SecretPropertyNames(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if names != nil {
		t.Errorf("nil schema: want nil map, got %v", names)
	}
}

func TestSecretPropertyNames_NoPropertiesKey(t *testing.T) {
	node := parseSchemaNode(t, "type: object\n")
	names, err := SecretPropertyNames(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if names != nil {
		t.Errorf("schema without properties: want nil map, got %v", names)
	}
}

func TestSecretPropertyNames_NoSecretProperties(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  api_key:
    type: string
  region:
    type: string
`)
	names, err := SecretPropertyNames(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if names != nil {
		t.Errorf("schema without secrets: want nil map, got %v", names)
	}
}

func TestSecretPropertyNames_SingleSecret(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  app_token:
    type: string
    x-gleipnir-secret: true
  region:
    type: string
`)
	names, err := SecretPropertyNames(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]bool{"app_token": true}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("SecretPropertyNames = %v, want %v", names, want)
	}
}

func TestSecretPropertyNames_MixedSecretAndNonSecret(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  token:
    type: string
    x-gleipnir-secret: true
  name:
    type: string
  password:
    type: string
    x-gleipnir-secret: true
`)
	names, err := SecretPropertyNames(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]bool{"token": true, "password": true}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("SecretPropertyNames = %v, want %v", names, want)
	}
}

func TestSecretPropertyNames_FalseAnnotationNotIncluded(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  api_key:
    type: string
    x-gleipnir-secret: false
`)
	names, err := SecretPropertyNames(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if names != nil {
		t.Errorf("x-gleipnir-secret: false must not be included, got %v", names)
	}
}

func TestSecretPropertyNames_StringTrueNotIncluded(t *testing.T) {
	// The annotation value must be a boolean true. A quoted "true" in YAML is a
	// string, which must not be accepted (per ADR-049 decision §1).
	node := parseSchemaNode(t, `
type: object
properties:
  api_key:
    type: string
    x-gleipnir-secret: "true"
`)
	names, err := SecretPropertyNames(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, found := names["api_key"]; found {
		t.Errorf("string value 'true' must not be treated as the annotation; got %v", names)
	}
}

// ── RedactSecrets tests ───────────────────────────────────────────────────────

func TestRedactSecrets_EmptyConfig(t *testing.T) {
	result, err := RedactSecrets("", map[string]bool{"token": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "{}" {
		t.Errorf("empty config: got %q, want %q", result, "{}")
	}
}

func TestRedactSecrets_EmptyObjectConfig(t *testing.T) {
	result, err := RedactSecrets("{}", map[string]bool{"token": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "{}" {
		t.Errorf("{} config: got %q, want %q", result, "{}")
	}
}

func TestRedactSecrets_SecretPresent(t *testing.T) {
	result, err := RedactSecrets(`{"token":"xapp-1-real","region":"us-east-1"}`, map[string]bool{"token": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	unmarshalJSON(t, result, &out)
	if out["token"] != RedactionSentinel {
		t.Errorf("token = %v, want %q", out["token"], RedactionSentinel)
	}
	if out["region"] != "us-east-1" {
		t.Errorf("region = %v, want %q", out["region"], "us-east-1")
	}
}

func TestRedactSecrets_SecretAbsent(t *testing.T) {
	// Secret key is not present in config — must not be added.
	result, err := RedactSecrets(`{"region":"us-east-1"}`, map[string]bool{"token": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	unmarshalJSON(t, result, &out)
	if _, ok := out["token"]; ok {
		t.Errorf("absent secret key must not be added to output, got %v", out)
	}
	if out["region"] != "us-east-1" {
		t.Errorf("region = %v, want %q", out["region"], "us-east-1")
	}
}

func TestRedactSecrets_NoSecretNames(t *testing.T) {
	input := `{"token":"xapp-1-real","region":"us-east-1"}`
	result, err := RedactSecrets(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != input {
		t.Errorf("nil secretNames: got %q, want input unchanged %q", result, input)
	}
}

func TestRedactSecrets_NonSecretKeysPreserved(t *testing.T) {
	result, err := RedactSecrets(`{"token":"xapp-1-real","name":"prod","count":3}`, map[string]bool{"token": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	unmarshalJSON(t, result, &out)
	if out["name"] != "prod" {
		t.Errorf("name = %v, want %q", out["name"], "prod")
	}
	// json.Unmarshal decodes JSON numbers as float64.
	if out["count"] != float64(3) {
		t.Errorf("count = %v, want 3", out["count"])
	}
}

// ── ContainsRedactionSentinel tests ──────────────────────────────────────────

func TestContainsRedactionSentinel_NoSentinel(t *testing.T) {
	cfg := map[string]any{"token": "xapp-1-real", "region": "us-east-1"}
	result := ContainsRedactionSentinel(cfg, map[string]bool{"token": true})
	if len(result) != 0 {
		t.Errorf("no sentinel: got %v, want empty", result)
	}
}

func TestContainsRedactionSentinel_OneSecret(t *testing.T) {
	cfg := map[string]any{"token": RedactionSentinel, "region": "us-east-1"}
	result := ContainsRedactionSentinel(cfg, map[string]bool{"token": true})
	want := []string{"token"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("ContainsRedactionSentinel = %v, want %v", result, want)
	}
}

func TestContainsRedactionSentinel_SentinelOnNonSecretIgnored(t *testing.T) {
	// "region" holds "***" but is not marked as a secret — must be ignored.
	cfg := map[string]any{"token": "xapp-1-real", "region": RedactionSentinel}
	result := ContainsRedactionSentinel(cfg, map[string]bool{"token": true})
	if len(result) != 0 {
		t.Errorf("sentinel on non-secret field must be ignored, got %v", result)
	}
}

func TestContainsRedactionSentinel_MultipleOffenders_Sorted(t *testing.T) {
	cfg := map[string]any{
		"token":    RedactionSentinel,
		"password": RedactionSentinel,
		"region":   "us-east-1",
	}
	result := ContainsRedactionSentinel(cfg, map[string]bool{"token": true, "password": true})
	want := []string{"password", "token"} // sorted alphabetically
	if !reflect.DeepEqual(result, want) {
		t.Errorf("ContainsRedactionSentinel = %v, want %v (sorted)", result, want)
	}
}

func TestContainsRedactionSentinel_EmptyConfig(t *testing.T) {
	result := ContainsRedactionSentinel(nil, map[string]bool{"token": true})
	if len(result) != 0 {
		t.Errorf("nil configMap: got %v, want empty", result)
	}
}

func TestContainsRedactionSentinel_EmptySecretNames(t *testing.T) {
	cfg := map[string]any{"token": RedactionSentinel}
	result := ContainsRedactionSentinel(cfg, nil)
	if len(result) != 0 {
		t.Errorf("nil secretNames: got %v, want empty", result)
	}
}
