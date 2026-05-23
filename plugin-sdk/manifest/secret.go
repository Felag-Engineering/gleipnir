package manifest

import "github.com/invopop/jsonschema"

// SecretAnnotationKey is the JSON Schema extension key that marks a config
// property as secret. The host redacts any property annotated with this key
// to "***" on every GET path (ADR-049). Tests and the host-side redactor
// reference this constant so the literal is never duplicated.
const SecretAnnotationKey = "x-gleipnir-secret"

// SecretString is a typed string for config fields that hold secrets
// (API tokens, passwords, private keys, etc.). Fields typed SecretString
// in a config schema struct will be reflected with x-gleipnir-secret: true,
// which causes the host to redact them to "***" on every GET response.
//
// Usage in a config struct:
//
//	type PluginConfig struct {
//	    AppToken manifest.SecretString `json:"app_token" jsonschema:"description=App-level token (xapp- prefix)"`
//	    Region   string                `json:"region"`
//	}
//
// Hand-authored YAML manifests should add the annotation directly:
//
//	config_schema:
//	  type: object
//	  properties:
//	    app_token:
//	      type: string
//	      x-gleipnir-secret: true
//
// See ADR-049 for the rationale behind this approach.
type SecretString string

// JSONSchema implements the invopop/jsonschema.SchemaCustomizer interface so
// that ReflectSchema emits {type: string, x-gleipnir-secret: true} for this
// field. The host-side configvalidate.SecretPropertyNames helper reads this
// annotation to build the set of properties to redact.
func (SecretString) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Extras: map[string]any{
			SecretAnnotationKey: true,
		},
	}
}
