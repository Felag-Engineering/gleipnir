package openai

import (
	"strings"
	"testing"
)

func TestValidateOptions(t *testing.T) {
	cases := []struct {
		name    string
		input   map[string]any
		wantErr string // substring to match; "" means no error
	}{
		{"nil", nil, ""},
		{"empty", map[string]any{}, ""},
		{"valid temperature", map[string]any{"temperature": 0.7}, ""},
		{"temperature low bound", map[string]any{"temperature": 0.0}, ""},
		{"temperature high bound", map[string]any{"temperature": 2.0}, ""},
		{"temperature too high", map[string]any{"temperature": 2.1}, "temperature"},
		{"temperature negative", map[string]any{"temperature": -0.1}, "temperature"},
		{"temperature wrong type", map[string]any{"temperature": "hot"}, "temperature"},
		{"valid top_p", map[string]any{"top_p": 0.9}, ""},
		{"top_p too high", map[string]any{"top_p": 1.5}, "top_p"},
		{"valid reasoning_effort low", map[string]any{"reasoning_effort": "low"}, ""},
		{"valid reasoning_effort medium", map[string]any{"reasoning_effort": "medium"}, ""},
		{"valid reasoning_effort high", map[string]any{"reasoning_effort": "high"}, ""},
		{"invalid reasoning_effort", map[string]any{"reasoning_effort": "extreme"}, "reasoning_effort"},
		{"valid max_output_tokens", map[string]any{"max_output_tokens": 1024}, ""},
		{"max_output_tokens zero", map[string]any{"max_output_tokens": 0}, "max_output_tokens"},
		{"max_output_tokens negative", map[string]any{"max_output_tokens": -1}, "max_output_tokens"},
		{"unknown key", map[string]any{"frequency_penalty": 1.0}, "unknown option"},
	}
	c := &Client{} // ValidateOptions has no dependencies
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.ValidateOptions(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseHintsAllFields(t *testing.T) {
	effort := "medium"
	in := map[string]any{
		"temperature":       0.5,
		"top_p":             0.9,
		"reasoning_effort":  effort,
		"max_output_tokens": 2048,
	}
	h, err := parseHints(in)
	if err != nil {
		t.Fatalf("parseHints: %v", err)
	}
	if h.Temperature == nil || *h.Temperature != 0.5 {
		t.Errorf("Temperature: got %v", h.Temperature)
	}
	if h.TopP == nil || *h.TopP != 0.9 {
		t.Errorf("TopP: got %v", h.TopP)
	}
	if h.ReasoningEffort == nil || *h.ReasoningEffort != "medium" {
		t.Errorf("ReasoningEffort: got %v", h.ReasoningEffort)
	}
	if h.MaxOutputTokens == nil || *h.MaxOutputTokens != 2048 {
		t.Errorf("MaxOutputTokens: got %v", h.MaxOutputTokens)
	}
}
