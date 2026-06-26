package configvalidate

import (
	"fmt"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"gopkg.in/yaml.v3"
)

// OptionsSpec carries the parsed x-gleipnir-options annotation for one
// schema property. Source is the opaque string the plugin recognizes as a
// ListOptions source (e.g. "channels", "users"). Multi is true when the field
// stores a list and the UI should render a multi-chip combobox.
type OptionsSpec struct {
	Source string
	Multi  bool
}

// OptionsAnnotations returns a map from property name to OptionsSpec for every
// top-level property in schemaNode that carries an x-gleipnir-options annotation.
//
// The annotation is read at the schema-property level (not the items level),
// so it captures both scalar string fields annotated via typed wrappers and
// array fields annotated via hand-authored YAML (the canonical approach for
// array fields per R2). The annotation value must be a mapping with at least a
// "source" key; "multi" is optional and defaults to false.
//
// Returns nil, nil when schemaNode is nil or declares no annotated properties.
func OptionsAnnotations(schemaNode *yaml.Node) (map[string]OptionsSpec, error) {
	if schemaNode == nil {
		return nil, nil
	}

	var schema map[string]any
	if err := schemaNode.Decode(&schema); err != nil {
		return nil, fmt.Errorf("configvalidate: decode schema node for options annotations: %w", err)
	}

	propertiesRaw, ok := schema["properties"]
	if !ok {
		return nil, nil
	}
	propertiesMap, ok := propertiesRaw.(map[string]any)
	if !ok {
		return nil, nil
	}

	result := make(map[string]OptionsSpec)
	for name, propRaw := range propertiesMap {
		propMap, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		annotationRaw, exists := propMap[sdkmanifest.OptionsAnnotationKey]
		if !exists {
			continue
		}
		// The annotation must be a map with at least a "source" key.
		annotationMap, ok := annotationRaw.(map[string]any)
		if !ok {
			continue
		}
		source, _ := annotationMap["source"].(string)
		if source == "" {
			// Annotation exists but has no usable source — skip.
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
