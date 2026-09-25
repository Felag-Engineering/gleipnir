package manifestv2

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// SecretAnnotationKey is the JSON Schema extension key that marks a config
// property as secret. The host redacts any property annotated with this key
// to "***" on every GET path (spec §9, the same rule plugin-sdk/manifest
// establishes for the v1.1 substrate). Copied rather than imported — see the
// package doc — so the literal is duplicated here rather than the two
// manifest formats sharing a type.
const SecretAnnotationKey = "x-gleipnir-secret"

// OptionsAnnotationKey is the JSON Schema extension key that marks a config
// property as having a dynamic option provider, rendered as an async combobox
// rather than a plain text input. Copied rather than imported; see
// SecretAnnotationKey.
const OptionsAnnotationKey = "x-gleipnir-options"

// OptionsSpec carries the parsed x-gleipnir-options annotation for one schema
// property. Source is the opaque string the plugin recognizes as a
// ListOptions source (e.g. "channels", "users"); Multi is true when the field
// stores a list and the UI should render a multi-chip combobox.
type OptionsSpec struct {
	Source string
	Multi  bool
}

// SecretPropertyNames returns the set of top-level property names in
// schemaNode (a config_schema or user_config_schema node) that are marked
// with x-gleipnir-secret: true. Only the root properties map is inspected;
// nested object secrets are out of scope, same as v1.
//
// Returns a nil map (not an error) when schemaNode is nil or declares no
// properties key. The annotation value must be exactly the boolean true — the
// string "true" and other non-boolean values are not included.
//
// This is the ADR-049 redaction path's implementation for both manifest
// formats: internal/plugin/configvalidate.SecretPropertyNames forwards here
// rather than re-implementing the same logic (#950). It lives in this v2-only
// package, not a shared leaf package, because the two manifest formats are
// kept as separate types on purpose (see the package doc) — this function
// operates on a raw *yaml.Node schema fragment, which is version-neutral, so
// there is nothing v1-specific for configvalidate's copy to diverge on.
func SecretPropertyNames(schemaNode *yaml.Node) (map[string]bool, error) {
	properties, err := schemaProperties(schemaNode)
	if err != nil {
		return nil, fmt.Errorf("manifestv2: secret property names: %w", err)
	}
	if properties == nil {
		return nil, nil
	}

	secrets := make(map[string]bool)
	for name, propRaw := range properties {
		propMap, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		annotationVal, exists := propMap[SecretAnnotationKey]
		if !exists {
			continue
		}
		if boolVal, isBool := annotationVal.(bool); isBool && boolVal {
			secrets[name] = true
		}
	}
	if len(secrets) == 0 {
		return nil, nil
	}
	return secrets, nil
}

// OptionsAnnotations returns a map from property name to OptionsSpec for
// every top-level property in schemaNode that carries an x-gleipnir-options
// annotation. The annotation value must be a mapping with at least a "source"
// key; "multi" is optional and defaults to false.
//
// Returns nil, nil when schemaNode is nil or declares no annotated properties.
//
// internal/plugin/configvalidate.OptionsAnnotations forwards here for the
// same reason SecretPropertyNames does — see that doc comment.
func OptionsAnnotations(schemaNode *yaml.Node) (map[string]OptionsSpec, error) {
	properties, err := schemaProperties(schemaNode)
	if err != nil {
		return nil, fmt.Errorf("manifestv2: options annotations: %w", err)
	}
	if properties == nil {
		return nil, nil
	}

	result := make(map[string]OptionsSpec)
	for name, propRaw := range properties {
		propMap, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		annotationRaw, exists := propMap[OptionsAnnotationKey]
		if !exists {
			continue
		}
		annotationMap, ok := annotationRaw.(map[string]any)
		if !ok {
			continue
		}
		source, _ := annotationMap["source"].(string)
		if source == "" {
			continue
		}
		multi, _ := annotationMap["multi"].(bool)
		result[name] = OptionsSpec{Source: source, Multi: multi}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// schemaProperties decodes schemaNode and returns its top-level "properties"
// mapping as a generic map, or nil when the node is nil, declares no
// properties key, or the key is not a mapping.
func schemaProperties(schemaNode *yaml.Node) (map[string]any, error) {
	if schemaNode == nil {
		return nil, nil
	}
	var schema map[string]any
	if err := schemaNode.Decode(&schema); err != nil {
		return nil, fmt.Errorf("decode schema node: %w", err)
	}
	propertiesRaw, ok := schema["properties"]
	if !ok {
		return nil, nil
	}
	properties, ok := propertiesRaw.(map[string]any)
	if !ok {
		return nil, nil
	}
	return properties, nil
}
