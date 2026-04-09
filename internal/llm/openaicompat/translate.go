package openaicompat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rapp992/gleipnir/internal/llm"
)

// BuildChatCompletionRequest translates an llm.MessageRequest into an OpenAI
// Chat Completions wire request. The `stream` argument sets Stream and
// StreamOptions; the translator is otherwise identical for sync and streaming.
//
// OpenAI restricts tool names to `^[a-zA-Z0-9_-]+$`. Gleipnir tool names
// routinely contain other characters (most notably '.' as an MCP namespace
// separator), so names must be sanitized before they hit the wire. Callers
// pass a ToolNameMapping built with llm.BuildNameMapping(req.Tools, "-"); an
// empty mapping is safe and disables rewriting (used by tests that don't
// exercise tool calls). See spec §7.6 for the full translation rules.
func BuildChatCompletionRequest(req llm.MessageRequest, stream bool, names llm.ToolNameMapping) chatRequest {
	out := chatRequest{
		Model:    req.Model,
		Messages: make([]chatMessage, 0, len(req.History)+1),
	}
	if stream {
		out.Stream = true
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	if req.SystemPrompt != "" {
		s := req.SystemPrompt
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: &s})
	}

	for _, turn := range req.History {
		out.Messages = append(out.Messages, translateTurn(turn, names)...)
	}

	for _, td := range req.Tools {
		name := td.Name
		if mapped, ok := names.OriginalToSanitized[name]; ok {
			name = mapped
		}
		out.Tools = append(out.Tools, chatTool{
			Type: "function",
			Function: chatToolFunc{
				Name:        name,
				Description: td.Description,
				Parameters:  td.InputSchema,
			},
		})
	}

	// MaxTokens: route to the right field depending on the model family.
	hints, _ := req.Hints.(*OpenAIHints)
	effectiveMax := req.MaxTokens
	if hints != nil && hints.MaxOutputTokens != nil {
		effectiveMax = *hints.MaxOutputTokens
	}
	if effectiveMax > 0 {
		v := effectiveMax
		if isOSeriesModel(req.Model) {
			out.MaxCompletionTokens = &v
		} else {
			out.MaxTokens = &v
		}
	}

	if hints != nil {
		if hints.Temperature != nil {
			t := *hints.Temperature
			out.Temperature = &t
		}
		if hints.TopP != nil {
			p := *hints.TopP
			out.TopP = &p
		}
		// reasoning_effort is an o-series-only parameter; sending it to other
		// models causes a 400 error from OpenAI.
		if hints.ReasoningEffort != nil && isOSeriesModel(req.Model) {
			e := *hints.ReasoningEffort
			out.ReasoningEffort = &e
		}
	}

	return out
}

// translateTurn maps one Gleipnir ConversationTurn into one or more wire
// messages. A user turn containing tool results produces N role:"tool"
// messages; a user turn mixing text and tool results emits tool messages
// first, then a user message with the concatenated text.
func translateTurn(turn llm.ConversationTurn, names llm.ToolNameMapping) []chatMessage {
	switch turn.Role {
	case llm.RoleAssistant:
		return []chatMessage{translateAssistantTurn(turn.Content, names)}
	case llm.RoleUser:
		return translateUserTurn(turn.Content)
	default:
		return nil
	}
}

func translateAssistantTurn(blocks []llm.ContentBlock, names llm.ToolNameMapping) chatMessage {
	msg := chatMessage{Role: "assistant"}
	var texts []string
	for _, b := range blocks {
		switch v := b.(type) {
		case llm.TextBlock:
			texts = append(texts, v.Text)
		case llm.ToolCallBlock:
			name := v.Name
			if mapped, ok := names.OriginalToSanitized[name]; ok {
				name = mapped
			}
			msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
				ID:   v.ID,
				Type: "function",
				Function: chatToolCallFunc{
					Name:      name,
					Arguments: string(v.Input),
				},
			})
		case llm.ThinkingBlock:
			// Chat Completions API has no reasoning content round-trip; skip.
		}
		// ToolResultBlock is not valid in an assistant turn; silently ignored.
	}
	if len(texts) > 0 {
		joined := strings.Join(texts, "\n\n")
		msg.Content = &joined
	}
	// Content stays nil (JSON null) when there are tool calls and no text.
	return msg
}

func translateUserTurn(blocks []llm.ContentBlock) []chatMessage {
	var toolMsgs []chatMessage
	var texts []string
	for _, b := range blocks {
		switch v := b.(type) {
		case llm.TextBlock:
			texts = append(texts, v.Text)
		case llm.ToolResultBlock:
			content := v.Content
			if v.IsError {
				content = "[error] " + content
			}
			c := content
			toolMsgs = append(toolMsgs, chatMessage{
				Role:       "tool",
				Content:    &c,
				ToolCallID: v.ToolCallID,
			})
		}
	}
	// Tool messages must come before any user text message so the model sees
	// results in the same order the calls were made.
	out := toolMsgs
	if len(texts) > 0 {
		joined := strings.Join(texts, "\n\n")
		out = append(out, chatMessage{Role: "user", Content: &joined})
	}
	return out
}

// ParseChatCompletionResponse translates a wire response into the normalized
// llm.MessageResponse. Tool-call names returned by OpenAI are the sanitized
// wire form; the `names` mapping reverses them back to the original Gleipnir
// names so the agent runtime can dispatch them. An empty mapping passes names
// through unchanged (used by tests that don't exercise tool calls). Returns
// an error only on malformed tool call arguments — all other abnormalities
// (unknown finish reasons, missing usage details) degrade gracefully.
func ParseChatCompletionResponse(wire *chatResponse, names llm.ToolNameMapping) (*llm.MessageResponse, error) {
	out := &llm.MessageResponse{}
	if len(wire.Choices) == 0 {
		return out, nil
	}
	choice := wire.Choices[0]

	// Content is omitted (nil) when the response is tool-calls-only; an empty
	// string string also carries no useful text and would create a spurious block
	// that confuses callers expecting at least one real token.
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		out.Text = []llm.TextBlock{{Text: *choice.Message.Content}}
	}

	for _, tc := range choice.Message.ToolCalls {
		// Validate that Arguments is parseable JSON — callers trust Input is.
		if !json.Valid([]byte(tc.Function.Arguments)) {
			return nil, fmt.Errorf("openai: tool call %q: arguments is not valid JSON: %q",
				tc.Function.Name, tc.Function.Arguments)
		}
		name := tc.Function.Name
		if original, ok := names.SanitizedToOriginal[name]; ok {
			name = original
		}
		out.ToolCalls = append(out.ToolCalls, llm.ToolCallBlock{
			ID:    tc.ID,
			Name:  name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}

	switch choice.FinishReason {
	case "stop":
		out.StopReason = llm.StopReasonEndTurn
	case "tool_calls":
		out.StopReason = llm.StopReasonToolUse
	case "length":
		out.StopReason = llm.StopReasonMaxTokens
	default:
		// "content_filter" and any future finish reasons are mapped to Error so
		// the agent runtime can surface them rather than silently continuing.
		out.StopReason = llm.StopReasonError
	}

	if wire.Usage != nil {
		out.Usage.InputTokens = wire.Usage.PromptTokens
		out.Usage.OutputTokens = wire.Usage.CompletionTokens
		if wire.Usage.CompletionTokensDetails != nil {
			out.Usage.ThinkingTokens = wire.Usage.CompletionTokensDetails.ReasoningTokens
		}
	}

	// Thinking is always nil for OpenAI — reasoning content is not surfaced
	// by the Chat Completions endpoint. See ADR-032 §2 ("Why Chat Completions
	// only, not the Responses API").
	return out, nil
}

// isOSeriesModel returns true when the model should route max_tokens to
// max_completion_tokens and honor reasoning_effort. The heuristic is:
// name starts with "o<digit>" (o1, o3, o4) or contains "reasoning".
// Contained to this function; no other place in the package pattern-matches
// on model names.
func isOSeriesModel(model string) bool {
	if strings.Contains(model, "reasoning") {
		return true
	}
	if len(model) < 2 {
		return false
	}
	if model[0] != 'o' {
		return false
	}
	c := model[1]
	return c >= '0' && c <= '9'
}
