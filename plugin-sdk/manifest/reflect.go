package manifest

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// ReflectSchema derives a JSON Schema from a Go struct value using
// github.com/invopop/jsonschema and returns it as a *yaml.Node for embedding
// in a Manifest field (e.g. EventKindDecl.BindingSchema).
//
// Reflector options and their rationale:
//
//   - DoNotReference: true — all $defs are inlined at their use site. This
//     removes ordering questions that arise when $defs maps are shared across
//     invocations and ensures the output is self-contained.
//
//   - ExpandedStruct: true — top-level struct fields are expanded directly into
//     the root schema's properties rather than being wrapped in a $ref.
//
//   - Anonymous: true — suppresses embedding of the struct type name as the
//     JSON Schema title.
//
// The $schema URL (invopop's draft 2020-12 header) is stripped from the root
// schema. Reasons: (a) santhosh-tekuri/v6 auto-detects the draft, so the URL
// adds no value; (b) stripping keeps reflected schemas byte-shape consistent
// with hand-authored YAML schema literals; (c) it insulates the codebase from
// invopop silently changing its default draft in a future release.
//
// additionalProperties: false is invopop's default for all struct reflections.
// This strict posture is intentional for binding schemas — do not try to
// override it via struct tags.
//
// Determinism note: this function returns whatever invopop produces; key
// ordering in the returned *yaml.Node is not guaranteed here. Signed bytes and
// canonical output are only produced after manifest.Marshal, which runs
// sortMappingNode at every depth of the final YAML tree.
func ReflectSchema(v any) (*yaml.Node, error) {
	r := &jsonschema.Reflector{
		Anonymous:      true,
		ExpandedStruct: true,
		DoNotReference: true,
	}
	s := r.Reflect(v)
	// Strip the "$schema" URL — see doc comment.
	s.Version = ""

	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("manifest: ReflectSchema json.Marshal: %w", err)
	}

	// Convert JSON → generic any → YAML bytes → yaml.Node. The two-step
	// conversion (instead of direct yaml.Unmarshal of the JSON) strips the
	// flow-style bits that yaml.v3 sets when parsing JSON. Without this,
	// manifest.Marshal would re-emit the schema as a single-line JSON-like
	// string (flow style) rather than block-indented YAML.
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return nil, fmt.Errorf("manifest: ReflectSchema json.Unmarshal (generic): %w", err)
	}
	yamlBytes, err := yaml.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("manifest: ReflectSchema yaml.Marshal (generic): %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &doc); err != nil {
		return nil, fmt.Errorf("manifest: ReflectSchema yaml.Unmarshal: %w", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0], nil
	}
	return &doc, nil
}

// MustReflectSchema is the panicking variant of ReflectSchema. It panics only
// on reflection errors from invopop; nil is never a valid input.
func MustReflectSchema(v any) *yaml.Node {
	node, err := ReflectSchema(v)
	if err != nil {
		panic(err)
	}
	return node
}
