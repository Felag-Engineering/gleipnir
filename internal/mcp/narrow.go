package mcp

import (
	"encoding/json"
	"fmt"
)

// NarrowSchema filters a JSON Schema's properties and required fields to only
// those declared in params. When params is nil or empty the original schema is
// returned unchanged (zero allocation for the common case). If the schema has
// no "properties" key, or it is not a map, the original schema is also
// returned unchanged.
func NarrowSchema(schema json.RawMessage, params map[string]any) (json.RawMessage, error) {
	if len(params) == 0 {
		return schema, nil
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		return nil, fmt.Errorf("unmarshal schema: %w", err)
	}

	propsRaw, ok := schemaMap["properties"]
	if !ok {
		return schema, nil
	}
	propsMap, ok := propsRaw.(map[string]any)
	if !ok {
		return schema, nil
	}

	// Build narrowed properties containing only keys present in both params and the schema.
	narrowedProps := make(map[string]any, len(params))
	for k := range params {
		if v, exists := propsMap[k]; exists {
			narrowedProps[k] = v
		}
	}
	schemaMap["properties"] = narrowedProps

	// Filter required array to only items also in params.
	if reqRaw, ok := schemaMap["required"]; ok {
		if reqSlice, ok := reqRaw.([]any); ok {
			var narrowedReq []any
			for _, item := range reqSlice {
				if s, ok := item.(string); ok {
					if _, inParams := params[s]; inParams {
						narrowedReq = append(narrowedReq, item)
					}
				}
			}
			if len(narrowedReq) == 0 {
				delete(schemaMap, "required")
			} else {
				schemaMap["required"] = narrowedReq
			}
		}
	}

	out, err := json.Marshal(schemaMap)
	if err != nil {
		return nil, fmt.Errorf("marshal narrowed schema: %w", err)
	}
	return out, nil
}

// ValidateCall checks that every key in input is present in the narrowed
// schema's properties. If input is empty, or the schema has no properties,
// it returns nil. This performs key-presence validation only — no type checks.
func ValidateCall(narrowedSchema json.RawMessage, input map[string]any) error {
	if len(input) == 0 {
		return nil
	}
	if len(narrowedSchema) == 0 {
		return nil
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(narrowedSchema, &schemaMap); err != nil {
		return fmt.Errorf("unmarshal schema: %w", err)
	}

	propsRaw, ok := schemaMap["properties"]
	if !ok {
		return nil
	}
	propsMap, ok := propsRaw.(map[string]any)
	if !ok {
		return nil
	}

	for k := range input {
		if _, allowed := propsMap[k]; !allowed {
			return fmt.Errorf("input key %q is not permitted by the narrowed schema", k)
		}
	}
	return nil
}
