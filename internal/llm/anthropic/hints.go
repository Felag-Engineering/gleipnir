// Package anthropic implements llm.LLMClient for the Anthropic Claude API.
package anthropic

// AnthropicHints carries optional Anthropic-specific tuning for a single API
// call. Callers pass this as MessageRequest.Hints; CreateMessage type-asserts
// to *AnthropicHints and ignores the field if the assertion fails.
type AnthropicHints struct {
	// MaxTokens overrides the per-call max_tokens. MessageRequest.MaxTokens
	// takes precedence over this field when set. Nil means fall through to
	// the package default of 4096.
	MaxTokens *int64

	// EnablePromptCaching adds a CacheControlEphemeralParam to the system
	// prompt block when true. This enables Anthropic's prompt caching feature,
	// which can reduce latency and cost for repeated system prompts.
	EnablePromptCaching *bool
}
