package openai

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/felag-engineering/gleipnir/internal/llm"
)

// buildInput translates the provider-neutral MessageRequest history into the
// Responses API input list. The system prompt is passed separately as
// ResponseNewParams.Instructions; it is not included in the input list here.
func buildInput(req llm.MessageRequest, names llm.ToolNameMapping) ([]responses.ResponseInputItemUnionParam, error) {
	var items []responses.ResponseInputItemUnionParam
	for _, turn := range req.History {
		turnItems, err := translateTurn(turn, names)
		if err != nil {
			return nil, err
		}
		items = append(items, turnItems...)
	}
	return items, nil
}

// translateTurn converts a single ConversationTurn into one or more input
// items. The Responses API represents tool calls and results as separate
// top-level items, not as nested content within a single message.
func translateTurn(turn llm.ConversationTurn, names llm.ToolNameMapping) ([]responses.ResponseInputItemUnionParam, error) {
	switch turn.Role {
	case llm.RoleAssistant:
		return translateAssistantTurn(turn.Content, names)
	case llm.RoleUser:
		return translateUserTurn(turn.Content), nil
	default:
		return nil, nil
	}
}

func translateAssistantTurn(blocks []llm.ContentBlock, names llm.ToolNameMapping) ([]responses.ResponseInputItemUnionParam, error) {
	var items []responses.ResponseInputItemUnionParam
	var textParts []string

	for _, b := range blocks {
		switch v := b.(type) {
		case llm.TextBlock:
			textParts = append(textParts, v.Text)
		case llm.ToolCallBlock:
			// Flush accumulated text first, then emit the function call item.
			if len(textParts) > 0 {
				items = append(items, assistantTextItem(strings.Join(textParts, "\n\n")))
				textParts = nil
			}
			name := v.Name
			if mapped, ok := names.OriginalToSanitized[name]; ok {
				name = mapped
			}
			args := string(v.Input)
			if args == "" {
				args = "{}"
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(args, v.ID, name))
		case llm.ThinkingBlock:
			if v.Provider != "" && v.Provider != "openai" {
				slog.Debug("openai: skipping ThinkingBlock from non-OpenAI provider",
					"block_provider", v.Provider)
				continue
			}
			state, err := unmarshalThinkingState(v.ProviderState)
			if err != nil {
				return nil, fmt.Errorf("openai: translateAssistantTurn: %w", err)
			}
			if state.ID == "" && state.EncryptedContent == "" {
				// No round-trip data — skip (non-OpenAI source or empty state).
				slog.Debug("openai: skipping ThinkingBlock with no ID and no encrypted_content")
				continue
			}
			// Flush accumulated text before emitting the reasoning item.
			if len(textParts) > 0 {
				items = append(items, assistantTextItem(strings.Join(textParts, "\n\n")))
				textParts = nil
			}
			// The Responses API requires a non-nil summary array on reasoning
			// input items, even when the model returned no summary text
			// (common with encrypted_content-only items). Always include at
			// least one entry to satisfy the wire format.
			summaryParams := []responses.ResponseReasoningItemSummaryParam{{Text: v.Text}}
			item := responses.ResponseInputItemParamOfReasoning(state.ID, summaryParams)
			if state.EncryptedContent != "" {
				item.OfReasoning.EncryptedContent = param.NewOpt(state.EncryptedContent)
			}
			items = append(items, item)
		}
	}
	if len(textParts) > 0 {
		items = append(items, assistantTextItem(strings.Join(textParts, "\n\n")))
	}
	return items, nil
}

// assistantTextItem encodes a plain assistant text turn as an EasyInputMessage
// with role "assistant". The Responses API accepts this for prior turns.
func assistantTextItem(text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant)
}

func translateUserTurn(blocks []llm.ContentBlock) []responses.ResponseInputItemUnionParam {
	var items []responses.ResponseInputItemUnionParam
	var textParts []string

	for _, b := range blocks {
		switch v := b.(type) {
		case llm.TextBlock:
			textParts = append(textParts, v.Text)
		case llm.ToolResultBlock:
			// Flush text before tool results so tool results appear in order.
			if len(textParts) > 0 {
				items = append(items, userTextItem(strings.Join(textParts, "\n\n")))
				textParts = nil
			}
			content := v.Content
			if v.IsError {
				content = "[error] " + content
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(v.ToolCallID, content))
		}
	}
	if len(textParts) > 0 {
		items = append(items, userTextItem(strings.Join(textParts, "\n\n")))
	}
	return items
}

func userTextItem(text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser)
}

// buildTools translates provider-neutral ToolDefinitions into Responses API
// ToolUnionParams. Returns the tool list and a name mapping for round-trip
// sanitization. Returns an error on name collisions after sanitization.
func buildTools(tools []llm.ToolDefinition) ([]responses.ToolUnionParam, llm.ToolNameMapping, error) {
	result := make([]responses.ToolUnionParam, 0, len(tools))
	names := llm.ToolNameMapping{
		SanitizedToOriginal: make(map[string]string, len(tools)),
		OriginalToSanitized: make(map[string]string, len(tools)),
	}

	for _, t := range tools {
		sanitized := llm.SanitizeToolName(t.Name, "-")

		if existing, conflict := names.SanitizedToOriginal[sanitized]; conflict && existing != t.Name {
			return nil, llm.ToolNameMapping{}, fmt.Errorf(
				"tool name collision after sanitization: %q and %q both become %q",
				existing, t.Name, sanitized,
			)
		}
		names.SanitizedToOriginal[sanitized] = t.Name
		names.OriginalToSanitized[t.Name] = sanitized

		// The SDK's FunctionToolParam.Parameters is map[string]any.
		var params map[string]any
		if len(t.InputSchema) > 0 {
			if err := json.Unmarshal(t.InputSchema, &params); err != nil {
				return nil, llm.ToolNameMapping{}, fmt.Errorf("unmarshalling schema for tool %s: %w", t.Name, err)
			}
		}

		tool := responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        sanitized,
				Description: openaisdk.String(t.Description),
				Parameters:  params,
			},
		}
		result = append(result, tool)
	}
	return result, names, nil
}

// translateResponse converts a Responses API response into the provider-neutral
// MessageResponse. sanitizedToOriginal reverses tool name sanitization.
func translateResponse(resp *responses.Response, names llm.ToolNameMapping) (*llm.MessageResponse, error) {
	out := &llm.MessageResponse{}

	for _, item := range resp.Output {
		switch v := item.AsAny().(type) {
		case responses.ResponseOutputMessage:
			for _, part := range v.Content {
				switch p := part.AsAny().(type) {
				case responses.ResponseOutputText:
					out.Text = append(out.Text, llm.TextBlock{Text: p.Text})
				}
			}
		case responses.ResponseFunctionToolCall:
			if !json.Valid([]byte(v.Arguments)) {
				return nil, fmt.Errorf("openai: tool call %q: arguments is not valid JSON: %q", v.Name, v.Arguments)
			}
			name := v.Name
			if original, ok := names.SanitizedToOriginal[name]; ok {
				name = original
			}
			out.ToolCalls = append(out.ToolCalls, llm.ToolCallBlock{
				ID:    v.CallID,
				Name:  name,
				Input: json.RawMessage(v.Arguments),
			})
		case responses.ResponseReasoningItem:
			var parts []string
			for _, s := range v.Summary {
				parts = append(parts, s.Text)
			}
			summaryText := strings.Join(parts, "\n")
			// Only append when there is something to round-trip: encrypted content
			// for multi-turn continuity, or summary text for audit display.
			if v.EncryptedContent != "" || summaryText != "" {
				state := openaiThinkingState{ID: v.ID, EncryptedContent: v.EncryptedContent}
				raw, marshalErr := marshalThinkingState(state)
				if marshalErr != nil {
					return nil, fmt.Errorf("openai: translateResponse: %w", marshalErr)
				}
				out.Thinking = append(out.Thinking, llm.ThinkingBlock{
					Provider:      "openai",
					Text:          summaryText,
					ProviderState: raw,
				})
			}
		default:
			slog.Debug("openai: skipping unhandled output item", "type", item.Type)
		}
	}

	// Map Responses API status to provider-neutral stop reasons. We check for
	// tool calls first because a "completed" status can coexist with tool_use
	// when the model stops after requesting tools (Responses API always sets
	// status=completed when it finishes its turn, even with tool calls).
	switch resp.Status {
	case responses.ResponseStatusCompleted:
		if len(out.ToolCalls) > 0 {
			out.StopReason = llm.StopReasonToolUse
		} else {
			out.StopReason = llm.StopReasonEndTurn
		}
	case responses.ResponseStatusIncomplete:
		out.StopReason = llm.StopReasonMaxTokens
	default:
		out.StopReason = llm.StopReasonUnknown
	}

	out.Usage = llm.TokenUsage{
		InputTokens:    int(resp.Usage.InputTokens),
		OutputTokens:   int(resp.Usage.OutputTokens),
		ThinkingTokens: int(resp.Usage.OutputTokensDetails.ReasoningTokens),
	}

	return out, nil
}
