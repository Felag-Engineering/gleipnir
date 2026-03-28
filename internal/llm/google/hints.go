// Package google provides a Gemini-backed LLM client.
package google

// GeminiHints carries optional Gemini-specific tuning for a single API call.
// Callers pass this as MessageRequest.Hints; CreateMessage type-asserts to
// *GeminiHints and ignores the field if the assertion fails.
type GeminiHints struct {
	// ThinkingBudget controls the token budget for the model's internal
	// reasoning/thinking step. Nil means use the model default.
	ThinkingBudget *int32

	// EnableGrounding adds a GoogleSearch tool to the request when true,
	// enabling Gemini's grounded search feature.
	EnableGrounding *bool
}
