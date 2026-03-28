package llm

import "strings"

// StubStreamFromResponse converts a CreateMessage response into a single-chunk
// buffered channel. This is the v1.0 stub pattern shared by all provider
// implementations: call CreateMessage, handle the error, then delegate
// chunk-building to this helper. Real streaming (one chunk per token) will
// replace this in a later release.
func StubStreamFromResponse(resp *MessageResponse) <-chan MessageChunk {
	var chunk MessageChunk

	if len(resp.Text) > 0 {
		parts := make([]string, len(resp.Text))
		for i, tb := range resp.Text {
			parts[i] = tb.Text
		}
		joined := strings.Join(parts, "")
		chunk.Text = &joined
	}

	if len(resp.ToolCalls) > 0 {
		chunk.ToolCall = &resp.ToolCalls[0]
	}

	chunk.StopReason = &resp.StopReason
	chunk.Usage = &resp.Usage

	ch := make(chan MessageChunk, 1)
	ch <- chunk
	close(ch)
	return ch
}
