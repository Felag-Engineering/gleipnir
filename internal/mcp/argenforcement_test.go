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
