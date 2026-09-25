package configvalidate

import (
	"fmt"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
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
//
// This forwards to plugin-sdk/manifestv2's copy of the same logic — see
// SecretPropertyNames's doc comment for why a v2-only manifest package carries
// this logic, and for the consolidation history (#950).
func OptionsAnnotations(schemaNode *yaml.Node) (map[string]OptionsSpec, error) {
	specs, err := manifestv2.OptionsAnnotations(schemaNode)
	if err != nil {
		return nil, fmt.Errorf("configvalidate: %w", err)
	}
	if specs == nil {
		return nil, nil
	}
	result := make(map[string]OptionsSpec, len(specs))
	for name, spec := range specs {
		result[name] = OptionsSpec{Source: spec.Source, Multi: spec.Multi}
	}
	return result, nil
}
