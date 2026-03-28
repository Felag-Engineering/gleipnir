// Package google provides a Gemini-backed LLM client. This file implements the
// critical-path translation from standard JSON Schema (map[string]any, as returned
// by MCP tool discovery) into *genai.Schema structs required by the Gemini SDK.
// It is the foundation for ADR-017 parameter scoping, which narrows enum constraints
// on object properties before passing schemas to the agent.
package google

import (
	"fmt"

	"google.golang.org/genai"
)

// translateJSONSchemaToGenaiSchema converts a JSON Schema map into a *genai.Schema.
// Unknown or unsupported top-level keys (e.g. "$schema", "additionalProperties") are
// silently ignored. Missing or non-string "type" fields and unsupported types return
// an error rather than producing a silently broken schema.
func translateJSONSchemaToGenaiSchema(schema map[string]any) (*genai.Schema, error) {
	rawType, ok := schema["type"]
	if !ok {
		return nil, fmt.Errorf("schema missing required \"type\" field")
	}

	typeStr, ok := rawType.(string)
	if !ok {
		return nil, fmt.Errorf("schema \"type\" must be a string, got %T", rawType)
	}

	var genaiType genai.Type
	switch typeStr {
	case "string":
		genaiType = genai.TypeString
	case "integer":
		genaiType = genai.TypeInteger
	case "number":
		genaiType = genai.TypeNumber
	case "boolean":
		genaiType = genai.TypeBoolean
	case "array":
		genaiType = genai.TypeArray
	case "object":
		genaiType = genai.TypeObject
	default:
		return nil, fmt.Errorf("unsupported JSON Schema type %q", typeStr)
	}

	result := &genai.Schema{Type: genaiType}

	if desc, ok := schema["description"].(string); ok {
		result.Description = desc
	}

	if rawEnum, ok := schema["enum"].([]any); ok && len(rawEnum) > 0 {
		enums := make([]string, len(rawEnum))
		for i, v := range rawEnum {
			enums[i] = fmt.Sprintf("%v", v)
		}
		result.Enum = enums
		result.Format = "enum"
	}

	if rawRequired, ok := schema["required"].([]any); ok && len(rawRequired) > 0 {
		var required []string
		for _, v := range rawRequired {
			if s, ok := v.(string); ok {
				required = append(required, s)
			}
		}
		if len(required) > 0 {
			result.Required = required
		}
	}

	if typeStr == "object" {
		rawProps, hasPropKey := schema["properties"]
		if hasPropKey {
			propsMap, ok := rawProps.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("\"properties\" must be an object, got %T", rawProps)
			}
			if len(propsMap) > 0 {
				result.Properties = make(map[string]*genai.Schema, len(propsMap))
				for propName, propVal := range propsMap {
					propSchema, ok := propVal.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("property %q: value is not an object", propName)
					}
					translated, err := translateJSONSchemaToGenaiSchema(propSchema)
					if err != nil {
						return nil, fmt.Errorf("property %q: %w", propName, err)
					}
					result.Properties[propName] = translated
				}
			}
		}
	}

	if typeStr == "array" {
		rawItems, ok := schema["items"]
		if !ok {
			return nil, fmt.Errorf("array schema missing required \"items\" field")
		}
		itemsMap, ok := rawItems.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("array items: value is not an object")
		}
		items, err := translateJSONSchemaToGenaiSchema(itemsMap)
		if err != nil {
			return nil, fmt.Errorf("array items: %w", err)
		}
		result.Items = items
	}

	return result, nil
}
