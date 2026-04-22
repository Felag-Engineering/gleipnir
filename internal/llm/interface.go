// Package llm defines the provider-agnostic interface and shared types for LLM
// API clients. Implementations live in sub-packages (e.g. internal/llm/anthropic).
package llm

import (
	"context"
	"encoding/json"
)

// Role identifies the author of a conversation turn.
// Only "user" and "assistant" are valid — tool results are content blocks within
// a user turn, not a separate role.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

func (r Role) String() string { return string(r) }

// StopReason maps provider-specific stop reasons to a normalized set.
// The int enum avoids tying the abstraction to any provider's wire format.
type StopReason int

const (
	// StopReasonUnknown is the zero value, indicating the stop reason has not been set.
	StopReasonUnknown StopReason = iota
	// StopReasonEndTurn indicates the model finished its response naturally.
	StopReasonEndTurn
	// StopReasonToolUse indicates the model issued one or more tool calls.
	StopReasonToolUse
	// StopReasonMaxTokens indicates the response was truncated at the token limit.
	StopReasonMaxTokens
	// StopReasonError indicates the response was terminated due to an error.
	StopReasonError
)

func (s StopReason) String() string {
	switch s {
	case StopReasonUnknown:
		return "unknown"
	case StopReasonEndTurn:
		return "end_turn"
	case StopReasonToolUse:
		return "tool_use"
	case StopReasonMaxTokens:
		return "max_tokens"
	case StopReasonError:
		return "error"
	default:
		return "unknown"
	}
}

// TokenUsage records token consumption for a single API call.
type TokenUsage struct {
	InputTokens    int
	OutputTokens   int
	ThinkingTokens int // Gemini reports thinking tokens separately; Anthropic includes them in OutputTokens
}

// ThinkingBlock is an internal reasoning block returned by the model.
// ThinkingBlock is included in conversation history for providers that require it
// for multi-turn continuity (Anthropic via signature, OpenAI via encrypted content).
// Providers that don't need it (Google, OpenAI-compat) silently skip it during
// message translation.
//
// Provider is the discriminator: request builders silently skip blocks whose Provider
// does not match the current provider (empty or mismatched). ProviderState is opaque,
// provider-owned JSON; each provider package defines an unexported state struct and its
// own marshal/unmarshal helpers — no shared schema. Empty ProviderState (nil or len 0)
// means text-only with no round-trip state; request builders skip the round-trip (same
// semantics as empty Signature/EncryptedContent in the old shape). Malformed
// ProviderState JSON is returned as an error — do not silently drop continuity.
//
// ADR-026 (amended): opaque provider-owned state
type ThinkingBlock struct {
	Provider      string
	Text          string
	Redacted      bool
	ProviderState json.RawMessage
}

// TextBlock is a plain text content block from the model's response.
type TextBlock struct {
	Text string
}

// ToolCallBlock is a tool invocation requested by the model.
type ToolCallBlock struct {
	// ID is a synthetic UUID assigned by the implementation. Providers that
	// return their own IDs (e.g. Anthropic) pass them through; providers that
	// do not (e.g. Google) must generate a UUID.
	ID    string
	Name  string
	Input json.RawMessage

	// ProviderMetadata holds opaque provider-specific bytes that must be
	// round-tripped on subsequent requests. Non-Google providers ignore this
	// field. Current keys: "google.thought_signature".
	ProviderMetadata map[string][]byte
}

// ToolResultBlock is the result of a tool invocation, sent back in a user turn.
type ToolResultBlock struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// ContentBlock is a single block within a conversation turn.
// Valid implementations: TextBlock, ToolCallBlock, ToolResultBlock, ThinkingBlock.
// The unexported method seals the interface so only these four types satisfy it.
type ContentBlock interface {
	contentBlock()
}

func (TextBlock) contentBlock()       {}
func (ToolCallBlock) contentBlock()   {}
func (ToolResultBlock) contentBlock() {}
func (ThinkingBlock) contentBlock()   {}

// ConversationTurn is a single turn in the conversation history.
// Tool results are content blocks within a user turn — there is no separate
// "tool_result" role.
type ConversationTurn struct {
	Role    Role
	Content []ContentBlock
}

// ToolDefinition is a tool the model may call, presented in the API request.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ProviderHints carries optional provider-specific tuning. The concrete type
// is defined by each provider implementation. Callers pass the provider's
// hints struct directly; implementations type-assert to their own type and
// ignore unrecognized values. Nil means use provider defaults.
type ProviderHints any

// MessageRequest is the complete input for a single LLM API call.
type MessageRequest struct {
	Model        string
	MaxTokens    int
	SystemPrompt string
	History      []ConversationTurn
	Tools        []ToolDefinition
	Hints        ProviderHints
}

// MessageResponse is the parsed output of a single LLM API call.
// Text and ToolCalls are separate slices because the BoundAgent processes them
// differently: text goes to audit as thought steps, tool calls go to dispatch.
// Thinking holds internal reasoning blocks for audit purposes only.
type MessageResponse struct {
	Text       []TextBlock
	ToolCalls  []ToolCallBlock
	Thinking   []ThinkingBlock
	StopReason StopReason
	Usage      TokenUsage
}

// MessageChunk is a single chunk from a streaming response. Fields are pointers
// because each chunk carries only a subset of the full response. At most one of
// Text, ToolCall, or Thinking is non-nil per chunk — one content block per chunk
// matches real streaming semantics where blocks arrive incrementally. If Err is
// non-nil, the stream encountered an error and this is the final chunk.
type MessageChunk struct {
	Text       *string
	ToolCall   *ToolCallBlock
	Thinking   *ThinkingBlock
	Usage      *TokenUsage
	StopReason *StopReason
	Err        error
}

// ModelInfo describes an available model from a provider.
type ModelInfo struct {
	Name        string // model ID used in API calls (e.g. "gemini-2.0-flash", "claude-sonnet-4-6")
	DisplayName string // human-readable name (e.g. "Gemini 2.0 Flash")
	IsReasoning bool   // true if the model supports reasoning/thinking (e.g. o1*, gpt-5*)
}

// ModelLister is the subset of ProviderRegistry used by model listing endpoints.
// Defined as an interface so handler packages do not depend on the concrete registry.
type ModelLister interface {
	ListModels(ctx context.Context, provider string) ([]ModelInfo, error)
	ListAllModels(ctx context.Context) (map[string][]ModelInfo, error)
	InvalidateModelCache(provider string) error
	InvalidateAllModelCaches()
}

// LLMClient is the provider-agnostic interface for interacting with an LLM API.
type LLMClient interface {
	// CreateMessage sends a single synchronous request and returns the complete
	// response.
	CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)

	// StreamMessage sends a request and returns a channel that emits chunks as
	// they arrive. The channel is closed when the response is complete or an
	// error occurs. The final chunk carries Usage and StopReason.
	StreamMessage(ctx context.Context, req MessageRequest) (<-chan MessageChunk, error)

	// ValidateOptions validates provider-specific options from the policy YAML.
	// It returns an error describing all validation problems if any are found.
	// Empty or nil options are valid.
	ValidateOptions(options map[string]any) error

	// ValidateModelName returns nil if modelName is recognized by this provider,
	// or a descriptive error if not. Implementations may make a network call to
	// fetch available models; results are cached for the lifetime of the process.
	ValidateModelName(ctx context.Context, modelName string) error

	// ListModels returns the models available from this provider. Results are
	// cached; call InvalidateModelCache to force a refresh on the next call.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// InvalidateModelCache clears any cached model list so the next call to
	// ListModels or ValidateModelName fetches fresh data from the API.
	InvalidateModelCache()
}
