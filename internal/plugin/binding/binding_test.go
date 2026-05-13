package binding_test

import (
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
