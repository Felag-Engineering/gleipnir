package openaicompat

import "fmt"

// OpenAIHints carries optional tuning fields that map to OpenAI Chat
// Completions request parameters. All fields are pointers so "unset" is
// distinct from "zero" — nil means the translator omits the field.
type OpenAIHints struct {
	Temperature     *float64
	TopP            *float64
	ReasoningEffort *string // "low" | "medium" | "high"
	MaxOutputTokens *int
}

var validReasoningEfforts = map[string]bool{"low": true, "medium": true, "high": true}

// parseHints converts a policy-YAML options map into an *OpenAIHints, or
// returns a descriptive error if any field is invalid or unknown.
func parseHints(options map[string]any) (*OpenAIHints, error) {
	h := &OpenAIHints{}
	for key, raw := range options {
		switch key {
		case "temperature":
			f, ok := toFloat64(raw)
			if !ok {
				return nil, fmt.Errorf("temperature: must be a number, got %T", raw)
			}
			if f < 0 || f > 2 {
				return nil, fmt.Errorf("temperature: must be in [0, 2], got %v", f)
			}
			h.Temperature = &f
		case "top_p":
			f, ok := toFloat64(raw)
			if !ok {
				return nil, fmt.Errorf("top_p: must be a number, got %T", raw)
			}
			if f < 0 || f > 1 {
				return nil, fmt.Errorf("top_p: must be in [0, 1], got %v", f)
			}
			h.TopP = &f
		case "reasoning_effort":
			s, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("reasoning_effort: must be a string, got %T", raw)
			}
			if !validReasoningEfforts[s] {
				return nil, fmt.Errorf("reasoning_effort: must be one of low, medium, high; got %q", s)
			}
			h.ReasoningEffort = &s
		case "max_output_tokens":
			n, ok := toInt(raw)
			if !ok {
				return nil, fmt.Errorf("max_output_tokens: must be an integer, got %T", raw)
			}
			if n <= 0 {
				return nil, fmt.Errorf("max_output_tokens: must be > 0, got %d", n)
			}
			h.MaxOutputTokens = &n
		default:
			return nil, fmt.Errorf("unknown option %q", key)
		}
	}
	return h, nil
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if x != float64(int(x)) {
			return 0, false
		}
		return int(x), true
	default:
		return 0, false
	}
}
