package mcp

import (
	"encoding/json"
	"sort"
	"testing"
)

// assertSchemaProperties unmarshals schema and checks that the keys in the
// properties map exactly match wantKeys (order-independent).
func assertSchemaProperties(t *testing.T, schema json.RawMessage, wantKeys []string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("assertSchemaProperties: unmarshal: %v", err)
	}

	propsRaw, ok := m["properties"]
	if !ok {
		if len(wantKeys) > 0 {
			t.Errorf("schema has no 'properties', want keys %v", wantKeys)
		}
		return
	}
	propsMap, ok := propsRaw.(map[string]any)
	if !ok {
		t.Fatalf("assertSchemaProperties: 'properties' is not map[string]any")
	}

	var gotKeys []string
	for k := range propsMap {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)

	want := make([]string, len(wantKeys))
	copy(want, wantKeys)
	sort.Strings(want)

	if len(gotKeys) != len(want) {
		t.Errorf("property keys = %v, want %v", gotKeys, want)
		return
	}
	for i := range gotKeys {
		if gotKeys[i] != want[i] {
			t.Errorf("property keys = %v, want %v", gotKeys, want)
			return
		}
	}
}

func TestNarrowSchema(t *testing.T) {
	// Shared base schema used across cases.
	baseSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"namespace": {"type": "string"},
			"pod":       {"type": "string"},
			"force":     {"type": "boolean"}
		},
		"required": ["namespace", "pod"]
	}`)

	noPropsSchema := json.RawMessage(`{"type": "object"}`)

	tests := []struct {
		name          string
		schema        json.RawMessage
		params        map[string]any
		wantKeys      []string
		wantRequired  []string // nil means don't check; empty means key must be absent
		wantUnchanged bool     // true means result must be byte-identical to input
	}{
		{
			name:          "nil params returns schema unchanged",
			schema:        baseSchema,
			params:        nil,
			wantUnchanged: true,
		},
		{
			name:          "empty params returns schema unchanged",
			schema:        baseSchema,
			params:        map[string]any{},
			wantUnchanged: true,
		},
		{
			name:         "params with namespace only",
			schema:       baseSchema,
			params:       map[string]any{"namespace": "x"},
			wantKeys:     []string{"namespace"},
			wantRequired: []string{"namespace"},
		},
		{
			name:         "params with namespace and pod",
			schema:       baseSchema,
			params:       map[string]any{"namespace": "x", "pod": "y"},
			wantKeys:     []string{"namespace", "pod"},
			wantRequired: []string{"namespace", "pod"},
		},
		{
			name:         "nonexistent param key is silently dropped",
			schema:       baseSchema,
			params:       map[string]any{"namespace": "x", "nonexistent": "y"},
			wantKeys:     []string{"namespace"},
			wantRequired: []string{"namespace"},
		},
		{
			name:          "schema without properties is returned unchanged",
			schema:        noPropsSchema,
			params:        map[string]any{"namespace": "x"},
			wantUnchanged: true,
		},
		{
			name:         "force only — not in required, required key removed",
			schema:       baseSchema,
			params:       map[string]any{"force": true},
			wantKeys:     []string{"force"},
			wantRequired: []string{}, // expected absent
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NarrowSchema(tc.schema, tc.params)
			if err != nil {
				t.Fatalf("NarrowSchema returned unexpected error: %v", err)
			}

			if tc.wantUnchanged {
				if string(got) != string(tc.schema) {
					t.Errorf("expected schema unchanged\ngot:  %s\nwant: %s", got, tc.schema)
				}
				return
			}

			assertSchemaProperties(t, got, tc.wantKeys)

			if tc.wantRequired != nil {
				var m map[string]any
				if err := json.Unmarshal(got, &m); err != nil {
					t.Fatalf("unmarshal result: %v", err)
				}
				if len(tc.wantRequired) == 0 {
					// Required key must be absent.
					if _, present := m["required"]; present {
						t.Errorf("expected 'required' key to be absent, but it is present")
					}
				} else {
					reqRaw, ok := m["required"]
					if !ok {
						t.Errorf("'required' key missing, want %v", tc.wantRequired)
						return
					}
					reqSlice, ok := reqRaw.([]any)
					if !ok {
						t.Fatalf("'required' is not []any")
					}
					var gotReq []string
					for _, v := range reqSlice {
						if s, ok := v.(string); ok {
							gotReq = append(gotReq, s)
						}
					}
					sort.Strings(gotReq)
					want := make([]string, len(tc.wantRequired))
					copy(want, tc.wantRequired)
					sort.Strings(want)
					if len(gotReq) != len(want) {
						t.Errorf("required = %v, want %v", gotReq, want)
						return
					}
					for i := range gotReq {
						if gotReq[i] != want[i] {
							t.Errorf("required = %v, want %v", gotReq, want)
							return
						}
					}
				}
			}
		})
	}
}

func TestValidateCall(t *testing.T) {
	// Shared schema with namespace and pod properties (no required).
	validSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"namespace": {"type": "string"},
			"pod":       {"type": "string"}
		}
	}`)

	noPropsSchema := json.RawMessage(`{"type": "object"}`)

	// The #769 shape: a root-level branch keyword with NO top-level
	// "properties". NarrowSchema returns this unchanged, so before #769 the
	// gate had nothing to filter against and permitted every key.
	rootOneOfSchema := json.RawMessage(`{
		"oneOf": [
			{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},
			{"type":"object","properties":{"danger":{"type":"string"}},"required":["danger"]}
		]
	}`)

	tests := []struct {
		name        string
		schema      json.RawMessage
		params      map[string]any
		input       map[string]any
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid input with all declared keys",
			schema:  validSchema,
			input:   map[string]any{"namespace": "prod", "pod": "web-1"},
			wantErr: false,
		},
		{
			name:    "empty input returns nil",
			schema:  validSchema,
			input:   map[string]any{},
			wantErr: false,
		},
		{
			name:    "nil input returns nil",
			schema:  validSchema,
			input:   nil,
			wantErr: false,
		},
		{
			name:        "undeclared key causes error",
			schema:      validSchema,
			input:       map[string]any{"namespace": "prod", "undeclared": "val"},
			wantErr:     true,
			errContains: "undeclared",
		},
		{
			name:        "only bad key causes error",
			schema:      validSchema,
			input:       map[string]any{"bad_key": "val"},
			wantErr:     true,
			errContains: "bad_key",
		},
		{
			name:    "nil schema returns nil for any input",
			schema:  nil,
			input:   map[string]any{"anything": "val"},
			wantErr: false,
		},
		{
			name:    "schema without properties returns nil for any input",
			schema:  noPropsSchema,
			input:   map[string]any{"anything": "val"},
			wantErr: false,
		},

		// ── #769: the params allowlist holds regardless of schema shape ──
		{
			// Before #769 this returned nil: NarrowSchema leaves a
			// root-oneOf schema unchanged, so the properties check found
			// nothing to filter and every key was permitted — and #744's
			// ArgValidator accepts "danger" too, since it is valid under the
			// tool's own second branch. The operator's scoping was ignored
			// end to end.
			name:        "root oneOf: scoped-out key is refused by the params allowlist",
			schema:      rootOneOfSchema,
			params:      map[string]any{"a": map[string]any{}},
			input:       map[string]any{"danger": "rm -rf"},
			wantErr:     true,
			errContains: "params scoping",
		},
		{
			name:    "root oneOf: a params-listed key is still permitted",
			schema:  rootOneOfSchema,
			params:  map[string]any{"a": map[string]any{}},
			input:   map[string]any{"a": "ok"},
			wantErr: false,
		},
		{
			name:        "no top-level properties: scoped-out key is refused",
			schema:      noPropsSchema,
			params:      map[string]any{"a": map[string]any{}},
			input:       map[string]any{"anything": "val"},
			wantErr:     true,
			errContains: "params scoping",
		},
		{
			// No params block means no scoping was ever requested, so the
			// allowlist check does not apply and legacy behavior stands.
			name:    "root oneOf with no params is unchanged",
			schema:  rootOneOfSchema,
			input:   map[string]any{"danger": "rm -rf"},
			wantErr: false,
		},
		{
			// Check 2 is stricter than check 1 where it applies: "ghost" is
			// in params but is not a property the tool declares, so the
			// narrowed property set never contains it.
			name:        "params-listed key absent from the schema still fails the properties check",
			schema:      validSchema,
			params:      map[string]any{"ghost": map[string]any{}},
			input:       map[string]any{"ghost": "val"},
			wantErr:     true,
			errContains: "narrowed schema",
		},
		{
			// Deterministic offender: keys are sorted, so the
			// alphabetically-first disallowed key is always the one named.
			name:        "multiple disallowed keys report the first alphabetically",
			schema:      rootOneOfSchema,
			params:      map[string]any{"a": map[string]any{}},
			input:       map[string]any{"zeta": 1, "beta": 2, "danger": 3},
			wantErr:     true,
			errContains: `"beta"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCall(tc.schema, tc.params, tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateCall returned nil, want error containing %q", tc.errContains)
				}
				if tc.errContains != "" && !containsStr(err.Error(), tc.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("ValidateCall returned unexpected error: %v", err)
				}
			}
		})
	}
}

// containsStr is a simple substring check to avoid importing strings in tests.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
