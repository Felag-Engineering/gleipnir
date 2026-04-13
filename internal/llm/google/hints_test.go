package google

import (
	"strings"
	"testing"
)

func TestParseHints_AllFields(t *testing.T) {
	level := "medium"
	budget := 512
	grounding := true

	h, err := parseHints(map[string]any{
		"thinking_level":   level,
		"thinking_budget":  budget,
		"enable_grounding": grounding,
	})
	if err != nil {
		t.Fatalf("parseHints returned unexpected error: %v", err)
	}
	if h.ThinkingLevel == nil || *h.ThinkingLevel != "medium" {
		t.Errorf("ThinkingLevel: got %v, want %q", h.ThinkingLevel, "medium")
	}
	if h.ThinkingBudget == nil || *h.ThinkingBudget != 512 {
		t.Errorf("ThinkingBudget: got %v, want 512", h.ThinkingBudget)
	}
	if h.EnableGrounding == nil || *h.EnableGrounding != true {
		t.Errorf("EnableGrounding: got %v, want true", h.EnableGrounding)
	}
}

func TestParseHints_NilOptions(t *testing.T) {
	h, err := parseHints(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h != nil {
		t.Errorf("expected nil hints for nil input, got %+v", h)
	}
}

func TestParseHints_EmptyOptions(t *testing.T) {
	h, err := parseHints(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil hints for empty map, got nil")
	}
	if h.ThinkingLevel != nil || h.ThinkingBudget != nil || h.EnableGrounding != nil {
		t.Errorf("expected all fields nil for empty options, got %+v", h)
	}
}

func TestParseHints_UnknownKey(t *testing.T) {
	_, err := parseHints(map[string]any{"unknown_key": "value"})
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown option") {
		t.Errorf("error %q does not mention unknown option", err.Error())
	}
}

func TestParseHints_InvalidThinkingLevel(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"invalid string", "max", "must be one of"},
		{"empty string", "", "must be one of"},
		{"wrong type", 42, "must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseHints(map[string]any{"thinking_level": tc.value})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseHints_InvalidThinkingBudget(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"zero", 0, "must be > 0"},
		{"negative", -1, "must be > 0"},
		{"non-integer float", 1.5, "must be an integer"},
		{"string", "big", "must be an integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseHints(map[string]any{"thinking_budget": tc.value})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseHints_InvalidGroundingType(t *testing.T) {
	_, err := parseHints(map[string]any{"enable_grounding": "yes"})
	if err == nil {
		t.Fatal("expected error for non-bool enable_grounding, got nil")
	}
	if !strings.Contains(err.Error(), "must be a bool") {
		t.Errorf("error %q does not mention expected type", err.Error())
	}
}

func TestParseHints_AllThinkingLevelValues(t *testing.T) {
	validLevels := []string{"minimal", "low", "medium", "high"}
	for _, level := range validLevels {
		t.Run(level, func(t *testing.T) {
			h, err := parseHints(map[string]any{"thinking_level": level})
			if err != nil {
				t.Fatalf("parseHints(%q): unexpected error: %v", level, err)
			}
			if h.ThinkingLevel == nil || *h.ThinkingLevel != level {
				t.Errorf("ThinkingLevel: got %v, want %q", h.ThinkingLevel, level)
			}
		})
	}
}
