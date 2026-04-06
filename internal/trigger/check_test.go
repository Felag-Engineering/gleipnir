package trigger

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/rapp992/gleipnir/internal/model"
)

// mcpText builds the JSON bytes for an MCP content array containing one text item.
// This is the format that mcp.Client.CallTool returns as ToolResult.Output.
func mcpText(text string) json.RawMessage {
	b, _ := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	return b
}

// TestExtractTextContent checks all extractTextContent edge cases.
func TestExtractTextContent(t *testing.T) {
	cases := []struct {
		name     string
		input    json.RawMessage
		wantText string
		wantOK   bool
	}{
		{
			name:     "single text item",
			input:    mcpText(`{"status":"ok"}`),
			wantText: `{"status":"ok"}`,
			wantOK:   true,
		},
		{
			name:     "multiple items, first text extracted",
			input:    json.RawMessage(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`),
			wantText: "first",
			wantOK:   true,
		},
		{
			name:   "empty content array",
			input:  json.RawMessage(`[]`),
			wantOK: false,
		},
		{
			name:   "content array with only non-text type",
			input:  json.RawMessage(`[{"type":"image","data":"..."}]`),
			wantOK: false,
		},
		{
			name:   "text item with empty text string",
			input:  json.RawMessage(`[{"type":"text","text":""}]`),
			wantOK: false,
		},
		{
			name:   "non-array JSON",
			input:  json.RawMessage(`{"type":"text","text":"hello"}`),
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, ok := extractTextContent(tc.input)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
		})
	}
}

// TestEvaluateCheck tests all comparators, path evaluation, and edge cases.
// The output parameter is in full MCP content array format, mirroring what
// mcp.Client.CallTool returns.
func TestEvaluateCheck(t *testing.T) {
	cases := []struct {
		name   string
		output json.RawMessage
		check  model.PollCheck
		want   bool
	}{
		// --- equals ---
		{
			name:   "equals string match",
			output: mcpText(`{"status":"degraded"}`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorEquals, Value: "degraded"},
			want:   true,
		},
		{
			name:   "equals string mismatch",
			output: mcpText(`{"status":"healthy"}`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorEquals, Value: "degraded"},
			want:   false,
		},
		{
			name:   "equals number match",
			output: mcpText(`{"count":42}`),
			check:  model.PollCheck{Path: "$.count", Comparator: model.ComparatorEquals, Value: float64(42)},
			want:   true,
		},
		{
			name:   "equals bool match",
			output: mcpText(`{"active":true}`),
			check:  model.PollCheck{Path: "$.active", Comparator: model.ComparatorEquals, Value: true},
			want:   true,
		},
		{
			name:   "equals bool mismatch",
			output: mcpText(`{"active":false}`),
			check:  model.PollCheck{Path: "$.active", Comparator: model.ComparatorEquals, Value: true},
			want:   false,
		},

		// --- not_equals ---
		{
			name:   "not_equals string mismatch returns true",
			output: mcpText(`{"status":"healthy"}`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorNotEquals, Value: "degraded"},
			want:   true,
		},
		{
			name:   "not_equals string match returns false",
			output: mcpText(`{"status":"degraded"}`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorNotEquals, Value: "degraded"},
			want:   false,
		},

		// --- greater_than ---
		{
			name:   "greater_than number above threshold",
			output: mcpText(`{"count":100}`),
			check:  model.PollCheck{Path: "$.count", Comparator: model.ComparatorGreaterThan, Value: float64(50)},
			want:   true,
		},
		{
			name:   "greater_than number below threshold",
			output: mcpText(`{"count":10}`),
			check:  model.PollCheck{Path: "$.count", Comparator: model.ComparatorGreaterThan, Value: float64(50)},
			want:   false,
		},
		{
			name:   "greater_than with string value returns false",
			output: mcpText(`{"status":"degraded"}`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorGreaterThan, Value: float64(50)},
			want:   false,
		},

		// --- less_than ---
		{
			name:   "less_than number below threshold",
			output: mcpText(`{"count":5}`),
			check:  model.PollCheck{Path: "$.count", Comparator: model.ComparatorLessThan, Value: float64(10)},
			want:   true,
		},
		{
			name:   "less_than number above threshold",
			output: mcpText(`{"count":20}`),
			check:  model.PollCheck{Path: "$.count", Comparator: model.ComparatorLessThan, Value: float64(10)},
			want:   false,
		},

		// --- contains ---
		{
			name:   "contains substring match",
			output: mcpText(`{"message":"service is degraded now"}`),
			check:  model.PollCheck{Path: "$.message", Comparator: model.ComparatorContains, Value: "degraded"},
			want:   true,
		},
		{
			name:   "contains no substring match",
			output: mcpText(`{"message":"all systems nominal"}`),
			check:  model.PollCheck{Path: "$.message", Comparator: model.ComparatorContains, Value: "degraded"},
			want:   false,
		},
		{
			name:   "contains with non-string extracted value",
			output: mcpText(`{"count":42}`),
			check:  model.PollCheck{Path: "$.count", Comparator: model.ComparatorContains, Value: "4"},
			want:   false,
		},

		// --- JSONPath edge cases ---
		{
			name:   "path extracts nothing",
			output: mcpText(`{"other":"field"}`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorEquals, Value: "degraded"},
			want:   false,
		},
		{
			name:   "nested path",
			output: mcpText(`{"data":{"status":"ok"}}`),
			check:  model.PollCheck{Path: "$.data.status", Comparator: model.ComparatorEquals, Value: "ok"},
			want:   true,
		},
		{
			name:   "array index path",
			output: mcpText(`{"items":[{"name":"alpha"},{"name":"beta"}]}`),
			check:  model.PollCheck{Path: "$.items[0].name", Comparator: model.ComparatorEquals, Value: "alpha"},
			want:   true,
		},

		// --- content format edge cases ---
		{
			name:   "text content is not valid JSON",
			output: mcpText(`not json at all`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorEquals, Value: "ok"},
			want:   false,
		},
		{
			name:   "no text content in MCP array",
			output: json.RawMessage(`[{"type":"image","data":"abc"}]`),
			check:  model.PollCheck{Path: "$.status", Comparator: model.ComparatorEquals, Value: "ok"},
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateCheck(tc.output, tc.check)
			if got != tc.want {
				t.Errorf("evaluateCheck() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEvaluateChecks verifies match mode logic and error handling.
func TestEvaluateChecks(t *testing.T) {
	passingCheck := model.PollCheck{
		Tool: "s.t", Path: "$.status", Comparator: model.ComparatorEquals, Value: "degraded",
	}
	failingCheck := model.PollCheck{
		Tool: "s.t", Path: "$.status", Comparator: model.ComparatorEquals, Value: "critical",
	}
	passingOutput := checkResult{Output: mcpText(`{"status":"degraded"}`)}
	failingOutput := checkResult{Output: mcpText(`{"status":"degraded"}`)} // doesn't match "critical"
	errorOutput := checkResult{Err: errors.New("tool call failed")}

	cases := []struct {
		name    string
		results []checkResult
		checks  []model.PollCheck
		match   model.MatchMode
		want    bool
	}{
		{
			name:    "match=all, all pass",
			results: []checkResult{passingOutput, passingOutput},
			checks:  []model.PollCheck{passingCheck, passingCheck},
			match:   model.MatchAll,
			want:    true,
		},
		{
			name:    "match=all, one fails",
			results: []checkResult{passingOutput, failingOutput},
			checks:  []model.PollCheck{passingCheck, failingCheck},
			match:   model.MatchAll,
			want:    false,
		},
		{
			name:    "match=any, one passes",
			results: []checkResult{passingOutput, failingOutput},
			checks:  []model.PollCheck{passingCheck, failingCheck},
			match:   model.MatchAny,
			want:    true,
		},
		{
			name:    "match=any, none pass",
			results: []checkResult{failingOutput, failingOutput},
			checks:  []model.PollCheck{failingCheck, failingCheck},
			match:   model.MatchAny,
			want:    false,
		},
		{
			name:    "error result treated as not-passed (match=all)",
			results: []checkResult{errorOutput, passingOutput},
			checks:  []model.PollCheck{passingCheck, passingCheck},
			match:   model.MatchAll,
			want:    false,
		},
		{
			name:    "error result treated as not-passed (match=any, other passes)",
			results: []checkResult{errorOutput, passingOutput},
			checks:  []model.PollCheck{passingCheck, passingCheck},
			match:   model.MatchAny,
			want:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateChecks(tc.results, tc.checks, tc.match)
			if got != tc.want {
				t.Errorf("evaluateChecks() = %v, want %v", got, tc.want)
			}
		})
	}
}
