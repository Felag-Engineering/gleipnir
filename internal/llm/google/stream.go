package google

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"

	"github.com/google/uuid"
	"github.com/rapp992/gleipnir/internal/llm"
	"google.golang.org/genai"
)

// consumeStream reads responses from a Google GenerateContentStream iterator
// and emits llm.MessageChunk values on out. The channel is closed exactly once
// when the stream ends. Callers must not close out.
//
// Parts within each response are processed in order:
//   - Thought parts → Thinking chunk (Provider="google").
//   - FunctionCall parts → ToolCall chunk (via buildToolCallBlockFromPart).
//   - Non-empty Text parts → Text chunk (local-pointer idiom).
//
// Usage and FinishReason are accumulated across responses; the final values
// are emitted in a single terminal chunk after the iterator is exhausted.
func consumeStream(
	ctx context.Context,
	seq iter.Seq2[*genai.GenerateContentResponse, error],
	out chan<- llm.MessageChunk,
	names llm.ToolNameMapping,
) {
	defer close(out)

	var usage llm.TokenUsage
	var finishReason genai.FinishReason
	hasToolCall := false

	for resp, err := range seq {
		select {
		case <-ctx.Done():
			out <- llm.MessageChunk{Err: ctx.Err()}
			return
		default:
		}

		if err != nil {
			out <- llm.MessageChunk{Err: fmt.Errorf("google: stream error: %w", err)}
			return
		}

		if resp.UsageMetadata != nil {
			usage = llm.TokenUsage{
				InputTokens:    int(resp.UsageMetadata.PromptTokenCount),
				OutputTokens:   int(resp.UsageMetadata.CandidatesTokenCount),
				ThinkingTokens: int(resp.UsageMetadata.ThoughtsTokenCount),
			}
		}

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			continue
		}

		candidate := resp.Candidates[0]

		for _, part := range candidate.Content.Parts {
			if part.Thought {
				out <- llm.MessageChunk{Thinking: &llm.ThinkingBlock{
					Provider: "google",
					Text:     part.Text,
					Redacted: false,
				}}
				continue
			}
			if part.FunctionCall != nil {
				block, err := buildToolCallBlockFromPart(part, names)
				if err != nil {
					out <- llm.MessageChunk{Err: fmt.Errorf("google: building tool call: %w", err)}
					return
				}
				out <- llm.MessageChunk{ToolCall: &block}
				hasToolCall = true
				continue
			}
			if part.Text != "" {
				// Use a local copy before taking the address so each chunk's
				// pointer has independent lifetime (matches the OpenAI stream.go idiom).
				text := part.Text
				out <- llm.MessageChunk{Text: &text}
			}
		}

		if candidate.FinishReason != genai.FinishReasonUnspecified {
			finishReason = candidate.FinishReason
		}
	}

	// Map FinishReason to llm.StopReason using the same logic as translateResponse.
	var stop llm.StopReason
	switch finishReason {
	case genai.FinishReasonStop:
		if hasToolCall {
			stop = llm.StopReasonToolUse
		} else {
			stop = llm.StopReasonEndTurn
		}
	case genai.FinishReasonMaxTokens:
		stop = llm.StopReasonMaxTokens
	case genai.FinishReasonSafety,
		genai.FinishReasonMalformedFunctionCall,
		genai.FinishReasonRecitation,
		genai.FinishReasonProhibitedContent:
		stop = llm.StopReasonError
	default:
		stop = llm.StopReasonUnknown
	}

	out <- llm.MessageChunk{StopReason: &stop, Usage: &usage}
}

// buildToolCallBlockFromPart constructs an llm.ToolCallBlock from a genai.Part
// containing a FunctionCall. It is shared between translateResponse (non-streaming)
// and consumeStream (streaming) to avoid duplicating UUID generation, name
// reverse-mapping, args marshaling, and ThoughtSignature metadata copying.
func buildToolCallBlockFromPart(part *genai.Part, names llm.ToolNameMapping) (llm.ToolCallBlock, error) {
	id := part.FunctionCall.ID
	if id == "" {
		id = uuid.NewString()
	}

	// Reverse-map from sanitized wire name to original MCP name.
	originalName := part.FunctionCall.Name
	if mapped, ok := names.SanitizedToOriginal[part.FunctionCall.Name]; ok {
		originalName = mapped
	}

	argsJSON, err := json.Marshal(part.FunctionCall.Args)
	if err != nil {
		return llm.ToolCallBlock{}, fmt.Errorf("marshaling function call args: %w", err)
	}

	block := llm.ToolCallBlock{
		ID:    id,
		Name:  originalName,
		Input: json.RawMessage(argsJSON),
	}

	// Gemini 3 attaches ThoughtSignature to the Part (not the inner FunctionCall).
	// It must be echoed back on subsequent turns or the API rejects the replayed call.
	if len(part.ThoughtSignature) > 0 {
		block.ProviderMetadata = map[string][]byte{
			"google.thought_signature": part.ThoughtSignature,
		}
	}

	return block, nil
}
