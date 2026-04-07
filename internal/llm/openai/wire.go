// Package openai implements an LLMClient against the OpenAI Chat Completions
// API. The same client serves OpenAI itself and any OpenAI-compatible backend
// (Ollama, vLLM, OpenRouter, Azure-via-compat, etc.) — the only differences
// are base_url and api_key, both set at construction time. See ADR-032.
package openai

import "encoding/json"

// --- Request types -----------------------------------------------------------

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Tools         []chatTool     `json:"tools,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`

	// Exactly one of MaxTokens or MaxCompletionTokens is set by the translator.
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`

	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	ReasoningEffort *string  `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage is a single message. `role` is one of "system", "user",
// "assistant", "tool". Content shape depends on role:
//   - system/user/assistant with text only: Content is a string.
//   - assistant with tool calls and no text: Content is nil (JSON null).
//   - tool: Content is a string; ToolCallID is set.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"` // pointer so we can emit JSON null
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function"
	Function chatToolCallFunc `json:"function"`
}

type chatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

type chatTool struct {
	Type     string       `json:"type"` // always "function"
	Function chatToolFunc `json:"function"`
}

type chatToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// --- Response types ----------------------------------------------------------

type chatResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
	Error   *apiError    `json:"error,omitempty"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatUsage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	CompletionTokensDetails *completionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type completionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// modelsResponse is the shape of GET {baseURL}/models.
type modelsResponse struct {
	Data []modelsEntry `json:"data"`
}

type modelsEntry struct {
	ID string `json:"id"`
}

// --- Streaming chunk types ---------------------------------------------------

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
	Usage   *chatUsage     `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Content   *string              `json:"content,omitempty"`
	ToolCalls []streamToolCallPart `json:"tool_calls,omitempty"`
}

type streamToolCallPart struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function streamToolCallPartFn `json:"function,omitempty"`
}

type streamToolCallPartFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}
