// Package schemautil provides shared helpers for converting *yaml.Node JSON
// Schema values to canonical JSON bytes. Two variants are offered:
//
//   - ToJSON: faithful round-trip for use with schema compilers.
//   - ToJSONStripped: strips cosmetic keys (description, default) for
//     material-change detection.
package schemautil

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ToJSON converts a *yaml.Node to canonical JSON bytes suitable for feeding to
// a JSON Schema compiler. A nil node is treated as an empty object schema
// (validates anything). Returns an error when the node cannot be marshalled.
func ToJSON(node *yaml.Node) ([]byte, error) {
	if node == nil {
		return []byte(`{}`), nil
	}
	raw, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("yaml marshal: %w", err)
	}
	var tree any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	out, err := json.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}
	return out, nil
}

// ToJSONStripped produces a deterministic JSON byte representation of a
// *yaml.Node JSON Schema for material comparison. Two schemas are materially
// equal if and only if their canonical bytes are equal.
//
// Strategy:
//  1. nil node → nil (nil ↔ nil compares as equal, nil ↔ non-nil as different).
//  2. YAML-marshal the node to bytes via yaml.v3.
//  3. Unmarshal those bytes into map[string]any (generic tree).
//  4. Recursively strip "description" and "default" keys at every depth.
//  5. json.Marshal the cleaned tree — Go's json package emits map keys alphabetically.
//
// Errors in marshal/unmarshal are treated as an empty schema (nil return) so
// a parse failure on one side always differs from a valid schema on the other.
func ToJSONStripped(node *yaml.Node) []byte {
	if node == nil {
		return nil
	}
	raw, err := yaml.Marshal(node)
	if err != nil {
		return nil
	}
	var tree any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return nil
	}
	cleaned := stripCosmeticKeys(tree)
	out, err := json.Marshal(cleaned)
	if err != nil {
		return nil
	}
	return out
}

// stripCosmeticKeys recursively removes "description" and "default" keys from
// every map node in the tree.
func stripCosmeticKeys(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, child := range val {
			if k == "description" || k == "default" {
				continue
			}
			out[k] = stripCosmeticKeys(child)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			out[i] = stripCosmeticKeys(elem)
		}
		return out
	default:
		return v
	}
}
