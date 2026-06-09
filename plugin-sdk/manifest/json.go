package manifest

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// MarshalJSON implements json.Marshaler for Manifest. The default JSON encoder
// corrupts *yaml.Node fields (e.g. BindingSchema, ConfigSchema, InputSchema)
// because yaml.Node has no MarshalJSON — the encoder would emit the internal
// struct fields (Kind, Value, Tag, Content) instead of the schema content.
//
// This method delegates to manifest.Marshal (the audited, deterministic YAML
// path that runs sortMappingNode at every depth), then round-trips
// YAML → generic any → JSON. By reusing the canonical bytes produced for
// signing, gen-manifest's JSON output is byte-stable and lossless:
// jsonToCanonicalYAML's subsequent YAML round-trip produces output identical to
// calling manifest.Marshal directly.
func (m Manifest) MarshalJSON() ([]byte, error) {
	y, err := Marshal(&m)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := yaml.Unmarshal(y, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}
