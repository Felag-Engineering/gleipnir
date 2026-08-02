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
//
// Numeric-literal caveat: when params is non-empty this function round-trips
// the schema through json.Unmarshal/json.Marshal into map[string]any WITHOUT
// json.Decoder.UseNumber, so numeric literals are re-rendered through
// float64. Verified: "10000000000000000000000000000001" -> "1e+31" and
// "0.000000000000000000001" -> "1e-21" are cosmetic (semantically
// identical), but the first is a genuine value change -- an instance equal to
// the original 32-digit literal would no longer satisfy a schema const/enum
// built from it -- and a literal outside float64 range fails to unmarshal at
// all ("1e400" -> "cannot unmarshal number 1e400 into Go value of type
// float64"). This is pre-existing behavior that already applied to the
// LLM-facing narrowed schema; #744 (ArgValidator, validate.go) is the first
// consumer for which it can affect enforcement rather than only presentation.
// Not fixed here -- see validate.go and the #744 PR description.
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
//
// This is the ADR-017 key-allowlist gate: it enforces the operator's params
// narrowing and runs unconditionally, for every tool call. It is also the
// fallback exact-enforcement mechanism when no canonical schema is available
// to compile (NULL canonical_schema, or a schema that failed to compile —
// see execution/agent's compileArgValidator). When a canonical schema IS
// available, ArgValidator (validate.go) runs in addition to this gate to
// enforce type/branch/required-field correctness — it does not replace this
// gate, since a compiled JSON Schema with no "additionalProperties" accepts
// unknown keys, which would silently drop the operator's parameter-scoping
// boundary.
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
