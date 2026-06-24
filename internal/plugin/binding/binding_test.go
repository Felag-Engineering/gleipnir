package binding_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/binding"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"gopkg.in/yaml.v3"
)

// schemaYAML parses the YAML string and returns the root mapping node,
// matching the shape of EventKindDecl.BindingSchema in practice.
func schemaYAML(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("schemaYAML: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	return &doc
}

// schemaMentionOnly is a minimal binding_schema with mention_only as a boolean.
const schemaMentionOnly = `
type: object
properties:
  mention_only:
    type: boolean
`

// schemaBasic is a binding_schema with one field of each v1 operator type.
const schemaBasic = `
type: object
properties:
  channel:
    type: string
  keyword:
    type: string
    format: contains
  pattern:
    type: string
    format: regex
  mention_only:
    type: boolean
  count:
    type: number
  active:
    type: boolean
`

// schemaNumeric has an integer field (channel_id) and a fractional number
// field (ratio) for the precision tests (#586).
const schemaNumeric = `
type: object
properties:
  channel_id:
    type: integer
  ratio:
    type: number
`

// decodeJSON unmarshals a JSON string into map[string]any with UseNumber, so
// numeric values arrive as json.Number — mirroring the production dispatcher
// decode path after the #586 fix.
func decodeJSON(t *testing.T, src string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(src)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
	return m
}

// schemaGlob has a field with format:glob (unsupported in v1).
const schemaGlob = `
type: object
properties:
  path:
    type: string
    format: glob
`

// ---- end-to-end fixture for SDK → evaluator handshake ----

// e2eFilter is a filter struct used in the end-to-end ReflectSchema test.
// It exercises all four v1 typed primitives plus the reserved mention_only field.
type e2eFilter struct {
	Channel     manifest.EqualsField   `json:"channel"`
	Keyword     manifest.ContainsField `json:"keyword,omitempty"`
	Pattern     manifest.RegexField    `json:"pattern,omitempty"`
	MentionOnly bool                   `json:"mention_only,omitempty"`
}

// TestCompileEvaluate_EqualsString verifies OpEquals for a plain string field.
func TestCompileEvaluate_EqualsString(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{"channel": "#incidents"}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, err := cb.Evaluate(map[string]any{"channel": "#incidents"})
	if err != nil || !match {
		t.Errorf("expected match: got (%v, %v)", match, err)
	}
	noMatch, err := cb.Evaluate(map[string]any{"channel": "#general"})
	if err != nil || noMatch {
		t.Errorf("expected no match: got (%v, %v)", noMatch, err)
	}
}

// TestCompileEvaluate_EqualsBool verifies OpEquals for a boolean field.
func TestCompileEvaluate_EqualsBool(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{"active": true}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, _ := cb.Evaluate(map[string]any{"active": true})
	if !match {
		t.Error("expected match for active=true")
	}
	noMatch, _ := cb.Evaluate(map[string]any{"active": false})
	if noMatch {
		t.Error("expected no match for active=false")
	}
}

// TestCompileEvaluate_EqualsNumber verifies OpEquals for a numeric field.
func TestCompileEvaluate_EqualsNumber(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{"count": float64(42)}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, _ := cb.Evaluate(map[string]any{"count": float64(42)})
	if !match {
		t.Error("expected match for count=42")
	}
	noMatch, _ := cb.Evaluate(map[string]any{"count": float64(7)})
	if noMatch {
		t.Error("expected no match for count=7")
	}
}

// TestCompileEvaluate_Contains verifies OpContains.
func TestCompileEvaluate_Contains(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{"keyword": "alert"}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, _ := cb.Evaluate(map[string]any{"keyword": "disk alert triggered"})
	if !match {
		t.Error("expected match: 'disk alert triggered' contains 'alert'")
	}
	noMatch, _ := cb.Evaluate(map[string]any{"keyword": "everything is fine"})
	if noMatch {
		t.Error("expected no match: 'everything is fine' does not contain 'alert'")
	}
}

// TestCompileEvaluate_Regex verifies OpRegex.
func TestCompileEvaluate_Regex(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{"pattern": `^CRIT:`}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, _ := cb.Evaluate(map[string]any{"pattern": "CRIT: disk full"})
	if !match {
		t.Error("expected match for CRIT: prefix")
	}
	noMatch, _ := cb.Evaluate(map[string]any{"pattern": "INFO: all good"})
	if noMatch {
		t.Error("expected no match for INFO prefix")
	}
}

// TestCompileEvaluate_MentionOnlyTrue verifies OpMentionOnly when binding is true.
func TestCompileEvaluate_MentionOnlyTrue(t *testing.T) {
	schema := schemaYAML(t, schemaMentionOnly)
	cb, err := binding.Compile(map[string]any{"mention_only": true}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, _ := cb.Evaluate(map[string]any{"mentioned": true})
	if !match {
		t.Error("expected match when payload.mentioned=true")
	}
	noMatch, _ := cb.Evaluate(map[string]any{"mentioned": false})
	if noMatch {
		t.Error("expected no match when payload.mentioned=false")
	}
	noField, _ := cb.Evaluate(map[string]any{})
	if noField {
		t.Error("expected no match when payload.mentioned absent")
	}
}

// TestCompileEvaluate_MentionOnlyFalse verifies that mention_only=false is a no-op.
func TestCompileEvaluate_MentionOnlyFalse(t *testing.T) {
	schema := schemaYAML(t, schemaMentionOnly)
	cb, err := binding.Compile(map[string]any{"mention_only": false}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// mention_only=false should match regardless of the mentioned field.
	match, _ := cb.Evaluate(map[string]any{})
	if !match {
		t.Error("expected match: mention_only=false is a no-op")
	}
	match2, _ := cb.Evaluate(map[string]any{"mentioned": false})
	if !match2 {
		t.Error("expected match: mention_only=false is a no-op even with mentioned=false")
	}
}

// TestCompileEvaluate_MultiFieldAND verifies implicit AND across multiple fields.
func TestCompileEvaluate_MultiFieldAND(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{
		"channel": "#incidents",
		"keyword": "alert",
	}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Both conditions met.
	match, _ := cb.Evaluate(map[string]any{
		"channel": "#incidents",
		"keyword": "disk alert",
	})
	if !match {
		t.Error("expected match when all fields match")
	}

	// Only channel matches.
	noMatch, _ := cb.Evaluate(map[string]any{
		"channel": "#incidents",
		"keyword": "all clear",
	})
	if noMatch {
		t.Error("expected no match when keyword does not match")
	}

	// Only keyword matches.
	noMatch2, _ := cb.Evaluate(map[string]any{
		"channel": "#general",
		"keyword": "disk alert",
	})
	if noMatch2 {
		t.Error("expected no match when channel does not match")
	}
}

// TestCompileEvaluate_EmptyBinding verifies that an empty binding matches everything.
func TestCompileEvaluate_EmptyBinding(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, _ := cb.Evaluate(map[string]any{"channel": "anything"})
	if !match {
		t.Error("expected empty binding to match everything")
	}
	matchEmpty, _ := cb.Evaluate(map[string]any{})
	if !matchEmpty {
		t.Error("expected empty binding to match empty payload")
	}
}

// TestCompileEvaluate_MissingPayloadField verifies silent false on absent field.
func TestCompileEvaluate_MissingPayloadField(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{"channel": "#incidents"}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	match, _ := cb.Evaluate(map[string]any{})
	if match {
		t.Error("expected false when payload field is missing")
	}
}

// TestCompileEvaluate_PayloadTypeMismatch verifies silent false on type mismatch.
func TestCompileEvaluate_PayloadTypeMismatch(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	cb, err := binding.Compile(map[string]any{"channel": "#incidents"}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// payload supplies a number where a string is expected.
	match, _ := cb.Evaluate(map[string]any{"channel": 42})
	if match {
		t.Error("expected false on payload type mismatch (number for string)")
	}
}

// TestCompile_ErrInvalidRegex verifies that a bad regex pattern is rejected.
func TestCompile_ErrInvalidRegex(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	_, err := binding.Compile(map[string]any{"pattern": `[invalid`}, schema)
	if !errors.Is(err, binding.ErrInvalidRegex) {
		t.Errorf("expected ErrInvalidRegex, got %v", err)
	}
}

// TestCompile_ErrInvalidRegex_GoVsECMA documents the Go RE2 constraint.
// The pattern (?<=foo)bar is valid ECMA but invalid Go RE2 (no lookbehind).
// Plugin authors must use RE2-compatible patterns.
func TestCompile_ErrInvalidRegex_GoVsECMA(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	_, err := binding.Compile(map[string]any{"pattern": `(?<=foo)bar`}, schema)
	if !errors.Is(err, binding.ErrInvalidRegex) {
		t.Errorf("expected ErrInvalidRegex for ECMA lookbehind, got %v", err)
	}
}

// TestCompile_ErrUnknownField verifies that a binding key absent from the schema is rejected.
func TestCompile_ErrUnknownField(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	_, err := binding.Compile(map[string]any{"nonexistent": "value"}, schema)
	if !errors.Is(err, binding.ErrUnknownField) {
		t.Errorf("expected ErrUnknownField, got %v", err)
	}
}

// TestCompile_ErrUnsupportedOperator verifies that format:glob is rejected.
func TestCompile_ErrUnsupportedOperator(t *testing.T) {
	schema := schemaYAML(t, schemaGlob)
	_, err := binding.Compile(map[string]any{"path": "/var/*"}, schema)
	if !errors.Is(err, binding.ErrUnsupportedOperator) {
		t.Errorf("expected ErrUnsupportedOperator for glob, got %v", err)
	}
}

// TestCompile_ErrValueTypeMismatch verifies that supplying a bool for a string field is rejected.
func TestCompile_ErrValueTypeMismatch(t *testing.T) {
	schema := schemaYAML(t, schemaBasic)
	_, err := binding.Compile(map[string]any{"channel": true}, schema)
	if err == nil {
		t.Error("expected error for bool value against string field")
	}
}

// TestCompile_NilSchema_EmptyBinding verifies that nil schema + empty binding → empty CompiledBinding.
func TestCompile_NilSchema_EmptyBinding(t *testing.T) {
	cb, err := binding.Compile(map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Compile(empty, nil): unexpected error: %v", err)
	}
	// Empty binding matches everything.
	match, _ := cb.Evaluate(map[string]any{"anything": "value"})
	if !match {
		t.Error("expected empty CompiledBinding to match everything")
	}
}

// TestCompile_NilSchema_NonEmptyBinding verifies that nil schema + non-empty binding → ErrUnknownField.
func TestCompile_NilSchema_NonEmptyBinding(t *testing.T) {
	_, err := binding.Compile(map[string]any{"k": "v"}, nil)
	if !errors.Is(err, binding.ErrUnknownField) {
		t.Errorf("expected ErrUnknownField for non-empty binding with nil schema, got %v", err)
	}
}

// TestEndToEnd_SDKToEvaluator performs an end-to-end handshake:
// reflect a filter struct via manifest.ReflectSchema, then Compile and
// Evaluate against synthetic payloads. This verifies that the SDK's
// emitted schema nodes are parseable by the binding evaluator.
func TestEndToEnd_SDKToEvaluator(t *testing.T) {
	node, err := manifest.ReflectSchema(e2eFilter{})
	if err != nil {
		t.Fatalf("manifest.ReflectSchema: %v", err)
	}

	tests := []struct {
		name    string
		binding map[string]any
		payload map[string]any
		want    bool
	}{
		{
			name:    "equals channel match",
			binding: map[string]any{"channel": "#incidents"},
			payload: map[string]any{"channel": "#incidents"},
			want:    true,
		},
		{
			name:    "equals channel no match",
			binding: map[string]any{"channel": "#incidents"},
			payload: map[string]any{"channel": "#general"},
			want:    false,
		},
		{
			name:    "contains keyword match",
			binding: map[string]any{"keyword": "alert"},
			payload: map[string]any{"keyword": "disk alert critical"},
			want:    true,
		},
		{
			name:    "contains keyword no match",
			binding: map[string]any{"keyword": "alert"},
			payload: map[string]any{"keyword": "all systems nominal"},
			want:    false,
		},
		{
			name:    "regex pattern match",
			binding: map[string]any{"pattern": `^ERROR`},
			payload: map[string]any{"pattern": "ERROR: disk full"},
			want:    true,
		},
		{
			name:    "regex pattern no match",
			binding: map[string]any{"pattern": `^ERROR`},
			payload: map[string]any{"pattern": "INFO: disk ok"},
			want:    false,
		},
		{
			name:    "mention_only true with mentioned=true",
			binding: map[string]any{"mention_only": true},
			payload: map[string]any{"mentioned": true},
			want:    true,
		},
		{
			name:    "mention_only true without mentioned",
			binding: map[string]any{"mention_only": true},
			payload: map[string]any{},
			want:    false,
		},
		{
			name:    "multi-field AND both match",
			binding: map[string]any{"channel": "#incidents", "keyword": "crit"},
			payload: map[string]any{"channel": "#incidents", "keyword": "disk crit alert"},
			want:    true,
		},
		{
			name:    "multi-field AND one fails",
			binding: map[string]any{"channel": "#incidents", "keyword": "crit"},
			payload: map[string]any{"channel": "#general", "keyword": "disk crit alert"},
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cb, err := binding.Compile(tc.binding, node)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			got, err := cb.Evaluate(tc.payload)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Evaluate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCompileEvaluate_LargeIntPrecision is the regression suite for #586:
// integer binding equality must not lose precision through float64 coercion.
// The binding value comes from YAML (int / int64), and the payload value is
// exercised across every concrete type the decode paths can produce
// (json.Number, float64, int, int64, string).
func TestCompileEvaluate_LargeIntPrecision(t *testing.T) {
	schema := schemaYAML(t, schemaNumeric)

	// Two distinct 64-bit IDs that are EQUAL as float64 (both round to the same
	// value above 2^53) but UNEQUAL as integers. This is the exact failure mode
	// from the issue.
	const idA = int64(9007199254740993) // 2^53 + 1
	const idB = int64(9007199254740992) // 2^53 (== idA-1; equal to idA in float64)

	tests := []struct {
		name        string
		bindingVal  any
		payloadVal  any
		want        bool
		description string
	}{
		{
			name:       "json.Number large id equal",
			bindingVal: idA,
			payloadVal: json.Number("9007199254740993"),
			want:       true,
		},
		{
			name:       "json.Number large id unequal but float-equal",
			bindingVal: idA,
			payloadVal: json.Number("9007199254740992"),
			want:       false, // would be true under the old float64 coercion bug
		},
		{
			name:       "int64 large id unequal but float-equal",
			bindingVal: idA,
			payloadVal: idB,
			want:       false,
		},
		{
			// float64(idB) is integral and within int64 range, so it is treated
			// as the exact integer idB and compared exactly — it does NOT match
			// the idA binding. (Note: float64(idA) would equal float64(idB) at
			// the bit level, so if the payload were already lossily decoded the
			// distinction is gone before we see it. This case proves we don't
			// re-introduce float coercion for an integral float64 input.)
			name:       "integral float64 payload compared exactly as int",
			bindingVal: idA,
			payloadVal: float64(idB),
			want:       false,
		},
		{
			name:       "small int still matches (int payload)",
			bindingVal: int64(42),
			payloadVal: 42,
			want:       true,
		},
		{
			name:       "small int no match (int payload)",
			bindingVal: int64(42),
			payloadVal: 7,
			want:       false,
		},
		{
			name:       "small int matches float64 payload",
			bindingVal: int64(42),
			payloadVal: float64(42),
			want:       true,
		},
		{
			name:       "negative large id equal (json.Number)",
			bindingVal: int64(-9007199254740993),
			payloadVal: json.Number("-9007199254740993"),
			want:       true,
		},
		{
			name:       "negative large id unequal but float-equal",
			bindingVal: int64(-9007199254740993),
			payloadVal: json.Number("-9007199254740992"),
			want:       false,
		},
		{
			name:       "string-encoded id matches integer binding",
			bindingVal: idA,
			payloadVal: "9007199254740993",
			want:       true,
		},
		{
			name:       "string-encoded id no match",
			bindingVal: idA,
			payloadVal: "9007199254740992",
			want:       false,
		},
		{
			name:       "non-numeric string is no match (not an error)",
			bindingVal: int64(42),
			payloadVal: "not-a-number",
			want:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cb, err := binding.Compile(map[string]any{"channel_id": tc.bindingVal}, schema)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			got, err := cb.Evaluate(map[string]any{"channel_id": tc.payloadVal})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Evaluate(channel_id=%v vs %v) = %v, want %v", tc.bindingVal, tc.payloadVal, got, tc.want)
			}
		})
	}
}

// TestCompileEvaluate_FractionalNumber verifies that genuinely fractional
// fields are still compared as floats and are not broken by the integer
// fast-path (#586).
func TestCompileEvaluate_FractionalNumber(t *testing.T) {
	schema := schemaYAML(t, schemaNumeric)

	cb, err := binding.Compile(map[string]any{"ratio": 0.5}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	tests := []struct {
		name    string
		payload any
		want    bool
	}{
		{"float64 equal", float64(0.5), true},
		{"json.Number equal", json.Number("0.5"), true},
		{"string equal", "0.5", true},
		{"float64 unequal", float64(0.25), false},
		{"integer payload unequal", 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := cb.Evaluate(map[string]any{"ratio": tc.payload})
			if got != tc.want {
				t.Errorf("Evaluate(ratio=0.5 vs %v) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}

// TestCompileEvaluate_WholeNumberBindingFromYAML verifies that a binding value
// declared as a fractional number with a whole-number value (e.g. ratio: 2)
// still compares exactly against integer payloads.
func TestCompileEvaluate_WholeNumberBindingFromYAML(t *testing.T) {
	schema := schemaYAML(t, schemaNumeric)
	cb, err := binding.Compile(map[string]any{"ratio": 2}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if match, _ := cb.Evaluate(map[string]any{"ratio": json.Number("2")}); !match {
		t.Error("expected ratio=2 to match json.Number(\"2\")")
	}
	if match, _ := cb.Evaluate(map[string]any{"ratio": float64(2)}); !match {
		t.Error("expected ratio=2 to match float64(2)")
	}
}

// TestCompileEvaluate_FullDecodePath exercises the precision fix end-to-end
// through the same json.Decoder(UseNumber) decode the production dispatcher
// uses, confirming a 64-bit ID survives JSON decode without precision loss.
func TestCompileEvaluate_FullDecodePath(t *testing.T) {
	schema := schemaYAML(t, schemaNumeric)
	cb, err := binding.Compile(map[string]any{"channel_id": int64(9007199254740993)}, schema)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	matchPayload := decodeJSON(t, `{"channel_id": 9007199254740993}`)
	if match, _ := cb.Evaluate(matchPayload); !match {
		t.Error("expected match for channel_id=9007199254740993 decoded via UseNumber")
	}

	// The float-equal neighbour must NOT match after the fix.
	noMatchPayload := decodeJSON(t, `{"channel_id": 9007199254740992}`)
	if match, _ := cb.Evaluate(noMatchPayload); match {
		t.Error("expected NO match for the float-equal neighbour 9007199254740992 (precision regression)")
	}
}
