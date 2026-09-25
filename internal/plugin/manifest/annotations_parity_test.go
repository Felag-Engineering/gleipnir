package manifest_test

// TestAnnotationParity pins internal/plugin/configvalidate's live
// SecretPropertyNames/OptionsAnnotations (the ADR-049 redaction path,
// deliberately left unchanged pre-demo) and plugin-sdk/manifestv2's
// duplicate of the same logic to identical output over a shared input
// table. It exists because #950 review decided against consolidating the
// two now: once configvalidate forwards to plugin-sdk/manifestv2 (#950),
// this test is deleted along with the duplication it is guarding against
// silently drifting.

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"gopkg.in/yaml.v3"
)

func parseParityNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parseParityNode: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return &doc
}

func TestAnnotationParity_SecretPropertyNames(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{name: "nil schema", schema: ""},
		{name: "no properties key", schema: `{type: object}`},
		{
			name: "one secret, one plain",
			schema: `
type: object
properties:
  token: {type: string, x-gleipnir-secret: true}
  region: {type: string}
`,
		},
		{
			name: "secret false is not secret",
			schema: `
type: object
properties:
  token: {type: string, x-gleipnir-secret: false}
`,
		},
		{
			name: "secret as a non-bool string does not count",
			schema: `
type: object
properties:
  token: {type: string, x-gleipnir-secret: "true"}
`,
		},
		{
			name: "nested object secret is out of scope for both",
			schema: `
type: object
properties:
  outer:
    type: object
    properties:
      token: {type: string, x-gleipnir-secret: true}
`,
		},
		{
			name: "array property is not itself secret",
			schema: `
type: object
properties:
  tokens:
    type: array
    items: {type: string}
    x-gleipnir-secret: true
`,
		},
		{
			name: "$ref property carries no annotation to find",
			schema: `
type: object
properties:
  token: {$ref: "#/$defs/Token"}
$defs:
  Token: {type: string, x-gleipnir-secret: true}
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var node *yaml.Node
			if tc.schema != "" {
				node = parseParityNode(t, tc.schema)
			}

			got, err := configvalidate.SecretPropertyNames(node)
			if err != nil {
				t.Fatalf("configvalidate.SecretPropertyNames: %v", err)
			}
			want, err := manifestv2.SecretPropertyNames(node)
			if err != nil {
				t.Fatalf("manifestv2.SecretPropertyNames: %v", err)
			}

			if len(got) != len(want) {
				t.Fatalf("configvalidate=%v manifestv2=%v, want identical", got, want)
			}
			for k := range want {
				if !got[k] {
					t.Errorf("configvalidate missing secret property %q present in manifestv2's result %v", k, want)
				}
			}
		})
	}
}

func TestAnnotationParity_OptionsAnnotations(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{name: "nil schema", schema: ""},
		{name: "no properties key", schema: `{type: object}`},
		{
			name: "single-source annotation",
			schema: `
type: object
properties:
  channel: {type: string, x-gleipnir-options: {source: channels}}
`,
		},
		{
			name: "multi annotation",
			schema: `
type: object
properties:
  channels: {type: array, x-gleipnir-options: {source: channels, multi: true}}
`,
		},
		{
			name: "annotation with no source is ignored",
			schema: `
type: object
properties:
  channel: {type: string, x-gleipnir-options: {}}
`,
		},
		{
			name: "nested object annotation is out of scope for both",
			schema: `
type: object
properties:
  outer:
    type: object
    properties:
      channel: {type: string, x-gleipnir-options: {source: channels}}
`,
		},
		{
			name: "$ref property carries no annotation to find",
			schema: `
type: object
properties:
  channel: {$ref: "#/$defs/Channel"}
$defs:
  Channel: {type: string, x-gleipnir-options: {source: channels}}
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var node *yaml.Node
			if tc.schema != "" {
				node = parseParityNode(t, tc.schema)
			}

			got, err := configvalidate.OptionsAnnotations(node)
			if err != nil {
				t.Fatalf("configvalidate.OptionsAnnotations: %v", err)
			}
			want, err := manifestv2.OptionsAnnotations(node)
			if err != nil {
				t.Fatalf("manifestv2.OptionsAnnotations: %v", err)
			}

			if len(got) != len(want) {
				t.Fatalf("configvalidate=%v manifestv2=%v, want identical", got, want)
			}
			for k, wantSpec := range want {
				gotSpec, ok := got[k]
				if !ok {
					t.Errorf("configvalidate missing options property %q present in manifestv2's result", k)
					continue
				}
				if gotSpec.Source != wantSpec.Source || gotSpec.Multi != wantSpec.Multi {
					t.Errorf("property %q: configvalidate=%+v manifestv2=%+v, want identical", k, gotSpec, wantSpec)
				}
			}
		})
	}
}
