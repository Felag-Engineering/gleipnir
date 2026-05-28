package schemautil_test

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/schemautil"
	"gopkg.in/yaml.v3"
)

func parseNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return &doc
}

func TestToJSON_NilNode(t *testing.T) {
	got, err := schemautil.ToJSON(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != `{}` {
		t.Errorf("got %q, want {}", got)
	}
}

func TestToJSON_Simple(t *testing.T) {
	node := parseNode(t, `type: string`)
	got, err := schemautil.ToJSON(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"type":"string"}`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToJSON_PreservesDescriptionAndDefault(t *testing.T) {
	node := parseNode(t, "type: string\ndescription: A field\ndefault: hello")
	got, err := schemautil.ToJSON(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// description and default must be present in the full round-trip.
	for _, key := range []string{`"description"`, `"default"`} {
		if !contains(string(got), key) {
			t.Errorf("expected %s in ToJSON output %q", key, got)
		}
	}
}

func TestToJSONStripped_NilNode(t *testing.T) {
	if got := schemautil.ToJSONStripped(nil); got != nil {
		t.Errorf("expected nil for nil node, got %q", got)
	}
}

func TestToJSONStripped_StripsDescriptionAndDefault(t *testing.T) {
	node := parseNode(t, "type: string\ndescription: A field\ndefault: hello")
	got := schemautil.ToJSONStripped(node)
	if got == nil {
		t.Fatal("unexpected nil result")
	}
	for _, key := range []string{`"description"`, `"default"`} {
		if contains(string(got), key) {
			t.Errorf("expected %s to be stripped from ToJSONStripped output %q", key, got)
		}
	}
	if !contains(string(got), `"type"`) {
		t.Errorf("expected type key to be present in %q", got)
	}
}

func TestToJSONStripped_EqualSchemasMaterially(t *testing.T) {
	a := parseNode(t, "type: object\nproperties:\n  x:\n    type: string\n    description: old desc\n    default: foo")
	b := parseNode(t, "type: object\nproperties:\n  x:\n    type: string\n    description: new desc\n    default: bar")

	aBytes := schemautil.ToJSONStripped(a)
	bBytes := schemautil.ToJSONStripped(b)

	if string(aBytes) != string(bBytes) {
		t.Errorf("schemas with only cosmetic differences should be materially equal:\na=%q\nb=%q", aBytes, bBytes)
	}
}

func TestToJSONStripped_DifferentSchemasMaterially(t *testing.T) {
	a := parseNode(t, "type: string")
	b := parseNode(t, "type: integer")

	if string(schemautil.ToJSONStripped(a)) == string(schemautil.ToJSONStripped(b)) {
		t.Error("schemas with different types should not be materially equal")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
