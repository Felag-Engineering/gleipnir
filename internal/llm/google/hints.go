// Package google provides a Gemini-backed LLM client.
package google

import (
	"fmt"

	"google.golang.org/genai"
)

// GeminiHints carries optional Gemini-specific tuning for a single API call.
// Callers pass this as MessageRequest.Hints; CreateMessage type-asserts to
// *GeminiHints and ignores the field if the assertion fails.
type GeminiHints struct {
	// ThinkingBudget controls the token budget for the model's internal
	// reasoning/thinking step. Nil means use the model default.
	ThinkingBudget *int32

	// ThinkingLevel controls the thinking intensity for models that support it.
	// Nil means use the model default. Valid values: "minimal", "low", "medium", "high".
	ThinkingLevel *string

	// EnableGrounding adds a GoogleSearch tool to the request when true,
	// enabling Gemini's grounded search feature.
	EnableGrounding *bool
}

// thinkingLevelToGenai is the single source of truth for the string-to-SDK
// mapping. ValidateOptions and buildConfig both reference this map so there is
// one place to add new levels.
var thinkingLevelToGenai = map[string]genai.ThinkingLevel{
	"minimal": genai.ThinkingLevelMinimal,
	"low":     genai.ThinkingLevelLow,
	"medium":  genai.ThinkingLevelMedium,
	"high":    genai.ThinkingLevelHigh,
}

// parseHints converts a policy-YAML options map into a *GeminiHints, or
// returns a descriptive error if any field is invalid or unknown.
// Nil input returns nil hints with no error.
func parseHints(options map[string]any) (*GeminiHints, error) {
	if options == nil {
		return nil, nil
	}
	h := &GeminiHints{}
	for key, raw := range options {
		switch key {
		case "thinking_level":
			s, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("thinking_level: must be a string, got %T", raw)
			}
			if _, valid := thinkingLevelToGenai[s]; !valid {
				return nil, fmt.Errorf("thinking_level: must be one of minimal, low, medium, high; got %q", s)
			}
			h.ThinkingLevel = &s
		case "thinking_budget":
			n, ok := toInt32(raw)
			if !ok {
				return nil, fmt.Errorf("thinking_budget: must be an integer, got %T", raw)
			}
			if n <= 0 {
				return nil, fmt.Errorf("thinking_budget: must be > 0, got %d", n)
			}
			h.ThinkingBudget = &n
		case "enable_grounding":
			b, ok := raw.(bool)
			if !ok {
				return nil, fmt.Errorf("enable_grounding: must be a bool, got %T", raw)
			}
			h.EnableGrounding = &b
		default:
			return nil, fmt.Errorf("unknown option %q", key)
		}
	}
	return h, nil
}

// toInt32 converts numeric types to int32. It accepts int, int64, and float64
// (rejecting non-integer float64 values).
func toInt32(v any) (int32, bool) {
	switch x := v.(type) {
	case int:
		return int32(x), true
	case int64:
		return int32(x), true
	case float64:
		if x != float64(int32(x)) {
			return 0, false
		}
		return int32(x), true
	default:
		return 0, false
	}
}
