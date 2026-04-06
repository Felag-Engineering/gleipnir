package llm

import "strings"

// StubStreamFromResponse converts a CreateMessage response into a multi-chunk
// buffered channel. This is the v1.0 stub pattern shared by all provider
// implementations: call CreateMessage, handle the error, then delegate
// chunk-building to this helper. Real streaming (one chunk per token) will
// replace this in a later release.
//
// Emission order:
//   1. One chunk per ThinkingBlock (Thinking field set).
//   2. One text chunk if resp.Text is non-empty (all TextBlocks joined into one string).
//   3. One chunk per ToolCallBlock (ToolCall field set).
//
// StopReason and Usage are attached only to the final chunk so consumers can
// treat a non-nil StopReason as the "stream complete" signal — matching real
// streaming behavior where metadata arrives once at the end.
func StubStreamFromResponse(resp *MessageResponse) <-chan MessageChunk {
	// Channel sized to hold every content chunk plus the metadata-only chunk for
	// the empty-response case. Formula: one slot per thinking block + one slot if
	// there is any text + one slot per tool call + one extra guarantees we never
	// size to zero (empty response still emits exactly one metadata chunk).
	textCount := 0
	if len(resp.Text) > 0 {
		textCount = 1
	}
	bufSize := len(resp.Thinking) + textCount + len(resp.ToolCalls)
	if bufSize == 0 {
		bufSize = 1
	}
	ch := make(chan MessageChunk, bufSize)

	chunks := make([]MessageChunk, 0, bufSize)

	for i := range resp.Thinking {
		tb := resp.Thinking[i] // local copy so the pointer stays valid after the loop
		chunks = append(chunks, MessageChunk{Thinking: &tb})
	}

	if len(resp.Text) > 0 {
		parts := make([]string, len(resp.Text))
		for i, tb := range resp.Text {
			parts[i] = tb.Text
		}
		joined := strings.Join(parts, "")
		chunks = append(chunks, MessageChunk{Text: &joined})
	}

	for i := range resp.ToolCalls {
		tc := resp.ToolCalls[i] // local copy so the pointer stays valid after the loop
		chunks = append(chunks, MessageChunk{ToolCall: &tc})
	}

	if len(chunks) == 0 {
		// Empty response: emit a single metadata-only chunk.
		chunks = append(chunks, MessageChunk{})
	}

	// Attach StopReason and Usage to the final chunk only.
	chunks[len(chunks)-1].StopReason = &resp.StopReason
	chunks[len(chunks)-1].Usage = &resp.Usage

	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch
}
