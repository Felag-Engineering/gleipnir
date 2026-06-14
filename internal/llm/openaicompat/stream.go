package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

// parseSSEStream reads OpenAI Chat Completions SSE from body and emits
// llm.MessageChunk values on out. The channel is always closed exactly once.
// Text deltas are emitted as they arrive; reasoning_content deltas from
// thinking-capable backends (LM Studio, Ollama ≥0.5, vLLM, llama.cpp) are
// emitted as Thinking chunks immediately before any Content chunk in the same
// delta — reasoning precedes the answer. Tool calls are buffered per
// delta.index and emitted as complete blocks when the stream's finish_reason
// arrives. The `names` mapping reverses sanitized wire-format tool names back
// to the original Gleipnir names when flushing tool calls; pass an empty
// mapping to disable rewriting.
//
// providerName is the admin-registered backend name (from ProviderWire.ProviderName)
// and is used as ThinkingBlock.Provider on emitted Thinking chunks.
//
// Error handling: any parse or I/O error emits a final MessageChunk{Err: err}
// and closes the channel. Context cancellation emits {Err: ctx.Err()} and
// closes the channel. A stream that ends without a [DONE] terminator is
// treated as an error.
func parseSSEStream(ctx context.Context, body io.ReadCloser, out chan<- llm.MessageChunk, names llm.ToolNameMapping, providerName string) {
	defer close(out)
	defer body.Close()

	// Check cancellation before we start reading.
	select {
	case <-ctx.Done():
		out <- llm.MessageChunk{Err: ctx.Err()}
		return
	default:
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	partials := map[int]*partialToolCall{}
	var sawDone bool
	var pendingStop *llm.StopReason
	var pendingUsage *llm.TokenUsage

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			out <- llm.MessageChunk{Err: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDone = true
			// Flush any complete tool calls first, then the stop + usage.
			flushToolCalls(partials, out, names)
			if pendingStop != nil || pendingUsage != nil {
				out <- llm.MessageChunk{StopReason: pendingStop, Usage: pendingUsage}
			}
			return
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			out <- llm.MessageChunk{Err: fmt.Errorf("openai: malformed SSE chunk: %w", err)}
			return
		}

		if chunk.Usage != nil {
			pendingUsage = &llm.TokenUsage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
			if chunk.Usage.CompletionTokensDetails != nil {
				pendingUsage.ThinkingTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			}
		}

		for _, choice := range chunk.Choices {
			// Emit reasoning_content before content so that when one delta carries
			// both fields the thinking chunk precedes the text chunk — reasoning
			// precedes the answer, and each chunk carries at most one content field.
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				thinking := *choice.Delta.ReasoningContent
				out <- llm.MessageChunk{Thinking: &llm.ThinkingBlock{Provider: providerName, Text: thinking, Redacted: false}}
			}
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				text := *choice.Delta.Content
				out <- llm.MessageChunk{Text: &text}
			}
			for _, part := range choice.Delta.ToolCalls {
				p := partials[part.Index]
				if p == nil {
					p = &partialToolCall{}
					partials[part.Index] = p
				}
				if part.ID != "" {
					p.ID = part.ID
				}
				if part.Function.Name != "" {
					p.Name = part.Function.Name
				}
				if part.Function.Arguments != "" {
					p.Arguments.WriteString(part.Function.Arguments)
				}
			}
			if choice.FinishReason != nil {
				sr := mapFinishReason(*choice.FinishReason)
				pendingStop = &sr
			}
		}
	}

	// Prefer the context error over scanner.Err()/missing-DONE: a cancelled
	// reader can return EOF before the in-loop ctx check fires, which would
	// otherwise misreport cancellation as "stream ended without [DONE]".
	if err := ctx.Err(); err != nil {
		out <- llm.MessageChunk{Err: err}
		return
	}
	if err := scanner.Err(); err != nil {
		out <- llm.MessageChunk{Err: fmt.Errorf("openai: reading SSE stream: %w", err)}
		return
	}
	if !sawDone {
		out <- llm.MessageChunk{Err: fmt.Errorf("openai: stream ended without [DONE] terminator")}
	}
}

type partialToolCall struct {
	ID        string
	Name      string
	Arguments bytes.Buffer
}

func flushToolCalls(partials map[int]*partialToolCall, out chan<- llm.MessageChunk, names llm.ToolNameMapping) {
	// Emit in deterministic order by index.
	indices := make([]int, 0, len(partials))
	for i := range partials {
		indices = append(indices, i)
	}
	// Small-slice insertion sort — sort.Ints pulls in the sort package, overkill.
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && indices[j-1] > indices[j]; j-- {
			indices[j-1], indices[j] = indices[j], indices[j-1]
		}
	}
	for _, idx := range indices {
		p := partials[idx]
		if p.ID == "" && p.Name == "" && p.Arguments.Len() == 0 {
			continue
		}
		args := p.Arguments.String()
		if args == "" {
			args = "{}"
		}
		name := p.Name
		if original, ok := names.SanitizedToOriginal[name]; ok {
			name = original
		}
		out <- llm.MessageChunk{ToolCall: &llm.ToolCallBlock{
			ID:    p.ID,
			Name:  name,
			Input: json.RawMessage(args),
		}}
	}
}

func mapFinishReason(s string) llm.StopReason {
	switch s {
	case "stop":
		return llm.StopReasonEndTurn
	case "tool_calls":
		return llm.StopReasonToolUse
	case "length":
		return llm.StopReasonMaxTokens
	default:
		return llm.StopReasonError
	}
}
