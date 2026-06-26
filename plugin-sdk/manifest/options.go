package manifest

import "github.com/invopop/jsonschema"

// OptionsAnnotationKey is the JSON Schema extension key that marks a config
// property as having a dynamic option provider. The host reads this annotation
// to determine whether to render an async combobox in the admin UI instead of
// a plain text input. Schema endpoints decode manifest YAML verbatim, so the
// annotation reaches the frontend unchanged.
const OptionsAnnotationKey = "x-gleipnir-options"

// OptionsAnnotation is the typed shape of the x-gleipnir-options annotation
// placed at the schema property level (not the items level for arrays). The
// host's configvalidate.OptionsAnnotations helper reads this from parsed YAML.
//
// source is the opaque string that identifies which option source the plugin
// exposes via ConfigOptionsService.ListOptions (e.g. "channels", "users").
// multi indicates that the field stores a list and the UI should allow multiple
// selections (renders as a multi-chip combobox).
type OptionsAnnotation struct {
	Source string `json:"source"`
	Multi  bool   `json:"multi,omitempty"`
}

// OptionsString is a typed string for config fields that have a dynamic option
// provider (single-value string fields, e.g. a binding user field).
//
// Fields typed OptionsString in a config schema struct emit
// x-gleipnir-options: {source: <source>} at the property level when reflected.
// This approach works only for scalar string fields. For array fields (e.g.
// channels []string) the annotation must be placed in a hand-authored YAML
// schema literal because the invopop/jsonschema reflector places SchemaCustomizer
// annotations at the item type level — not the array property level — which is
// the wrong location for the host's property-level check.
//
// Usage in a config schema struct (single-value field):
//
//	type MyConfig struct {
//	    User options.OptionsString `json:"user" jsonschema:"description=Slack user ID"`
//	}
//
// For array fields use a hand-authored YAML literal:
//
//	subscription_schema:
//	  type: object
//	  properties:
//	    channels:
//	      type: array
//	      x-gleipnir-options:
//	        source: channels
//	        multi: true
//	      items:
//	        type: string
//
// See issue #622 and ADR-R2 for the rationale.
type OptionsString string

// optionsStringSchema is the default JSONSchema for an OptionsString field.
// The source and multi values are set by OptionsStringWithSource.
func (OptionsString) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
	}
}

// OptionsStringSource returns a SchemaCustomizer that emits
// x-gleipnir-options: {source: source} for a scalar OptionsString field.
// multi should be false for a single-value string field; array fields need
// a hand-authored YAML schema (see OptionsString doc comment).
//
// Usage:
//
//	type MyBinding struct {
//	    User string `json:"user" jsonschema:"description=Slack user ID"`
//	}
//
//	// In a reflect call, pass a customizer to annotate the User field:
//	manifest.MustReflectSchemaWithOptions(MyBinding{},
//	    manifest.OptionsStringSource("user", "users", false))
//
// In practice for the Slack plugin, EqualsField (which already emits
// {type: string}) is used for the user binding field. The annotation is
// applied via a hand-authored schema node or the reflect customizer approach.
// See R2 in the plan-review revisions.
func OptionsStringSource(fieldName, source string, multi bool) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Extras: map[string]any{
			OptionsAnnotationKey: OptionsAnnotation{Source: source, Multi: multi},
		},
	}
}
