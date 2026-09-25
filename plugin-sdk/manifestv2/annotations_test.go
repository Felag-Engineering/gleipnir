package manifestv2

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// parseSchemaNode parses a YAML string into a *yaml.Node (document-unwrapped),
// mirroring what Parse produces for a config_schema field.
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

func TestSecretPropertyNames(t *testing.T) {
	tests := []struct {
		name   string
		schema *yaml.Node
		want   map[string]bool
	}{
		{name: "nil schema", schema: nil, want: nil},
		{
			name:   "no properties key",
			schema: parseSchemaNode(t, `{type: object}`),
			want:   nil,
		},
		{
			name: "one secret, one plain",
			schema: parseSchemaNode(t, `
type: object
properties:
  token:
    type: string
    x-gleipnir-secret: true
  region:
    type: string
`),
			want: map[string]bool{"token": true},
		},
		{
			name: "string true does not count",
			schema: parseSchemaNode(t, `
type: object
properties:
  token:
    type: string
    x-gleipnir-secret: "true"
`),
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SecretPropertyNames(tc.schema)
			if err != nil {
				t.Fatalf("SecretPropertyNames: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k := range tc.want {
				if !got[k] {
					t.Errorf("missing secret property %q", k)
				}
			}
		})
	}
}

func TestOptionsAnnotations(t *testing.T) {
	tests := []struct {
		name   string
		schema *yaml.Node
		want   map[string]OptionsSpec
	}{
		{name: "nil schema", schema: nil, want: nil},
		{
			name: "single-source annotation",
			schema: parseSchemaNode(t, `
type: object
properties:
  channel:
    type: string
    x-gleipnir-options:
      source: channels
`),
			want: map[string]OptionsSpec{"channel": {Source: "channels"}},
		},
		{
			name: "multi annotation",
			schema: parseSchemaNode(t, `
type: object
properties:
  channels:
    type: array
    x-gleipnir-options:
      source: channels
      multi: true
`),
			want: map[string]OptionsSpec{"channels": {Source: "channels", Multi: true}},
		},
		{
			name: "annotation with no source is ignored",
			schema: parseSchemaNode(t, `
type: object
properties:
  channel:
    type: string
    x-gleipnir-options: {}
`),
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OptionsAnnotations(tc.schema)
			if err != nil {
				t.Fatalf("OptionsAnnotations: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, wantSpec := range tc.want {
				if got[k] != wantSpec {
					t.Errorf("property %q = %+v, want %+v", k, got[k], wantSpec)
				}
			}
		})
	}
}
