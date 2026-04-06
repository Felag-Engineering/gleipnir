package llm

import (
	"encoding/json"
	"testing"
)

// drainChunks reads all chunks from ch and returns them in order.
func drainChunks(ch <-chan MessageChunk) []MessageChunk {
	var chunks []MessageChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	return chunks
}

func TestStubStreamFromResponse(t *testing.T) {
	t.Run("text-only response", func(t *testing.T) {
		resp := &MessageResponse{
			Text:       []TextBlock{{Text: "hello world"}},
			StopReason: StopReasonEndTurn,
			Usage:      TokenUsage{InputTokens: 10, OutputTokens: 5},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		c := chunks[0]
		if c.Text == nil || *c.Text != "hello world" {
			t.Errorf("Text = %v, want %q", c.Text, "hello world")
		}
		if c.ToolCall != nil {
			t.Errorf("ToolCall = %v, want nil", c.ToolCall)
		}
		if c.Thinking != nil {
			t.Errorf("Thinking = %v, want nil", c.Thinking)
		}
		assertMetadata(t, c, StopReasonEndTurn, TokenUsage{InputTokens: 10, OutputTokens: 5})
	})

	t.Run("tool-call-only response", func(t *testing.T) {
		resp := &MessageResponse{
			ToolCalls: []ToolCallBlock{
				{ID: "tc-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
			},
			StopReason: StopReasonToolUse,
			Usage:      TokenUsage{InputTokens: 8, OutputTokens: 3},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		c := chunks[0]
		if c.ToolCall == nil {
			t.Fatal("ToolCall is nil, want non-nil")
		}
		if c.ToolCall.Name != "search" {
			t.Errorf("ToolCall.Name = %q, want %q", c.ToolCall.Name, "search")
		}
		if c.Text != nil {
			t.Errorf("Text = %v, want nil", c.Text)
		}
		if c.Thinking != nil {
			t.Errorf("Thinking = %v, want nil", c.Thinking)
		}
		assertMetadata(t, c, StopReasonToolUse, TokenUsage{InputTokens: 8, OutputTokens: 3})
	})

	t.Run("combined text and tool calls", func(t *testing.T) {
		// Text and tool call must arrive as separate chunks (multi-chunk emission).
		resp := &MessageResponse{
			Text:       []TextBlock{{Text: "using tool"}},
			ToolCalls:  []ToolCallBlock{{ID: "tc-2", Name: "fetch", Input: json.RawMessage(`{}`)}},
			StopReason: StopReasonToolUse,
			Usage:      TokenUsage{InputTokens: 15, OutputTokens: 7},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks (1 text + 1 tool call), got %d", len(chunks))
		}

		textChunk := chunks[0]
		if textChunk.Text == nil || *textChunk.Text != "using tool" {
			t.Errorf("chunks[0].Text = %v, want %q", textChunk.Text, "using tool")
		}
		if textChunk.StopReason != nil {
			t.Error("chunks[0].StopReason should be nil (not the final chunk)")
		}

		toolChunk := chunks[1]
		if toolChunk.ToolCall == nil || toolChunk.ToolCall.Name != "fetch" {
			t.Errorf("chunks[1].ToolCall = %v, want Name=%q", toolChunk.ToolCall, "fetch")
		}
		assertMetadata(t, toolChunk, StopReasonToolUse, TokenUsage{InputTokens: 15, OutputTokens: 7})
	})

	t.Run("empty response", func(t *testing.T) {
		resp := &MessageResponse{
			StopReason: StopReasonUnknown,
			Usage:      TokenUsage{InputTokens: 2, OutputTokens: 0},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		if len(chunks) != 1 {
			t.Fatalf("expected 1 metadata-only chunk, got %d", len(chunks))
		}
		c := chunks[0]
		if c.Text != nil {
			t.Errorf("Text = %v, want nil", c.Text)
		}
		if c.ToolCall != nil {
			t.Errorf("ToolCall = %v, want nil", c.ToolCall)
		}
		if c.Thinking != nil {
			t.Errorf("Thinking = %v, want nil", c.Thinking)
		}
		assertMetadata(t, c, StopReasonUnknown, TokenUsage{InputTokens: 2, OutputTokens: 0})
	})

	t.Run("thinking-only response", func(t *testing.T) {
		resp := &MessageResponse{
			Thinking: []ThinkingBlock{
				{Text: "step one", Redacted: false},
				{Text: "step two", Redacted: false},
			},
			StopReason: StopReasonEndTurn,
			Usage:      TokenUsage{InputTokens: 20, OutputTokens: 10},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks (one per thinking block), got %d", len(chunks))
		}

		if chunks[0].Thinking == nil || chunks[0].Thinking.Text != "step one" {
			t.Errorf("chunks[0].Thinking = %v, want Text=%q", chunks[0].Thinking, "step one")
		}
		if chunks[0].StopReason != nil {
			t.Error("chunks[0].StopReason should be nil (not the final chunk)")
		}

		if chunks[1].Thinking == nil || chunks[1].Thinking.Text != "step two" {
			t.Errorf("chunks[1].Thinking = %v, want Text=%q", chunks[1].Thinking, "step two")
		}
		assertMetadata(t, chunks[1], StopReasonEndTurn, TokenUsage{InputTokens: 20, OutputTokens: 10})
	})

	t.Run("redacted thinking block", func(t *testing.T) {
		resp := &MessageResponse{
			Thinking:   []ThinkingBlock{{Text: "", Redacted: true}},
			StopReason: StopReasonEndTurn,
			Usage:      TokenUsage{InputTokens: 5, OutputTokens: 2},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0].Thinking == nil {
			t.Fatal("Thinking is nil, want non-nil")
		}
		if !chunks[0].Thinking.Redacted {
			t.Error("Thinking.Redacted = false, want true")
		}
		assertMetadata(t, chunks[0], StopReasonEndTurn, TokenUsage{InputTokens: 5, OutputTokens: 2})
	})

	t.Run("multiple tool calls", func(t *testing.T) {
		// Regression test: previously only ToolCalls[0] was forwarded.
		resp := &MessageResponse{
			ToolCalls: []ToolCallBlock{
				{ID: "tc-a", Name: "alpha", Input: json.RawMessage(`{}`)},
				{ID: "tc-b", Name: "beta", Input: json.RawMessage(`{}`)},
				{ID: "tc-c", Name: "gamma", Input: json.RawMessage(`{}`)},
			},
			StopReason: StopReasonToolUse,
			Usage:      TokenUsage{InputTokens: 12, OutputTokens: 6},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		if len(chunks) != 3 {
			t.Fatalf("expected 3 chunks (one per tool call), got %d", len(chunks))
		}

		wantNames := []string{"alpha", "beta", "gamma"}
		for i, name := range wantNames {
			if chunks[i].ToolCall == nil {
				t.Fatalf("chunks[%d].ToolCall is nil", i)
			}
			if chunks[i].ToolCall.Name != name {
				t.Errorf("chunks[%d].ToolCall.Name = %q, want %q", i, chunks[i].ToolCall.Name, name)
			}
			if i < 2 && chunks[i].StopReason != nil {
				t.Errorf("chunks[%d].StopReason should be nil (not the final chunk)", i)
			}
		}
		assertMetadata(t, chunks[2], StopReasonToolUse, TokenUsage{InputTokens: 12, OutputTokens: 6})
	})

	t.Run("thinking with text and tool calls", func(t *testing.T) {
		// Covers all three content types simultaneously.
		resp := &MessageResponse{
			Thinking: []ThinkingBlock{
				{Text: "reasoning here", Redacted: false},
			},
			Text: []TextBlock{{Text: "my answer"}},
			ToolCalls: []ToolCallBlock{
				{ID: "tc-x", Name: "lookup", Input: json.RawMessage(`{"key":"v"}`)},
				{ID: "tc-y", Name: "store", Input: json.RawMessage(`{"key":"v"}`)},
			},
			StopReason: StopReasonToolUse,
			Usage:      TokenUsage{InputTokens: 30, OutputTokens: 20},
		}
		chunks := drainChunks(StubStreamFromResponse(resp))

		// Expect: 1 thinking + 1 text + 2 tool calls = 4 chunks.
		if len(chunks) != 4 {
			t.Fatalf("expected 4 chunks, got %d", len(chunks))
		}

		// Chunk 0: thinking.
		if chunks[0].Thinking == nil || chunks[0].Thinking.Text != "reasoning here" {
			t.Errorf("chunks[0].Thinking = %v, want Text=%q", chunks[0].Thinking, "reasoning here")
		}
		if chunks[0].Text != nil || chunks[0].ToolCall != nil {
			t.Error("chunks[0] should only have Thinking set")
		}

		// Chunk 1: text.
		if chunks[1].Text == nil || *chunks[1].Text != "my answer" {
			t.Errorf("chunks[1].Text = %v, want %q", chunks[1].Text, "my answer")
		}
		if chunks[1].Thinking != nil || chunks[1].ToolCall != nil {
			t.Error("chunks[1] should only have Text set")
		}

		// Chunk 2: first tool call.
		if chunks[2].ToolCall == nil || chunks[2].ToolCall.Name != "lookup" {
			t.Errorf("chunks[2].ToolCall = %v, want Name=%q", chunks[2].ToolCall, "lookup")
		}
		if chunks[2].StopReason != nil {
			t.Error("chunks[2].StopReason should be nil (not the final chunk)")
		}

		// Chunk 3: second tool call, carries metadata.
		if chunks[3].ToolCall == nil || chunks[3].ToolCall.Name != "store" {
			t.Errorf("chunks[3].ToolCall = %v, want Name=%q", chunks[3].ToolCall, "store")
		}
		assertMetadata(t, chunks[3], StopReasonToolUse, TokenUsage{InputTokens: 30, OutputTokens: 20})

		// Verify intermediate chunks do NOT carry metadata.
		for i := 0; i < 3; i++ {
			if chunks[i].StopReason != nil || chunks[i].Usage != nil {
				t.Errorf("chunks[%d] should not carry StopReason or Usage", i)
			}
		}
	})
}

func TestStubStreamFromResponse_ChannelClosed(t *testing.T) {
	resp := &MessageResponse{
		Text:       []TextBlock{{Text: "hi"}},
		StopReason: StopReasonEndTurn,
	}

	ch := StubStreamFromResponse(resp)

	// Drain all chunks before checking closure.
	for range ch {
	}

	// Channel must be closed — subsequent receive must return ok=false.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after all chunks consumed, but received a value")
	}
}

// assertMetadata checks that the chunk carries the expected StopReason and Usage.
func assertMetadata(t *testing.T, c MessageChunk, wantStop StopReason, wantUsage TokenUsage) {
	t.Helper()
	if c.StopReason == nil {
		t.Fatal("StopReason is nil, want non-nil")
	}
	if *c.StopReason != wantStop {
		t.Errorf("StopReason = %v, want %v", *c.StopReason, wantStop)
	}
	if c.Usage == nil {
		t.Fatal("Usage is nil, want non-nil")
	}
	if *c.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", *c.Usage, wantUsage)
	}
}
