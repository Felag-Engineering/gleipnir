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

// assertPropertyEnum unmarshals schema and checks that properties[key] has the
// expected "enum" array. wantEnum entries are compared via JSON marshaling to
// match the round-trip semantics used by NarrowSchema and ValidateCall.
func assertPropertyEnum(t *testing.T, schema json.RawMessage, key string, wantEnum []any) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("assertPropertyEnum: unmarshal: %v", err)
	}
	propsMap, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyEnum: schema has no properties map")
	}
	propRaw, ok := propsMap[key]
	if !ok {
		t.Fatalf("assertPropertyEnum: property %q not found", key)
	}
	propMap, ok := propRaw.(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyEnum: property %q is not a map", key)
	}
	enumRaw, ok := propMap["enum"]
	if !ok {
		t.Fatalf("assertPropertyEnum: property %q has no 'enum' key", key)
	}
	gotEnum, ok := enumRaw.([]any)
	if !ok {
		t.Fatalf("assertPropertyEnum: property %q 'enum' is not []any", key)
	}

	if len(gotEnum) != len(wantEnum) {
		t.Errorf("property %q enum length = %d, want %d (got %v, want %v)", key, len(gotEnum), len(wantEnum), gotEnum, wantEnum)
		return
	}
	for i, want := range wantEnum {
		wantBytes, _ := json.Marshal(want)
		gotBytes, _ := json.Marshal(gotEnum[i])
		if string(wantBytes) != string(gotBytes) {
			t.Errorf("property %q enum[%d] = %v (json: %s), want %v (json: %s)", key, i, gotEnum[i], gotBytes, want, wantBytes)
		}
	}
}

// assertPropertyType unmarshals schema and checks that properties[key] has the
// expected "type" value. wantType may be a string or slice.
func assertPropertyType(t *testing.T, schema json.RawMessage, key string, wantType any) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("assertPropertyType: unmarshal: %v", err)
	}
	propsMap, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyType: schema has no properties map")
	}
	propRaw, ok := propsMap[key]
	if !ok {
		t.Fatalf("assertPropertyType: property %q not found", key)
	}
	propMap, ok := propRaw.(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyType: property %q is not a map", key)
	}
	wantBytes, _ := json.Marshal(wantType)
	gotBytes, _ := json.Marshal(propMap["type"])
	if string(wantBytes) != string(gotBytes) {
		t.Errorf("property %q type = %v, want %v", key, propMap["type"], wantType)
	}
}

// assertPropertyDescription checks that properties[key] has the expected "description".
func assertPropertyDescription(t *testing.T, schema json.RawMessage, key string, wantDesc string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("assertPropertyDescription: unmarshal: %v", err)
	}
	propsMap, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyDescription: schema has no properties map")
	}
	propRaw, ok := propsMap[key]
	if !ok {
		t.Fatalf("assertPropertyDescription: property %q not found", key)
	}
	propMap, ok := propRaw.(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyDescription: property %q is not a map", key)
	}
	gotDesc, _ := propMap["description"].(string)
	if gotDesc != wantDesc {
		t.Errorf("property %q description = %q, want %q", key, gotDesc, wantDesc)
	}
}

// assertPropertyNoKey checks that properties[key] does NOT have the named sub-key.
func assertPropertyNoKey(t *testing.T, schema json.RawMessage, propKey, subKey string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("assertPropertyNoKey: unmarshal: %v", err)
	}
	propsMap, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyNoKey: schema has no properties map")
	}
	propRaw, ok := propsMap[propKey]
	if !ok {
		t.Fatalf("assertPropertyNoKey: property %q not found", propKey)
	}
	propMap, ok := propRaw.(map[string]any)
	if !ok {
		t.Fatalf("assertPropertyNoKey: property %q is not a map", propKey)
	}
	if _, found := propMap[subKey]; found {
		t.Errorf("property %q unexpectedly contains key %q", propKey, subKey)
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
		wantEnumFor   map[string][]any // key → expected enum entries (nil means skip)
		wantErr       bool
		errContains   string
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
		// --- Existing scalar-param cases updated with explicit enum assertions ---
		// These four cases previously only checked key-set; they now also verify
		// that each scalar param value becomes a single-element enum (ADR-017).
		{
			name:         "params with namespace only",
			schema:       baseSchema,
			params:       map[string]any{"namespace": "x"},
			wantKeys:     []string{"namespace"},
			wantRequired: []string{"namespace"},
			wantEnumFor:  map[string][]any{"namespace": {"x"}},
		},
		{
			name:         "params with namespace and pod",
			schema:       baseSchema,
			params:       map[string]any{"namespace": "x", "pod": "y"},
			wantKeys:     []string{"namespace", "pod"},
			wantRequired: []string{"namespace", "pod"},
			wantEnumFor:  map[string][]any{"namespace": {"x"}, "pod": {"y"}},
		},
		{
			name:         "nonexistent param key is silently dropped",
			schema:       baseSchema,
			params:       map[string]any{"namespace": "x", "nonexistent": "y"},
			wantKeys:     []string{"namespace"},
			wantRequired: []string{"namespace"},
			wantEnumFor:  map[string][]any{"namespace": {"x"}},
		},
		{
			name:         "force only — not in required, required key removed",
			schema:       baseSchema,
			params:       map[string]any{"force": true},
			wantKeys:     []string{"force"},
			wantRequired: []string{}, // expected absent
			wantEnumFor:  map[string][]any{"force": {true}},
		},
		// --- nil value: key-allowlist only, no enum ---
		{
			name:   "nil value preserves original property unchanged",
			schema: baseSchema,
			params: map[string]any{"namespace": nil},
			// The original property {"type":"string"} must come through intact.
			wantKeys: []string{"namespace"},
			// wantEnumFor intentionally absent: property must have no enum key.
		},
		// --- Slice of strings → enum ---
		{
			name: "slice of strings becomes enum with description preserved and minLength dropped",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"namespace": {"type": "string", "description": "target namespace", "minLength": 1}
				}
			}`),
			params:      map[string]any{"namespace": []any{"worker-01", "worker-02"}},
			wantKeys:    []string{"namespace"},
			wantEnumFor: map[string][]any{"namespace": {"worker-01", "worker-02"}},
		},
		// --- Numeric heterogeneity: [1, 2.5] must succeed (both JSON number-kind) ---
		{
			name: "numeric heterogeneity int and float64 is allowed",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"port": {"type": "number"}
				}
			}`),
			params:      map[string]any{"port": []any{int(80), float64(443.5)}},
			wantKeys:    []string{"port"},
			wantEnumFor: map[string][]any{"port": {int(80), float64(443.5)}},
		},
		// --- Numeric enum compatible with both type:integer and type:number ---
		{
			name: "numeric enum matches schema type integer",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"port": {"type": "integer"}
				}
			}`),
			params:      map[string]any{"port": []any{int(22), int(80)}},
			wantKeys:    []string{"port"},
			wantEnumFor: map[string][]any{"port": {int(22), int(80)}},
		},
		{
			name: "numeric enum matches schema type number",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"port": {"type": "number"}
				}
			}`),
			params:      map[string]any{"port": []any{int(22), int(80)}},
			wantKeys:    []string{"port"},
			wantEnumFor: map[string][]any{"port": {int(22), int(80)}},
		},
		// --- type as array union: member and non-member ---
		{
			name: "type-union array: string value matches [string null] union",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"region": {"type": ["string", "null"]}
				}
			}`),
			params:      map[string]any{"region": "us-east-1"},
			wantKeys:    []string{"region"},
			wantEnumFor: map[string][]any{"region": {"us-east-1"}},
		},
		// --- schema without properties ---
		{
			name:          "schema without properties is returned unchanged",
			schema:        noPropsSchema,
			params:        map[string]any{"namespace": "x"},
			wantUnchanged: true,
		},
		// --- Error cases ---
		{
			name:        "empty slice is a config error",
			schema:      baseSchema,
			params:      map[string]any{"namespace": []any{}},
			wantErr:     true,
			errContains: "empty value list",
		},
		{
			name:        "mixed-type slice int and string is a config error",
			schema:      baseSchema,
			params:      map[string]any{"namespace": []any{int(1), "two"}},
			wantErr:     true,
			errContains: "mixed JSON kinds",
		},
		{
			name: "value kind different from schema type is a config error",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"namespace": {"type": "string"}
				}
			}`),
			params:      map[string]any{"namespace": []any{int(22), int(80)}},
			wantErr:     true,
			errContains: "incompatible with schema type",
		},
		{
			name: "type-union non-member is a config error",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"count": {"type": ["integer", "null"]}
				}
			}`),
			params:      map[string]any{"count": "not-a-number"},
			wantErr:     true,
			errContains: "incompatible with schema type",
		},
		{
			name:        "nested map param value is a config error",
			schema:      baseSchema,
			params:      map[string]any{"namespace": map[string]any{"foo": "bar"}},
			wantErr:     true,
			errContains: "nested value scoping not supported",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NarrowSchema(tc.schema, tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NarrowSchema returned nil, want error containing %q", tc.errContains)
				}
				if tc.errContains != "" && !containsStr(err.Error(), tc.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errContains)
				}
				return
			}
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

			// Per-property enum assertions for all cases where wantEnumFor is set.
			for propKey, wantEnum := range tc.wantEnumFor {
				assertPropertyEnum(t, got, propKey, wantEnum)
			}

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

// TestNarrowSchema_DescriptionPreservedMinLengthDropped explicitly verifies the
// "drop extra constraints, keep description" behavior (ADR-017).
func TestNarrowSchema_DescriptionPreservedMinLengthDropped(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"namespace": {
				"type": "string",
				"description": "the target namespace",
				"minLength": 1,
				"pattern": "^[a-z]"
			}
		}
	}`)

	got, err := NarrowSchema(schema, map[string]any{"namespace": []any{"worker-01", "worker-02"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertPropertyType(t, got, "namespace", "string")
	assertPropertyDescription(t, got, "namespace", "the target namespace")
	assertPropertyEnum(t, got, "namespace", []any{"worker-01", "worker-02"})
	// Extra constraints must be stripped.
	assertPropertyNoKey(t, got, "namespace", "minLength")
	assertPropertyNoKey(t, got, "namespace", "pattern")
}

// TestNarrowSchema_NilPreservesOriginal explicitly verifies that a nil param
// value means key-allowlist only: the original property arrives at the output
// without an "enum" key added.
func TestNarrowSchema_NilPreservesOriginal(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"namespace": {"type": "string", "minLength": 3}
		}
	}`)

	got, err := NarrowSchema(schema, map[string]any{"namespace": nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertSchemaProperties(t, got, []string{"namespace"})
	// Must not have an enum key — it is the original property unchanged.
	assertPropertyNoKey(t, got, "namespace", "enum")
	// Original constraints (minLength) are preserved because the whole property
	// object was copied verbatim.
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := m["properties"].(map[string]any)
	prop := props["namespace"].(map[string]any)
	if v, ok := prop["minLength"]; !ok || v == nil {
		t.Errorf("expected minLength to be preserved for nil param, got prop=%v", prop)
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

	// Schema with an enum constraint on namespace (mimics NarrowSchema output).
	enumSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"namespace": {"type": "string", "enum": ["worker-01", "worker-02"]},
			"pod":       {"type": "string"}
		}
	}`)

	noPropsSchema := json.RawMessage(`{"type": "object"}`)

	tests := []struct {
		name        string
		schema      json.RawMessage
		input       map[string]any
		wantErr     bool
		errContains string
	}{
		// --- Existing key-presence cases (unchanged behavior) ---
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
		// --- Enum membership cases ---
		{
			name:    "value in enum is accepted",
			schema:  enumSchema,
			input:   map[string]any{"namespace": "worker-01"},
			wantErr: false,
		},
		{
			name:        "value not in enum is rejected",
			schema:      enumSchema,
			input:       map[string]any{"namespace": "production"},
			wantErr:     true,
			errContains: "not permitted by the policy enum constraint",
		},
		{
			// JSON numbers: yaml.v3 parses whole numbers as Go int; json.Unmarshal
			// re-hydrates them as float64. Both marshal to the same JSON bytes, so
			// byte comparison correctly identifies them as equal.
			name: "float64 input matches integer enum entry (yaml-int round-trip safe)",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"port": {"type": "integer", "enum": [22, 80]}
				}
			}`),
			// Simulate the post-json.Unmarshal state where the int from YAML has
			// become float64 in the agent's decoded input.
			input:   map[string]any{"port": float64(22)},
			wantErr: false,
		},
		{
			name: "property without enum is not enum-checked",
			// pod has no enum; any string value is accepted.
			schema:  enumSchema,
			input:   map[string]any{"pod": "anything-goes"},
			wantErr: false,
		},
		{
			// Enum constrains the value only when it is present; omitting the key
			// is fine (required-ness is governed by the required array, not enum).
			name:    "enum property omitted from input is accepted",
			schema:  enumSchema,
			input:   map[string]any{"pod": "web-1"},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCall(tc.schema, tc.input)
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
