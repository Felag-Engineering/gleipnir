package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// buildDiamondRefSchema builds the reported PoC shape: a "$defs" chain of
// depth levels, where level i's "allOf" references level i+1 TWICE (the
// same $defs entry, not two distinct ones) via "$ref", bottoming out at a
// plain leaf schema. Structurally shallow (root -> $defs -> level -> allOf
// -> $ref is about 5 levels of real JSON nesting) but implies 2^depth
// leaf-schema evaluations if fully expanded, because "allOf" always
// evaluates every branch and santhosh-tekuri/jsonschema/v6 has no
// memoization at VALIDATION time. Mirrors the measured PoC: depth 40, a few
// KB, ~130 total nodes.
func buildDiamondRefSchema(depth int) json.RawMessage {
	defs := make(map[string]any, depth)
	for i := 1; i <= depth; i++ {
		if i == depth {
			defs[fmt.Sprintf("level%d", i)] = map[string]any{"type": "object"}
			continue
		}
		next := fmt.Sprintf("#/$defs/level%d", i+1)
		defs[fmt.Sprintf("level%d", i)] = map[string]any{
			"allOf": []any{
				map[string]any{"$ref": next},
				map[string]any{"$ref": next},
			},
		}
	}
	root := map[string]any{
		"type":       "object",
		"properties": map[string]any{"q": map[string]any{"type": "string"}},
		"allOf": []any{
			map[string]any{"$ref": "#/$defs/level1"},
			map[string]any{"$ref": "#/$defs/level1"},
		},
		"$defs": defs,
	}
	b, err := json.Marshal(root)
	if err != nil {
		panic(fmt.Sprintf("buildDiamondRefSchema: marshal: %v", err))
	}
	return b
}

// TestNewArgValidator_RejectsUnboundedRefExpansion is the regression test
// for the reported PoC: a compact, perfectly valid-looking schema whose
// "$ref" graph implies 2^depth leaf-schema evaluations. It must be REFUSED
// at NewArgValidator (compile time), and PROMPTLY -- Validate takes no
// context.Context and the goroutine that would otherwise run it cannot be
// killed, so the only fix is never entering Validate at all. The deadline
// below is a generous CI-safe bound, not a tight wall-clock assertion: the
// fix computes this statically in time linear in document size and should
// return in well under a second even at depth 40.
func TestNewArgValidator_RejectsUnboundedRefExpansion(t *testing.T) {
	schema := buildDiamondRefSchema(40)
	t.Logf("PoC schema size: %d bytes", len(schema))

	done := make(chan error, 1)
	go func() {
		_, err := NewArgValidator(schema, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("NewArgValidator accepted a depth-40 diamond $ref schema; want a compile-time refusal")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NewArgValidator did not return within 5s -- the $ref expansion budget must be enforced statically, before Validate is ever reachable")
	}
}

// TestNewArgValidator_LegitimateDefsReuseCompiles guards against the budget
// being tight enough to break ordinary schemas: reusing one $defs entry
// from several SIBLING properties is common (not multiplicative -- sibling
// properties are summed, not chained into a doubling "$ref" diamond) and
// must still compile and enforce correctly.
func TestNewArgValidator_LegitimateDefsReuseCompiles(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"billing_address": {"$ref": "#/$defs/Address"},
			"shipping_address": {"$ref": "#/$defs/Address"},
			"return_address": {"$ref": "#/$defs/Address"}
		},
		"$defs": {
			"Address": {
				"type": "object",
				"properties": {
					"street": {"type": "string"},
					"city": {"type": "string"},
					"postal_code": {"type": "string"}
				},
				"required": ["street", "city"]
			}
		}
	}`)

	v, err := NewArgValidator(schema, nil)
	if err != nil {
		t.Fatalf("NewArgValidator rejected an ordinary schema with legitimate $defs reuse: %v", err)
	}

	valid := map[string]any{
		"billing_address": map[string]any{"street": "1 Main St", "city": "Springfield"},
	}
	if err := v.Validate(valid); err != nil {
		t.Errorf("Validate rejected a valid instance: %v", err)
	}

	invalid := map[string]any{
		"billing_address": map[string]any{"city": "Springfield"}, // missing required "street"
	}
	if err := v.Validate(invalid); err == nil {
		t.Error("Validate accepted an instance missing a required nested field")
	}
}

func TestCheckSchemaExpansionBudget(t *testing.T) {
	unmarshal := func(t *testing.T, raw json.RawMessage) any {
		t.Helper()
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal test schema: %v", err)
		}
		return doc
	}

	tests := []struct {
		name        string
		schema      func(t *testing.T) json.RawMessage
		wantErr     bool
		errContains string
	}{
		{
			name: "flat schema, no $ref at all",
			schema: func(t *testing.T) json.RawMessage {
				return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
			},
		},
		{
			name:   "shallow diamond well under budget",
			schema: func(t *testing.T) json.RawMessage { return buildDiamondRefSchema(10) }, // cost ~= 2^10 = 1024
		},
		{
			name:        "deep diamond exceeds the expansion-path budget",
			schema:      func(t *testing.T) json.RawMessage { return buildDiamondRefSchema(40) }, // cost ~= 2^40
			wantErr:     true,
			errContains: "validation paths",
		},
		{
			name: "self-referential $ref cycle is refused",
			schema: func(t *testing.T) json.RawMessage {
				return json.RawMessage(`{"type":"object","properties":{"children":{"type":"array","items":{"$ref":"#"}}}}`)
			},
			wantErr:     true,
			errContains: "cyclic",
		},
		{
			name: "indirect $ref cycle through $defs is refused",
			schema: func(t *testing.T) json.RawMessage {
				return json.RawMessage(`{"$ref":"#/$defs/a","$defs":{"a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}}}`)
			},
			wantErr:     true,
			errContains: "cyclic",
		},
		{
			name: "ref count cap: many sibling $refs to one small target, low path cost",
			schema: func(t *testing.T) json.RawMessage {
				props := make(map[string]any, maxSchemaRefCount+1)
				for i := 0; i < maxSchemaRefCount+1; i++ {
					props[fmt.Sprintf("p%d", i)] = map[string]any{"$ref": "#/$defs/Leaf"}
				}
				b, err := json.Marshal(map[string]any{
					"type":       "object",
					"properties": props,
					"$defs":      map[string]any{"Leaf": map[string]any{"type": "string"}},
				})
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				return b
			},
			wantErr:     true,
			errContains: "\"$ref\" occurrences",
		},
		{
			name: "defs count cap: many unreferenced $defs entries, low path cost",
			schema: func(t *testing.T) json.RawMessage {
				defs := make(map[string]any, maxSchemaDefsEntries+1)
				for i := 0; i < maxSchemaDefsEntries+1; i++ {
					defs[fmt.Sprintf("d%d", i)] = map[string]any{"type": "string"}
				}
				b, err := json.Marshal(map[string]any{
					"type":  "object",
					"$defs": defs,
				})
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				return b
			},
			wantErr:     true,
			errContains: "\"$defs\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := unmarshal(t, tc.schema(t))
			err := checkSchemaExpansionBudget(doc)
			if tc.wantErr {
				if err == nil {
					t.Fatal("checkSchemaExpansionBudget returned nil, want error")
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errContains)
				}
				return
			}
			if err != nil {
				t.Errorf("checkSchemaExpansionBudget returned unexpected error: %v", err)
			}
		})
	}
}
