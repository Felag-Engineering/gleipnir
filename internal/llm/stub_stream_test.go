package llm

import (
	"encoding/json"
	"testing"
)

func TestStubStreamFromResponse(t *testing.T) {
	tests := []struct {
		name          string
		resp          *MessageResponse
		wantText      *string
		wantToolCall  bool
		wantToolName  string
		wantStopReason StopReason
		wantUsage     TokenUsage
	}{
		{
			name: "text-only response",
			resp: &MessageResponse{
				Text:       []TextBlock{{Text: "hello world"}},
				StopReason: StopReasonEndTurn,
				Usage:      TokenUsage{InputTokens: 10, OutputTokens: 5},
			},
			wantText:       strPtr("hello world"),
			wantToolCall:   false,
			wantStopReason: StopReasonEndTurn,
			wantUsage:      TokenUsage{InputTokens: 10, OutputTokens: 5},
		},
		{
			name: "tool-call-only response",
			resp: &MessageResponse{
				ToolCalls: []ToolCallBlock{
					{ID: "tc-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
				},
				StopReason: StopReasonToolUse,
				Usage:      TokenUsage{InputTokens: 8, OutputTokens: 3},
			},
			wantText:       nil,
			wantToolCall:   true,
			wantToolName:   "search",
			wantStopReason: StopReasonToolUse,
			wantUsage:      TokenUsage{InputTokens: 8, OutputTokens: 3},
		},
		{
			name: "combined text and tool calls",
			resp: &MessageResponse{
				Text:      []TextBlock{{Text: "using tool"}},
				ToolCalls: []ToolCallBlock{{ID: "tc-2", Name: "fetch", Input: json.RawMessage(`{}`)}},
				StopReason: StopReasonToolUse,
				Usage:      TokenUsage{InputTokens: 15, OutputTokens: 7},
			},
			wantText:       strPtr("using tool"),
			wantToolCall:   true,
			wantToolName:   "fetch",
			wantStopReason: StopReasonToolUse,
			wantUsage:      TokenUsage{InputTokens: 15, OutputTokens: 7},
		},
		{
			name: "empty response",
			resp: &MessageResponse{
				StopReason: StopReasonUnknown,
				Usage:      TokenUsage{InputTokens: 2, OutputTokens: 0},
			},
			wantText:       nil,
			wantToolCall:   false,
			wantStopReason: StopReasonUnknown,
			wantUsage:      TokenUsage{InputTokens: 2, OutputTokens: 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := StubStreamFromResponse(tc.resp)

			chunk, ok := <-ch
			if !ok {
				t.Fatal("expected chunk from channel, got closed channel")
			}

			if chunk.Err != nil {
				t.Errorf("expected nil Err, got %v", chunk.Err)
			}

			if tc.wantText == nil {
				if chunk.Text != nil {
					t.Errorf("expected nil Text, got %q", *chunk.Text)
				}
			} else {
				if chunk.Text == nil {
					t.Fatal("expected non-nil Text, got nil")
				}
				if *chunk.Text != *tc.wantText {
					t.Errorf("expected Text=%q, got %q", *tc.wantText, *chunk.Text)
				}
			}

			if tc.wantToolCall {
				if chunk.ToolCall == nil {
					t.Fatal("expected non-nil ToolCall, got nil")
				}
				if chunk.ToolCall.Name != tc.wantToolName {
					t.Errorf("expected ToolCall.Name=%q, got %q", tc.wantToolName, chunk.ToolCall.Name)
				}
			} else {
				if chunk.ToolCall != nil {
					t.Errorf("expected nil ToolCall, got %+v", chunk.ToolCall)
				}
			}

			if chunk.StopReason == nil {
				t.Fatal("expected non-nil StopReason, got nil")
			}
			if *chunk.StopReason != tc.wantStopReason {
				t.Errorf("expected StopReason=%v, got %v", tc.wantStopReason, *chunk.StopReason)
			}

			if chunk.Usage == nil {
				t.Fatal("expected non-nil Usage, got nil")
			}
			if *chunk.Usage != tc.wantUsage {
				t.Errorf("expected Usage=%+v, got %+v", tc.wantUsage, *chunk.Usage)
			}
		})
	}
}

func TestStubStreamFromResponse_ChannelClosed(t *testing.T) {
	resp := &MessageResponse{
		Text:       []TextBlock{{Text: "hi"}},
		StopReason: StopReasonEndTurn,
	}

	ch := StubStreamFromResponse(resp)

	// Consume the single chunk.
	<-ch

	// Channel must be closed — second receive must return ok=false.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after single chunk, but received another value")
	}
}

// strPtr is a helper to take the address of a string literal.
func strPtr(s string) *string { return &s }
