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
	InputTokens  int
	OutputTokens int
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
}

// ToolResultBlock is the result of a tool invocation, sent back in a user turn.
type ToolResultBlock struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// ContentBlock is a single block within a conversation turn.
// Valid implementations: TextBlock, ToolCallBlock, ToolResultBlock.
// The unexported method seals the interface so only these three types satisfy it.
type ContentBlock interface {
	contentBlock()
}

func (TextBlock) contentBlock()       {}
func (ToolCallBlock) contentBlock()   {}
func (ToolResultBlock) contentBlock() {}

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
type MessageResponse struct {
	Text       []TextBlock
	ToolCalls  []ToolCallBlock
	StopReason StopReason
	Usage      TokenUsage
}

// MessageChunk is a single chunk from a streaming response. Fields are pointers
// because each chunk carries only a subset of the full response. If Err is
// non-nil, the stream encountered an error and this is the final chunk.
type MessageChunk struct {
	Text       *string
	ToolCall   *ToolCallBlock
	Usage      *TokenUsage
	StopReason *StopReason
	Err        error
}

// ModelInfo describes an available model from a provider.
type ModelInfo struct {
	Name        string // model ID used in API calls (e.g. "gemini-2.0-flash", "claude-sonnet-4-6")
	DisplayName string // human-readable name (e.g. "Gemini 2.0 Flash")
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
