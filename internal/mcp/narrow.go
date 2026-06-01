package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// NarrowSchema filters a JSON Schema's properties and required fields to only
// those declared in params. When params is nil or empty the original schema is
// returned unchanged (zero allocation for the common case). If the schema has
// no "properties" key, or it is not a map, the original schema is also
// returned unchanged.
//
// For each key k in params that exists in the schema's properties:
//   - params[k] == nil  → preserve the original property unchanged (key-allowlist only).
//   - params[k] is []any  → build an enum from those elements.
//   - params[k] is a scalar (string/bool/int/float64)  → single-element enum.
//
// Enum-building produces a FRESH property map containing only "type", "enum",
// and "description" (all copied verbatim from the original). Extra constraints
// like format/minLength/pattern are dropped — they are redundant against an enum.
//
// Config errors (returned as errors, surfaced as build-time run failures):
//   - orig property is not a map[string]any (e.g. boolean schema)
//   - param value is a non-nil, non-scalar, non-slice type (nested map)
//   - empty []any value list
//   - []any with mixed JSON kinds (e.g. [1,"two"] rejected; [1,2.5] allowed)
//   - value kind does not match the schema's declared "type"
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
	for k, paramVal := range params {
		orig, exists := propsMap[k]
		if !exists {
			continue
		}

		// nil means key-allowlist only — preserve the original property. Check nil
		// before the scalar branch so a bare YAML `key:` (which parses as nil) does
		// not accidentally become enum:[null].
		if paramVal == nil {
			narrowedProps[k] = orig
			continue
		}

		// orig must be a map for us to build a constrained enum property. Boolean
		// schemas (true/false) and other non-map values are unsupported.
		origMap, ok := orig.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("param %q: original schema property is not an object (boolean schemas are unsupported)", k)
		}

		// Determine the enum entries from the param value.
		var entries []any
		switch v := paramVal.(type) {
		case []any:
			entries = v
		case string, bool, int, float64:
			entries = []any{v}
		default:
			// Nested maps and other complex values are out of scope for v1.
			return nil, fmt.Errorf("param %q: nested or unsupported value type %T (nested value scoping not supported)", k, paramVal)
		}

		if len(entries) == 0 {
			return nil, fmt.Errorf("param %q: empty value list", k)
		}

		// Validate that all entries share the same JSON kind, and that the kind is
		// compatible with the schema's declared type (if any).
		if err := validateEnumEntries(k, entries, origMap); err != nil {
			return nil, err
		}

		// Build a FRESH property map. Only carry forward "type", "enum", and
		// "description" — other JSON Schema keywords (format, minLength, pattern, …)
		// are redundant against an enum and are intentionally dropped (ADR-017).
		narrowedProp := make(map[string]any, 3)
		if t, ok := origMap["type"]; ok {
			narrowedProp["type"] = t
		}
		narrowedProp["enum"] = entries
		if desc, ok := origMap["description"]; ok {
			narrowedProp["description"] = desc
		}
		narrowedProps[k] = narrowedProp
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

// jsonKind returns a canonical kind string for a Go value as it would appear
// in JSON: "string", "number", "bool", or "null". Returns "" for types that
// are not valid JSON scalar primitives.
func jsonKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case int, float64:
		// Both Go int and float64 marshal to a JSON number.
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return ""
	}
}

// validateEnumEntries checks that every entry in entries shares the same JSON
// kind, and that the kind is compatible with the schema's declared "type" (if
// present). type may be a string or a []any of strings (union type).
func validateEnumEntries(paramKey string, entries []any, origMap map[string]any) error {
	// Verify all entries share the same JSON kind. [1, 2.5] is allowed because
	// both Go int and float64 are JSON numbers. [1, "two"] is rejected.
	firstKind := jsonKind(entries[0])
	if firstKind == "" {
		return fmt.Errorf("param %q: entry %v has unsupported type %T for JSON enum", paramKey, entries[0], entries[0])
	}
	for i, entry := range entries[1:] {
		k := jsonKind(entry)
		if k == "" {
			return fmt.Errorf("param %q: entry %v at index %d has unsupported type %T for JSON enum", paramKey, entry, i+1, entry)
		}
		if k != firstKind {
			return fmt.Errorf("param %q: mixed JSON kinds in value list (entry 0 is %s, entry %d is %s)", paramKey, firstKind, i+1, k)
		}
	}

	// Check that the entry kind is compatible with the schema's declared type.
	// If "type" is absent we skip the check.
	typeRaw, hasType := origMap["type"]
	if !hasType {
		return nil
	}

	// Collect declared types into a slice (handles both "string" and ["string","null"]).
	var declaredTypes []string
	switch t := typeRaw.(type) {
	case string:
		declaredTypes = []string{t}
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				declaredTypes = append(declaredTypes, s)
			}
		}
	default:
		// Unknown type format — skip check.
		return nil
	}

	// Check each entry against the declared types. A numeric entry satisfies both
	// "integer" and "number" because JSON has a single number type; yaml.v3 gives
	// us Go int for whole numbers and float64 for decimals — both are valid for
	// either schema type.
	for _, entry := range entries {
		if !kindMatchesDeclaredTypes(jsonKind(entry), declaredTypes) {
			return fmt.Errorf("param %q: value %v (%s) is incompatible with schema type %v", paramKey, entry, jsonKind(entry), typeRaw)
		}
	}
	return nil
}

// kindMatchesDeclaredTypes returns true if the JSON kind of an enum entry is
// compatible with at least one of the declared schema types.
func kindMatchesDeclaredTypes(kind string, declaredTypes []string) bool {
	for _, dt := range declaredTypes {
		switch dt {
		case "string":
			if kind == "string" {
				return true
			}
		case "number", "integer":
			// JSON number: both Go int and float64 satisfy integer and number.
			if kind == "number" {
				return true
			}
		case "boolean":
			if kind == "bool" {
				return true
			}
		case "null":
			if kind == "null" {
				return true
			}
		}
	}
	return false
}

// ValidateCall checks that every key in input is present in the narrowed
// schema's properties. If input is empty, or the schema has no properties,
// it returns nil. After key-presence validation, it checks that any input
// value whose property carries an "enum" is a member of that enum.
//
// Enum membership is tested via JSON marshaling: both the input value and each
// candidate enum entry are marshaled to JSON and compared byte-by-byte. This
// avoids YAML-int / JSON-float64 round-trip mismatches (yaml.v3 gives Go int;
// json.Unmarshal gives float64; json.Marshal("22") is identical for both).
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

	// First pass: key-presence check. An undeclared key is rejected immediately
	// with the original "not permitted" error before any marshal work occurs.
	for k := range input {
		if _, allowed := propsMap[k]; !allowed {
			return fmt.Errorf("input key %q is not permitted by the narrowed schema", k)
		}
	}

	// Second pass: enum-membership check for properties that carry an "enum".
	for k, inputVal := range input {
		propRaw, ok := propsMap[k]
		if !ok {
			continue
		}
		propMap, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		enumRaw, ok := propMap["enum"]
		if !ok {
			continue
		}
		enumEntries, ok := enumRaw.([]any)
		if !ok {
			continue
		}

		matched, err := isInEnum(inputVal, enumEntries)
		if err != nil {
			return fmt.Errorf("enum check for %q: %w", k, err)
		}
		if !matched {
			return fmt.Errorf("input value for %q is not permitted by the policy enum constraint", k)
		}
	}
	return nil
}

// isInEnum reports whether val is byte-equal (after JSON marshaling) to any
// entry in entries. A marshal error propagates upward — it is never silently
// treated as a non-match, because a broken value that can't be marshaled should
// not be allowed through security enforcement.
func isInEnum(val any, entries []any) (bool, error) {
	valBytes, err := json.Marshal(val)
	if err != nil {
		return false, fmt.Errorf("marshal input value: %w", err)
	}
	for _, entry := range entries {
		entryBytes, err := json.Marshal(entry)
		if err != nil {
			return false, fmt.Errorf("marshal enum entry: %w", err)
		}
		if bytes.Equal(valBytes, entryBytes) {
			return true, nil
		}
	}
	return false, nil
}
