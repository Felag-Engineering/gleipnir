package mcp

import (
	"encoding/json"
	"testing"
)

func TestClassifyArgEnforcement(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		canonical string
		want      ArgEnforcement
	}{
		{"compiles", `{"type":"object"}`, `{"type":"object"}`, ArgEnforcementExact},
		{"no schema declared", ``, ``, ArgEnforcementNoSchema},
		{"whitespace only schema", ` `, ` `, ArgEnforcementNoSchema},
		{"declared but normalization failed", `{"a":1,"a":2}`, ``, ArgEnforcementNoCanonicalSchema},
		{"unresolvable ref", `{"$ref":"#/$defs/missing"}`, `{"$ref":"#/$defs/missing"}`, ArgEnforcementUncompilable},
		{"external ref denied", `{"$ref":"file:///etc/passwd"}`, `{"$ref":"file:///etc/passwd"}`, ArgEnforcementUncompilable},
		{"draft-07 array items under 2020-12 default", `{"type":"array","items":[{"type":"string"}]}`, `{"items":[{"type":"string"}],"type":"array"}`, ArgEnforcementUncompilable},
		{"invalid pattern", `{"type":"string","pattern":"("}`, `{"pattern":"(","type":"string"}`, ArgEnforcementUncompilable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyArgEnforcement(json.RawMessage(tt.raw), json.RawMessage(tt.canonical))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if wantReduced := tt.want != ArgEnforcementExact; got.Reduced() != wantReduced {
				t.Errorf("Reduced() = %v, want %v", got.Reduced(), wantReduced)
			}
		})
	}
}

// TestClassifyArgEnforcementWithParams pins that narrowing can change compile
// success in both directions, which is why the unscoped classification on the
// Tools page cannot stand in for a policy's actual enforcement.
func TestClassifyArgEnforcementWithParams(t *testing.T) {
	badPatternInOtherProp := `{"properties":{"a":{"type":"string"},"b":{"pattern":"(","type":"string"}},"type":"object"}`
	refToScopedOutProp := `{"properties":{"a":{"$ref":"#/properties/b"},"b":{"type":"string"}},"type":"object"}`

	tests := []struct {
		name      string
		canonical string
		params    map[string]any
		want      ArgEnforcement
	}{
		{"bad pattern, unscoped", badPatternInOtherProp, nil, ArgEnforcementUncompilable},
		{"bad pattern scoped out", badPatternInOtherProp, map[string]any{"a": nil}, ArgEnforcementExact},
		{"bad pattern scoped in", badPatternInOtherProp, map[string]any{"b": nil}, ArgEnforcementUncompilable},
		{"ref target, unscoped", refToScopedOutProp, nil, ArgEnforcementExact},
		{"ref target scoped out", refToScopedOutProp, map[string]any{"a": nil}, ArgEnforcementUncompilable},
		{"ref target kept", refToScopedOutProp, map[string]any{"a": nil, "b": nil}, ArgEnforcementExact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := json.RawMessage(tt.canonical)
			got := ClassifyArgEnforcementWithParams(raw, raw, tt.params)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
