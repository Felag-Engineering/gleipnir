package policy

import (
	"encoding/json"
	"testing"
)

func TestValidateParamsScope(t *testing.T) {
	const toolRef = "github.list_repos"

	tests := []struct {
		name       string
		toolIndex  int
		params     map[string]any
		canonical  json.RawMessage
		wantIssues []Issue
	}{
		{
			name:       "empty params + nil canonical -> no issues",
			params:     nil,
			canonical:  nil,
			wantIssues: nil,
		},
		{
			name:       "empty params + oneOf canonical -> no issues",
			params:     nil,
			canonical:  json.RawMessage(`{"oneOf":[{"properties":{"a":{}}}]}`),
			wantIssues: nil,
		},
		{
			name:      "params + nil canonical -> no-canonical issue",
			params:    map[string]any{"a": 1},
			canonical: nil,
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" has no stored canonical schema — schema could not be canonicalized; parameter scoping unavailable for this tool (refresh the MCP server's tools, then save again)`,
			}},
		},
		{
			name:      "params + empty canonical -> no-canonical issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(""),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" has no stored canonical schema — schema could not be canonicalized; parameter scoping unavailable for this tool (refresh the MCP server's tools, then save again)`,
			}},
		},
		{
			name:      "params + whitespace-only canonical -> no-canonical issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage("  "),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" has no stored canonical schema — schema could not be canonicalized; parameter scoping unavailable for this tool (refresh the MCP server's tools, then save again)`,
			}},
		},
		{
			name:      "canonical is a JSON boolean -> not-object issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`true`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" canonical schema is not a JSON object; parameter scoping unavailable for this tool`,
			}},
		},
		{
			name:      "canonical is a JSON array -> not-object issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`[1,2]`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" canonical schema is not a JSON object; parameter scoping unavailable for this tool`,
			}},
		},
		{
			name:      "canonical is unparseable JSON -> not-object issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" canonical schema is not a JSON object; parameter scoping unavailable for this tool`,
			}},
		},
		{
			name:      "canonical has no properties key -> no-properties issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"type":"object"}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" declares no top-level properties; parameter scoping unavailable for this tool`,
			}},
		},
		{
			name:      "canonical properties is not an object -> no-properties issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"properties":[]}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params",
				Message: `tool "github.list_repos" declares no top-level properties; parameter scoping unavailable for this tool`,
			}},
		},
		{
			name:      "root oneOf, single param key -> branch issue",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"oneOf":[{"properties":{"a":{}}},{"properties":{"b":{}}}]}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "oneOf"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "root anyOf -> branch issue naming anyOf",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"anyOf":[{"properties":{"a":{}}}]}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "anyOf"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "root allOf -> branch issue naming allOf",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"allOf":[{"properties":{"a":{}}}]}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "allOf"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "root $ref -> branch issue naming $ref",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"$ref":"#/definitions/foo"}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "$ref"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "root not -> branch issue naming not",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"not":{"properties":{"a":{}}}}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "not"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "root if -> branch issue naming if",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"if":{"properties":{"a":{}}}}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "if"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "root has both properties and oneOf -> still rejected as branching (decision b)",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"properties":{"a":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "oneOf"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "root has both $ref and oneOf -> deterministic ordering reports $ref first",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"$ref":"#/definitions/foo","oneOf":[{"properties":{"a":{}}}]}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.a",
				Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "$ref"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
			}},
		},
		{
			name:      "branch root with two params keys -> exactly 2 issues, sorted a then b",
			params:    map[string]any{"b": 1, "a": 1},
			canonical: json.RawMessage(`{"oneOf":[{"properties":{"a":{}}},{"properties":{"b":{}}}]}`),
			wantIssues: []Issue{
				{
					Field:   "capabilities.tools[0].params.a",
					Message: `cannot scope "a" — tool "github.list_repos" declares a top-level "oneOf"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
				},
				{
					Field:   "capabilities.tools[0].params.b",
					Message: `cannot scope "b" — tool "github.list_repos" declares a top-level "oneOf"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`,
				},
			},
		},
		{
			name:       "plain top-level key -> no issues",
			params:     map[string]any{"a": 1},
			canonical:  json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`),
			wantIssues: nil,
		},
		{
			name:      "one plain key + one unknown key -> exactly 1 issue for the unknown key",
			params:    map[string]any{"a": 1, "foo": 1},
			canonical: json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[0].params.foo",
				Message: `"foo" is not a top-level property of tool "github.list_repos"`,
			}},
		},
		{
			name:      "two unknown keys -> ordered foo then z",
			params:    map[string]any{"z": 1, "foo": 1},
			canonical: json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`),
			wantIssues: []Issue{
				{
					Field:   "capabilities.tools[0].params.foo",
					Message: `"foo" is not a top-level property of tool "github.list_repos"`,
				},
				{
					Field:   "capabilities.tools[0].params.z",
					Message: `"z" is not a top-level property of tool "github.list_repos"`,
				},
			},
		},
		{
			name:      "toolIndex=2 unknown key -> Field carries the index, Message is byte-identical to toolIndex=0",
			toolIndex: 2,
			params:    map[string]any{"foo": 1},
			canonical: json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`),
			wantIssues: []Issue{{
				Field:   "capabilities.tools[2].params.foo",
				Message: `"foo" is not a top-level property of tool "github.list_repos"`,
			}},
		},
		{
			name:   "patternProperties/dependentSchemas/additionalProperties do not gate a declared property",
			params: map[string]any{"a": 1},
			canonical: json.RawMessage(`{
				"type": "object",
				"properties": {"a": {}},
				"patternProperties": {"^x-": {}},
				"dependentSchemas": {"a": {"required": ["a"]}},
				"additionalProperties": false
			}`),
			wantIssues: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateParamsScope(tc.toolIndex, toolRef, tc.params, tc.canonical)
			if len(got) != len(tc.wantIssues) {
				t.Fatalf("got %d issues, want %d: got=%v want=%v", len(got), len(tc.wantIssues), got, tc.wantIssues)
			}
			for i, wantIssue := range tc.wantIssues {
				if got[i].Field != wantIssue.Field {
					t.Errorf("issue[%d].Field = %q, want %q", i, got[i].Field, wantIssue.Field)
				}
				if got[i].Message != wantIssue.Message {
					t.Errorf("issue[%d].Message = %q, want %q", i, got[i].Message, wantIssue.Message)
				}
			}
		})
	}
}

// TestValidateParamsScope_IndexNeverLeaksIntoMessage locks the toolIndex==0
// vs toolIndex==2 unknown-key case explicitly: the Field carries the index,
// but the Message must be byte-identical either way. A regression that
// reintroduced a field-path prefix into Message would pass the table test
// above (which compares Message directly per-row) only if it also happened
// to match — this test makes the "same Message, different Field" invariant
// impossible to miss.
func TestValidateParamsScope_IndexNeverLeaksIntoMessage(t *testing.T) {
	canonical := json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`)
	params := map[string]any{"foo": 1}

	at0 := validateParamsScope(0, "github.list_repos", params, canonical)
	at2 := validateParamsScope(2, "github.list_repos", params, canonical)

	if len(at0) != 1 || len(at2) != 1 {
		t.Fatalf("expected exactly 1 issue each, got %d and %d", len(at0), len(at2))
	}
	if at0[0].Field == at2[0].Field {
		t.Errorf("expected different Field per toolIndex, both were %q", at0[0].Field)
	}
	if at0[0].Message != at2[0].Message {
		t.Errorf("expected identical Message across toolIndex, got %q vs %q", at0[0].Message, at2[0].Message)
	}
}

// TestValidateParamsScope_ComposedRenderDoesNotDuplicatePath is the
// regression gate for the duplicated-prefix defect the plan review caught:
// ValidationError.Error() must render the field path exactly once.
func TestValidateParamsScope_ComposedRenderDoesNotDuplicatePath(t *testing.T) {
	canonical := json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`)
	issues := validateParamsScope(0, "github.list_repos", map[string]any{"foo": 1}, canonical)

	ve := &ValidationError{Errors: issues}
	want := `policy validation failed: capabilities.tools[0].params.foo: "foo" is not a top-level property of tool "github.list_repos"`
	if ve.Error() != want {
		t.Errorf("Error() = %q, want %q", ve.Error(), want)
	}
}
