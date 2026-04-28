package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/responses"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

// consumeStream reads events from the SDK streaming response and emits
// llm.MessageChunk values on out. The channel is closed exactly once when the
// stream ends (successfully or with an error). Callers must not close out.
//
// The Responses API emits fine-grained delta events per item type:
//   - response.output_text.delta  → text chunk
//   - response.function_call_arguments.delta → accumulate tool args
//   - response.output_item.done   → flush completed tool call
//   - response.completed          → emit final usage + stop reason
func consumeStream(
	ctx context.Context,
	stream streamIterator,
	out chan<- llm.MessageChunk,
	names llm.ToolNameMapping,
) {
	defer close(out)

	// Track per-item tool call argument accumulation, keyed by item_id.
	type partialCall struct {
		id   string // call_id for FunctionCallOutput
		name string
		args strings.Builder
	}
	partials := map[string]*partialCall{}

	for stream.Next() {
		select {
		case <-ctx.Done():
			out <- llm.MessageChunk{Err: ctx.Err()}
			return
		default:
		}

		evt := stream.Current()
		switch v := evt.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			text := v.Delta
			out <- llm.MessageChunk{Text: &text}

		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			p := partials[v.ItemID]
			if p == nil {
				p = &partialCall{}
				partials[v.ItemID] = p
			}
			p.args.WriteString(v.Delta)

		case responses.ResponseOutputItemDoneEvent:
			// When a function_call item is done, emit the complete tool call.
			if fc, ok := v.Item.AsAny().(responses.ResponseFunctionToolCall); ok {
				p := partials[v.Item.ID]
				var argsStr string
				if p != nil {
					argsStr = p.args.String()
					delete(partials, v.Item.ID)
				}
				if argsStr == "" {
					argsStr = fc.Arguments
				}
				if argsStr == "" {
					argsStr = "{}"
				}
				name := fc.Name
				if original, ok := names.SanitizedToOriginal[name]; ok {
					name = original
				}
				out <- llm.MessageChunk{ToolCall: &llm.ToolCallBlock{
					ID:    fc.CallID,
					Name:  name,
					Input: json.RawMessage(argsStr),
				}}
			} else if ri, ok := v.Item.AsAny().(responses.ResponseReasoningItem); ok {
				// encrypted_content is present for reasoning models when the Include
				// param is set (see buildParams). Non-reasoning models return only
				// summary text.
				var parts []string
				for _, s := range ri.Summary {
					parts = append(parts, s.Text)
				}
				summaryText := strings.Join(parts, "\n")
				if ri.EncryptedContent != "" || summaryText != "" {
					raw, err := marshalThinkingState(openaiThinkingState{ID: ri.ID, EncryptedContent: ri.EncryptedContent})
					if err != nil {
						out <- llm.MessageChunk{Err: fmt.Errorf("openai: marshal thinking state: %w", err)}
						return
					}
					out <- llm.MessageChunk{Thinking: &llm.ThinkingBlock{
						Provider:      "openai",
						Text:          summaryText,
						ProviderState: raw,
					}}
				}
			}

		case responses.ResponseCompletedEvent:
			// Determine stop reason from the completed response. Tool calls
			// already emitted above tell us this was a tool_use turn.
			var stop llm.StopReason
			switch v.Response.Status {
			case responses.ResponseStatusCompleted:
				// Check if any tool calls were in the output.
				hasTool := false
				for _, item := range v.Response.Output {
					if item.Type == "function_call" {
						hasTool = true
						break
					}
				}
				if hasTool {
					stop = llm.StopReasonToolUse
				} else {
					stop = llm.StopReasonEndTurn
				}
			case responses.ResponseStatusIncomplete:
				stop = llm.StopReasonMaxTokens
			default:
				stop = llm.StopReasonUnknown
			}

			usage := llm.TokenUsage{
				InputTokens:    int(v.Response.Usage.InputTokens),
				OutputTokens:   int(v.Response.Usage.OutputTokens),
				ThinkingTokens: int(v.Response.Usage.OutputTokensDetails.ReasoningTokens),
			}
			out <- llm.MessageChunk{StopReason: &stop, Usage: &usage}

		case responses.ResponseErrorEvent:
			out <- llm.MessageChunk{Err: fmt.Errorf("openai: stream error event: %s", v.Message)}
			return
		}
	}

	if err := stream.Err(); err != nil {
		out <- llm.MessageChunk{Err: fmt.Errorf("openai: stream error: %w", err)}
	}
}

// streamIterator is the subset of the SDK's ssestream.Stream that consumeStream
// needs. Extracted as an interface so tests can inject a fake.
type streamIterator interface {
	Next() bool
	Current() responses.ResponseStreamEventUnion
	Err() error
}
