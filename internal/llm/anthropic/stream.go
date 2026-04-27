package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/felag-engineering/gleipnir/internal/llm"
)

// streamIterator is the subset of *ssestream.Stream[anthropic.MessageStreamEventUnion]
// that consumeStream needs. Extracted as an interface so tests can inject a fake
// without spinning up a real SDK stream.
type streamIterator interface {
	Next() bool
	Current() anthropic.MessageStreamEventUnion
	Err() error
}

// partialBlock holds the in-progress state for a single content block while
// streaming. Blocks accumulate deltas until ContentBlockStop, at which point
// the completed block is flushed as a chunk.
type partialBlock struct {
	kind string // "text" | "thinking" | "redacted_thinking" | "tool_use"

	// tool_use fields
	toolID   string
	toolName string // sanitized wire name; reverse-mapped to original on flush
	argsBuf  strings.Builder

	// thinking fields
	thinkingText strings.Builder
	signature    string
	redactedData string
}

// consumeStream reads events from the Anthropic SSE stream and emits
// llm.MessageChunk values on out. The channel is closed exactly once when the
// stream ends (successfully or with an error). Callers must not close out.
//
// Token accounting:
//   - inputTokens is initialized from MessageStartEvent.Message.Usage.InputTokens only.
//   - outputTokens is updated from MessageDeltaEvent.Usage.OutputTokens (cumulative).
//   - MessageDeltaEvent.Usage.InputTokens is always ignored — it is 0 in
//     non-cached responses and reading it would clobber the real input count.
func consumeStream(
	ctx context.Context,
	stream streamIterator,
	out chan<- llm.MessageChunk,
	sanitizedToOriginal map[string]string,
) {
	defer close(out)

	// partials tracks per-block state keyed by the block index (int64 to match
	// the SDK's Index field type and avoid int/int64 map lookup mismatches).
	partials := map[int64]*partialBlock{}

	var inputTokens int64
	var outputTokens int64
	var rawStopReason string // populated by MessageDeltaEvent; mapped on MessageStopEvent

	for stream.Next() {
		select {
		case <-ctx.Done():
			out <- llm.MessageChunk{Err: ctx.Err()}
			return
		default:
		}

		evt := stream.Current()
		switch v := evt.AsAny().(type) {
		case anthropic.MessageStartEvent:
			// InputTokens is authoritative here; do not read it from MessageDeltaEvent.
			inputTokens = v.Message.Usage.InputTokens

		case anthropic.ContentBlockStartEvent:
			pb := &partialBlock{}
			switch block := v.ContentBlock.AsAny().(type) {
			case anthropic.TextBlock:
				pb.kind = "text"
			case anthropic.ThinkingBlock:
				pb.kind = "thinking"
			case anthropic.RedactedThinkingBlock:
				pb.kind = "redacted_thinking"
				// RedactedData arrives whole at start, not via deltas.
				pb.redactedData = block.Data
			case anthropic.ToolUseBlock:
				pb.kind = "tool_use"
				pb.toolID = block.ID
				pb.toolName = block.Name
			}
			partials[v.Index] = pb

		case anthropic.ContentBlockDeltaEvent:
			pb := partials[v.Index]
			if pb == nil {
				// Defensive: drop deltas for unknown blocks.
				continue
			}
			switch delta := v.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				// Use a local copy before taking the address so each chunk's
				// pointer has independent lifetime (matches the OpenAI stream.go idiom).
				text := delta.Text
				out <- llm.MessageChunk{Text: &text}
			case anthropic.ThinkingDelta:
				pb.thinkingText.WriteString(delta.Thinking)
			case anthropic.SignatureDelta:
				pb.signature = delta.Signature
			case anthropic.InputJSONDelta:
				pb.argsBuf.WriteString(delta.PartialJSON)
			}
			// CitationsDelta is intentionally ignored — not used in MessageResponse.

		case anthropic.ContentBlockStopEvent:
			pb := partials[v.Index]
			if pb == nil {
				continue
			}
			delete(partials, v.Index)

			switch pb.kind {
			case "text":
				// Text deltas were already emitted incrementally; nothing to flush.

			case "thinking":
				raw, err := marshalThinkingState(anthropicThinkingState{Signature: pb.signature})
				if err != nil {
					out <- llm.MessageChunk{Err: fmt.Errorf("anthropic: marshal thinking state: %w", err)}
					return
				}
				out <- llm.MessageChunk{Thinking: &llm.ThinkingBlock{
					Provider:      "anthropic",
					Text:          pb.thinkingText.String(),
					Redacted:      false,
					ProviderState: raw,
				}}

			case "redacted_thinking":
				raw, err := marshalThinkingState(anthropicThinkingState{RedactedData: pb.redactedData})
				if err != nil {
					out <- llm.MessageChunk{Err: fmt.Errorf("anthropic: marshal thinking state: %w", err)}
					return
				}
				out <- llm.MessageChunk{Thinking: &llm.ThinkingBlock{
					Provider:      "anthropic",
					Text:          "[redacted]",
					Redacted:      true,
					ProviderState: raw,
				}}

			case "tool_use":
				originalName := pb.toolName
				if mapped, ok := sanitizedToOriginal[pb.toolName]; ok {
					originalName = mapped
				}
				argsStr := pb.argsBuf.String()
				if argsStr == "" {
					argsStr = "{}"
				}
				out <- llm.MessageChunk{ToolCall: &llm.ToolCallBlock{
					ID:    pb.toolID,
					Name:  originalName,
					Input: json.RawMessage(argsStr),
				}}
			}

		case anthropic.MessageDeltaEvent:
			// StopReason is buffered here and mapped to llm.StopReason on MessageStopEvent.
			rawStopReason = string(v.Delta.StopReason)
			// OutputTokens is cumulative; take the latest value. InputTokens is
			// ignored here — see token accounting comment on consumeStream.
			outputTokens = v.Usage.OutputTokens

		case anthropic.MessageStopEvent:
			var stop llm.StopReason
			switch anthropic.StopReason(rawStopReason) {
			case anthropic.StopReasonEndTurn:
				stop = llm.StopReasonEndTurn
			case anthropic.StopReasonToolUse:
				stop = llm.StopReasonToolUse
			case anthropic.StopReasonMaxTokens:
				stop = llm.StopReasonMaxTokens
			default:
				stop = llm.StopReasonUnknown
			}
			usage := llm.TokenUsage{
				InputTokens:  int(inputTokens),
				OutputTokens: int(outputTokens),
			}
			out <- llm.MessageChunk{StopReason: &stop, Usage: &usage}
		}
	}

	if err := stream.Err(); err != nil {
		out <- llm.MessageChunk{Err: fmt.Errorf("anthropic: stream error: %w", err)}
	}
}
