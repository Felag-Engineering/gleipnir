package mcp

import (
	"encoding/json"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/schemanorm"
)

func TestToolSchemaChanged(t *testing.T) {
	tests := []struct {
		name           string
		oldRaw         string
		oldCanonical   *string
		freshRaw       json.RawMessage
		freshCanonical json.RawMessage
		want           bool
	}{
		{
			name:           "both canonical present and equal",
			oldRaw:         `{"b":1,"a":2}`,
			oldCanonical:   strPtr(`{"a":2,"b":1}`),
			freshRaw:       json.RawMessage(`{"a":2,"b":1}`),
			freshCanonical: json.RawMessage(`{"a":2,"b":1}`),
			want:           false,
		},
		{
			name:           "both canonical present and different",
			oldRaw:         `{"a":1}`,
			oldCanonical:   strPtr(`{"a":1}`),
			freshRaw:       json.RawMessage(`{"a":2}`),
			freshCanonical: json.RawMessage(`{"a":2}`),
			want:           true,
		},
		{
			name:           "key-order-only raw difference with equal canonical is not drift",
			oldRaw:         `{"a":1,"b":2}`,
			oldCanonical:   strPtr(`{"a":1,"b":2}`),
			freshRaw:       json.RawMessage(`{"b":2,"a":1}`),
			freshCanonical: json.RawMessage(`{"a":1,"b":2}`),
			want:           false,
		},
		{
			name:           "old canonical nil, equal raw falls back to raw compare",
			oldRaw:         `{"a":1}`,
			oldCanonical:   nil,
			freshRaw:       json.RawMessage(`{"a":1}`),
			freshCanonical: json.RawMessage(`{"a":1}`),
			want:           false,
		},
		{
			name:           "old canonical nil, different raw falls back to raw compare",
			oldRaw:         `{"a":1}`,
			oldCanonical:   nil,
			freshRaw:       json.RawMessage(`{"a":2}`),
			freshCanonical: json.RawMessage(`{"a":2}`),
			want:           true,
		},
		{
			name:           "fresh canonical nil (normalization failed) falls back to raw compare",
			oldRaw:         `{"a":1}`,
			oldCanonical:   strPtr(`{"a":1}`),
			freshRaw:       json.RawMessage(`{"a":1}`),
			freshCanonical: nil,
			want:           false,
		},
		{
			name:           "both canonical nil falls back to raw compare, and raw differs",
			oldRaw:         `{"a":1}`,
			oldCanonical:   nil,
			freshRaw:       json.RawMessage(`{"a":2}`),
			freshCanonical: nil,
			want:           true,
		},
		{
			name:           "old canonical empty string behaves as nil",
			oldRaw:         `{"a":1,"b":2}`,
			oldCanonical:   strPtr(""),
			freshRaw:       json.RawMessage(`{"b":2,"a":1}`),
			freshCanonical: json.RawMessage(`{"a":1,"b":2}`),
			// oldCanonical == "" is treated as no-canonical, so the fallback
			// compares raw bytes, which do differ (key order swapped).
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolSchemaChanged(tt.oldRaw, tt.oldCanonical, tt.freshRaw, tt.freshCanonical)
			if got != tt.want {
				t.Errorf("toolSchemaChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanonicalizeDiscovered(t *testing.T) {
	t.Run("valid schema is normalized and never dropped", func(t *testing.T) {
		buf := captureLogger(t)

		tools := []Tool{
			{Name: "tool-a", Description: "desc", InputSchema: json.RawMessage(`{"b":1,"a":2}`)},
		}
		discovered := canonicalizeDiscovered("srv1", "test-server", tools)

		if len(discovered) != 1 {
			t.Fatalf("len(discovered) = %d, want 1 (nothing must be dropped)", len(discovered))
		}

		want, err := schemanorm.Normalize(tools[0].InputSchema)
		if err != nil {
			t.Fatalf("schemanorm.Normalize: %v", err)
		}
		if string(discovered[0].CanonicalSchema) != string(want) {
			t.Errorf("CanonicalSchema = %s, want %s", discovered[0].CanonicalSchema, want)
		}
		if ptr := discovered[0].CanonicalSchemaPtr(); ptr == nil || *ptr != string(want) {
			t.Errorf("CanonicalSchemaPtr() = %v, want %q", ptr, string(want))
		}

		if lines := decodeLogLines(t, buf); len(lines) != 0 {
			t.Errorf("expected no log lines for a valid schema, got %d: %v", len(lines), lines)
		}
	})

	t.Run("duplicate-key schema fails open with NULL canonical and one WARN", func(t *testing.T) {
		buf := captureLogger(t)

		tools := []Tool{
			{Name: "bad-tool", Description: "desc", InputSchema: json.RawMessage(`{"type":"object","type":"array"}`)},
		}
		discovered := canonicalizeDiscovered("srv1", "test-server", tools)

		if len(discovered) != 1 {
			t.Fatalf("len(discovered) = %d, want 1 (a failed tool must still be returned)", len(discovered))
		}
		if discovered[0].CanonicalSchema != nil {
			t.Errorf("CanonicalSchema = %s, want nil after normalization failure", discovered[0].CanonicalSchema)
		}
		if ptr := discovered[0].CanonicalSchemaPtr(); ptr != nil {
			t.Errorf("CanonicalSchemaPtr() = %q, want nil after normalization failure", *ptr)
		}

		lines := decodeLogLines(t, buf)
		if len(lines) != 1 {
			t.Fatalf("expected exactly 1 log line, got %d: %v", len(lines), lines)
		}
		line := lines[0]
		if line["server_name"] != "test-server" {
			t.Errorf("server_name = %v, want test-server", line["server_name"])
		}
		if line["tool_name"] != "bad-tool" {
			t.Errorf("tool_name = %v, want bad-tool", line["tool_name"])
		}
		if line["err"] == nil || line["err"] == "" {
			t.Errorf("err field missing or empty: %v", line)
		}
	})

	t.Run("empty or absent schema is NULL canonical with zero log lines", func(t *testing.T) {
		buf := captureLogger(t)

		tools := []Tool{
			{Name: "no-schema", Description: "desc", InputSchema: nil},
			{Name: "blank-schema", Description: "desc", InputSchema: json.RawMessage("   ")},
		}
		discovered := canonicalizeDiscovered("srv1", "test-server", tools)

		if len(discovered) != 2 {
			t.Fatalf("len(discovered) = %d, want 2", len(discovered))
		}
		for i, d := range discovered {
			if d.CanonicalSchema != nil {
				t.Errorf("discovered[%d].CanonicalSchema = %s, want nil", i, d.CanonicalSchema)
			}
		}
		if lines := decodeLogLines(t, buf); len(lines) != 0 {
			t.Errorf("expected zero log lines for absent/empty schemas, got %d: %v", len(lines), lines)
		}
	})
}
