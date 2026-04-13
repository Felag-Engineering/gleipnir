package api

// ModelDisplayNames maps API model IDs to human-friendly display names.
// Used by the timeseries handler to return display names in cost_by_model so
// the frontend receives "Sonnet 4.6" rather than "claude-sonnet-4-6".
//
// Display name convention:
//   - Anthropic: strip the "Claude " prefix (e.g. "Claude Sonnet 4.6" → "Sonnet 4.6")
//   - Google: strip "(Preview)" suffix for preview models (keeps chart labels short)
//   - OpenAI: names are already concise; used as-is from the curated list
//
// Keys must stay in sync with MODEL_PRICING in frontend/src/constants/pricing.ts.
// The drift-prevention test in modelnames_test.go enforces that every model in
// each provider's curated list has an entry here.
var ModelDisplayNames = map[string]string{
	// Anthropic curated models (from internal/llm/anthropic/models.go curatedModels).
	"claude-opus-4-6":   "Opus 4.6",
	"claude-sonnet-4-6": "Sonnet 4.6",
	"claude-haiku-4-5":  "Haiku 4.5",
	"claude-opus-4-5":   "Opus 4.5",
	"claude-sonnet-4-5": "Sonnet 4.5",

	// Anthropic validation aliases (accepted by ValidateModelName but not shown in the UI).
	// These are dated pins stored in policies that predate the undated IDs above.
	"claude-sonnet-4-20250514": "Sonnet 4",

	// Anthropic legacy IDs — kept for backward compatibility with historical run data.
	"claude-haiku-3-5-20241022": "Haiku 3.5",
	"claude-opus-4-20250515":    "Opus 4",

	// Google curated models (from internal/llm/google/models.go curatedModels).
	// "(Preview)" is stripped to keep cost chart labels short.
	"gemini-3-pro-preview":   "Gemini 3 Pro",
	"gemini-3-flash-preview": "Gemini 3 Flash",
	"gemini-2.5-pro":         "Gemini 2.5 Pro",
	"gemini-2.5-flash":       "Gemini 2.5 Flash",
	"gemini-2.5-flash-lite":  "Gemini 2.5 Flash-Lite",
	"gemini-2.0-flash":       "Gemini 2.0 Flash",
	"gemini-2.0-flash-lite":  "Gemini 2.0 Flash-Lite",

	// OpenAI curated models (from internal/llm/openai/models.go curatedModels).
	"gpt-5":        "GPT-5",
	"gpt-5-mini":   "GPT-5 Mini",
	"gpt-5-nano":   "GPT-5 Nano",
	"gpt-4.1":      "GPT-4.1",
	"gpt-4.1-mini": "GPT-4.1 Mini",
	"gpt-4.1-nano": "GPT-4.1 Nano",
}

// GetModelDisplayName returns the display name for a model API ID.
// Falls back to the raw API ID when no mapping exists, so unknown models
// still appear in the cost chart rather than being silently dropped.
func GetModelDisplayName(apiModelID string) string {
	if name, ok := ModelDisplayNames[apiModelID]; ok {
		return name
	}
	return apiModelID
}
