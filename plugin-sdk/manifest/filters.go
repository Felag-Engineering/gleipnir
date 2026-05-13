package manifest

import "github.com/invopop/jsonschema"

// RegexField is a typed string that carries JSON Schema metadata indicating
// its value must be a valid regular expression. Use it as a struct field type
// when declaring event-kind binding schemas.
//
//	type MyFilter struct {
//	    Pattern manifest.RegexField `json:"pattern" jsonschema:"title=Pattern"`
//	}
type RegexField string

// JSONSchema implements the invopop/jsonschema.SchemaCustomizer interface so
// that ReflectSchema emits {type: string, format: regex} for this field.
func (RegexField) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Format: "regex"}
}

// ContainsField is a typed string for "contains substring" filter semantics.
// The host-side trigger engine interprets the value as a substring to match.
type ContainsField string

// JSONSchema implements jsonschema.SchemaCustomizer for ContainsField.
func (ContainsField) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string"}
}

// EqualsField is a typed string for exact-match filter semantics.
type EqualsField string

// JSONSchema implements jsonschema.SchemaCustomizer for EqualsField.
func (EqualsField) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string"}
}

// GlobField is a typed string for glob-pattern filter semantics (e.g. "#*").
// The host-side trigger engine interprets the value as a glob pattern.
type GlobField string

// JSONSchema implements jsonschema.SchemaCustomizer for GlobField.
func (GlobField) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Format: "glob"}
}
