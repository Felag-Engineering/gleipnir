package openai

import (
	"strings"

	"github.com/rapp992/gleipnir/internal/llm"
)

// BuildChatCompletionRequest translates an llm.MessageRequest into an OpenAI
// Chat Completions wire request. The `stream` argument sets Stream and
// StreamOptions; the translator is otherwise identical for sync and streaming.
// See spec §7.6 for the full translation rules.
func BuildChatCompletionRequest(req llm.MessageRequest, stream bool) chatRequest {
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
		out.Messages = append(out.Messages, translateTurn(turn)...)
	}

	for _, td := range req.Tools {
		out.Tools = append(out.Tools, chatTool{
			Type: "function",
			Function: chatToolFunc{
				Name:        td.Name,
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
func translateTurn(turn llm.ConversationTurn) []chatMessage {
	switch turn.Role {
	case llm.RoleAssistant:
		return []chatMessage{translateAssistantTurn(turn.Content)}
	case llm.RoleUser:
		return translateUserTurn(turn.Content)
	default:
		return nil
	}
}

func translateAssistantTurn(blocks []llm.ContentBlock) chatMessage {
	msg := chatMessage{Role: "assistant"}
	var texts []string
	for _, b := range blocks {
		switch v := b.(type) {
		case llm.TextBlock:
			texts = append(texts, v.Text)
		case llm.ToolCallBlock:
			msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
				ID:   v.ID,
				Type: "function",
				Function: chatToolCallFunc{
					Name:      v.Name,
					Arguments: string(v.Input),
				},
			})
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
