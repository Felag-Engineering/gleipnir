package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

// fakeStream implements streamIterator for unit tests. Each call to Next
// advances to the next event; Err is returned after all events are consumed.
type fakeStream struct {
	events []anthropic.MessageStreamEventUnion
	idx    int
	err    error
}

func (f *fakeStream) Next() bool {
	if f.idx >= len(f.events) {
		return false
	}
	f.idx++
	return true
}

func (f *fakeStream) Current() anthropic.MessageStreamEventUnion {
	return f.events[f.idx-1]
}

func (f *fakeStream) Err() error { return f.err }

// unmarshalEvent decodes a raw JSON string into a MessageStreamEventUnion.
func unmarshalEvent(t *testing.T, raw string) anthropic.MessageStreamEventUnion {
	t.Helper()
	var evt anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshalEvent: %v", err)
	}
	return evt
}

// makeMessageStartEvent builds a message_start event with the given input token count.
func makeMessageStartEvent(inputTokens int) anthropic.MessageStreamEventUnion {
	raw := fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`, inputTokens)
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal([]byte(raw), &evt)
	return evt
}

// makeContentBlockStartEvent builds a content_block_start event.
// block should be a JSON object like {"type":"text","text":""} or
// {"type":"tool_use","id":"tu_1","name":"search","input":{}}.
func makeContentBlockStartEvent(index int, blockJSON string) anthropic.MessageStreamEventUnion {
	raw := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":%s}`, index, blockJSON)
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal([]byte(raw), &evt)
	return evt
}

// makeTextDeltaEvent builds a content_block_delta event for a text delta.
func makeTextDeltaEvent(index int, delta string) anthropic.MessageStreamEventUnion {
	payload, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]string{"type": "text_delta", "text": delta},
	})
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal(payload, &evt)
	return evt
}

// makeThinkingDeltaEvent builds a content_block_delta event for a thinking delta.
func makeThinkingDeltaEvent(index int, delta string) anthropic.MessageStreamEventUnion {
	payload, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]string{"type": "thinking_delta", "thinking": delta},
	})
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal(payload, &evt)
	return evt
}

// makeSignatureDeltaEvent builds a content_block_delta event for a signature delta.
func makeSignatureDeltaEvent(index int, sig string) anthropic.MessageStreamEventUnion {
	payload, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]string{"type": "signature_delta", "signature": sig},
	})
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal(payload, &evt)
	return evt
}

// makeInputJSONDeltaEvent builds a content_block_delta event for a tool input JSON delta.
func makeInputJSONDeltaEvent(index int, partialJSON string) anthropic.MessageStreamEventUnion {
	payload, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]string{"type": "input_json_delta", "partial_json": partialJSON},
	})
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal(payload, &evt)
	return evt
}

// makeContentBlockStopEvent builds a content_block_stop event.
func makeContentBlockStopEvent(index int) anthropic.MessageStreamEventUnion {
	raw := fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index)
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal([]byte(raw), &evt)
	return evt
}

// makeMessageDeltaEvent builds a message_delta event with stop reason and output tokens.
func makeMessageDeltaEvent(stopReason string, outputTokens int) anthropic.MessageStreamEventUnion {
	raw := fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"output_tokens":%d}}`, stopReason, outputTokens)
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal([]byte(raw), &evt)
	return evt
}

// makeMessageStopEvent builds a message_stop event.
func makeMessageStopEvent() anthropic.MessageStreamEventUnion {
	raw := `{"type":"message_stop"}`
	var evt anthropic.MessageStreamEventUnion
	_ = json.Unmarshal([]byte(raw), &evt)
	return evt
}

// collectChunks drains the channel and returns all chunks.
func collectChunks(t *testing.T, ch <-chan llm.MessageChunk) []llm.MessageChunk {
	t.Helper()
	var out []llm.MessageChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestConsumeStream_TextOnly(t *testing.T) {
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeMessageStartEvent(100),
			makeContentBlockStartEvent(0, `{"type":"text","text":""}`),
			makeTextDeltaEvent(0, "Hello"),
			makeTextDeltaEvent(0, " World"),
			makeContentBlockStopEvent(0),
			makeMessageDeltaEvent("end_turn", 20),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	chunks := collectChunks(t, out)

	// Expect: 2 text chunks + 1 final chunk.
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	if chunks[0].Text == nil || *chunks[0].Text != "Hello" {
		t.Errorf("chunks[0].Text = %v, want %q", chunks[0].Text, "Hello")
	}
	if chunks[1].Text == nil || *chunks[1].Text != " World" {
		t.Errorf("chunks[1].Text = %v, want %q", chunks[1].Text, " World")
	}

	final := chunks[2]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonEndTurn {
		t.Errorf("final.StopReason = %v, want EndTurn", final.StopReason)
	}
	if final.Usage == nil || final.Usage.InputTokens != 100 || final.Usage.OutputTokens != 20 {
		t.Errorf("final.Usage = %v, want {InputTokens:100, OutputTokens:20}", final.Usage)
	}
}

func TestConsumeStream_TextAndToolCall(t *testing.T) {
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeMessageStartEvent(50),
			makeContentBlockStartEvent(0, `{"type":"text","text":""}`),
			makeTextDeltaEvent(0, "thinking"),
			makeContentBlockStopEvent(0),
			makeContentBlockStartEvent(1, `{"type":"tool_use","id":"tu_1","name":"search","input":{}}`),
			makeInputJSONDeltaEvent(1, `{"q`),
			makeInputJSONDeltaEvent(1, `":"test"}`),
			makeContentBlockStopEvent(1),
			makeMessageDeltaEvent("tool_use", 30),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	chunks := collectChunks(t, out)

	// Expect: 1 text chunk + 1 tool call chunk + 1 final chunk.
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %+v", len(chunks), chunks)
	}

	if chunks[0].Text == nil || *chunks[0].Text != "thinking" {
		t.Errorf("chunks[0].Text = %v, want %q", chunks[0].Text, "thinking")
	}

	tool := chunks[1].ToolCall
	if tool == nil {
		t.Fatal("chunks[1].ToolCall is nil")
	}
	if tool.ID != "tu_1" {
		t.Errorf("ToolCall.ID = %q, want %q", tool.ID, "tu_1")
	}
	if tool.Name != "search" {
		t.Errorf("ToolCall.Name = %q, want %q", tool.Name, "search")
	}
	wantArgs := `{"q":"test"}`
	if string(tool.Input) != wantArgs {
		t.Errorf("ToolCall.Input = %q, want %q", tool.Input, wantArgs)
	}

	final := chunks[2]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonToolUse {
		t.Errorf("final.StopReason = %v, want ToolUse", final.StopReason)
	}
}

func TestConsumeStream_ThinkingWithSignature(t *testing.T) {
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeContentBlockStartEvent(0, `{"type":"thinking","thinking":""}`),
			makeThinkingDeltaEvent(0, "I think"),
			makeThinkingDeltaEvent(0, " deeply"),
			makeSignatureDeltaEvent(0, "sig-abc"),
			makeContentBlockStopEvent(0),
			makeMessageDeltaEvent("end_turn", 10),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	chunks := collectChunks(t, out)

	// Expect: 1 thinking chunk + 1 final chunk.
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	thinking := chunks[0].Thinking
	if thinking == nil {
		t.Fatal("chunks[0].Thinking is nil")
	}
	if thinking.Provider != "anthropic" {
		t.Errorf("Thinking.Provider = %q, want %q", thinking.Provider, "anthropic")
	}
	if thinking.Text != "I think deeply" {
		t.Errorf("Thinking.Text = %q, want %q", thinking.Text, "I think deeply")
	}
	var state anthropicThinkingState
	if err := json.Unmarshal(thinking.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.Signature != "sig-abc" {
		t.Errorf("state.Signature = %q, want %q", state.Signature, "sig-abc")
	}
	if thinking.Redacted {
		t.Error("Thinking.Redacted = true, want false")
	}
}

func TestConsumeStream_RedactedThinking(t *testing.T) {
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeContentBlockStartEvent(0, `{"type":"redacted_thinking","data":"redacted-blob"}`),
			makeContentBlockStopEvent(0),
			makeMessageDeltaEvent("end_turn", 5),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	chunks := collectChunks(t, out)

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	thinking := chunks[0].Thinking
	if thinking == nil {
		t.Fatal("chunks[0].Thinking is nil")
	}
	if thinking.Provider != "anthropic" {
		t.Errorf("Thinking.Provider = %q, want %q", thinking.Provider, "anthropic")
	}
	if thinking.Text != "[redacted]" {
		t.Errorf("Thinking.Text = %q, want %q", thinking.Text, "[redacted]")
	}
	if !thinking.Redacted {
		t.Error("Thinking.Redacted = false, want true")
	}
	var state anthropicThinkingState
	if err := json.Unmarshal(thinking.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.RedactedData != "redacted-blob" {
		t.Errorf("state.RedactedData = %q, want %q", state.RedactedData, "redacted-blob")
	}
}

func TestConsumeStream_ToolNameReverseMapping(t *testing.T) {
	sanitizedToOriginal := map[string]string{
		"fs-read": "fs.read",
	}

	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeContentBlockStartEvent(0, `{"type":"tool_use","id":"tu_1","name":"fs-read","input":{}}`),
			makeInputJSONDeltaEvent(0, `{}`),
			makeContentBlockStopEvent(0),
			makeMessageDeltaEvent("tool_use", 5),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, sanitizedToOriginal)

	chunks := collectChunks(t, out)

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	tool := chunks[0].ToolCall
	if tool == nil {
		t.Fatal("chunks[0].ToolCall is nil")
	}
	if tool.Name != "fs.read" {
		t.Errorf("ToolCall.Name = %q, want %q", tool.Name, "fs.read")
	}
}

func TestConsumeStream_EmptyToolArgs(t *testing.T) {
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeContentBlockStartEvent(0, `{"type":"tool_use","id":"tu_1","name":"noop","input":{}}`),
			// No InputJSONDelta events — args remain empty.
			makeContentBlockStopEvent(0),
			makeMessageDeltaEvent("tool_use", 5),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	chunks := collectChunks(t, out)

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	tool := chunks[0].ToolCall
	if tool == nil {
		t.Fatal("chunks[0].ToolCall is nil")
	}
	if string(tool.Input) != "{}" {
		t.Errorf("ToolCall.Input = %q, want %q", tool.Input, "{}")
	}
}

func TestConsumeStream_TokenAccounting(t *testing.T) {
	// MessageDeltaEvent reports InputTokens=0 (non-cached). The rule is:
	// MessageStartEvent is authoritative for InputTokens; MessageDeltaEvent's
	// InputTokens field is always ignored.
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeMessageStartEvent(500),
			makeContentBlockStartEvent(0, `{"type":"text","text":""}`),
			makeTextDeltaEvent(0, "hi"),
			makeContentBlockStopEvent(0),
			// MessageDeltaEvent with InputTokens=0 (as in non-cached responses).
			// This raw JSON mirrors what the Anthropic API returns.
			func() anthropic.MessageStreamEventUnion {
				raw := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":120}}`
				var evt anthropic.MessageStreamEventUnion
				_ = json.Unmarshal([]byte(raw), &evt)
				return evt
			}(),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	chunks := collectChunks(t, out)

	final := chunks[len(chunks)-1]
	if final.Usage == nil {
		t.Fatal("final chunk has no Usage")
	}
	if final.Usage.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500 (MessageDeltaEvent.Usage.InputTokens=0 must be ignored)", final.Usage.InputTokens)
	}
	if final.Usage.OutputTokens != 120 {
		t.Errorf("OutputTokens = %d, want 120", final.Usage.OutputTokens)
	}
}

func TestConsumeStream_StreamError(t *testing.T) {
	streamErr := errors.New("connection reset")
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeMessageStartEvent(10),
		},
		err: streamErr,
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	chunks := collectChunks(t, out)

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk (the error chunk), got none")
	}
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Fatal("expected final chunk to have Err, got nil")
	}
	if !errors.Is(last.Err, streamErr) {
		t.Errorf("Err = %v, want to wrap %v", last.Err, streamErr)
	}
}

func TestConsumeStream_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeMessageStartEvent(10),
			makeContentBlockStartEvent(0, `{"type":"text","text":""}`),
			makeTextDeltaEvent(0, "hello"),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(ctx, stream, out, nil)

	chunks := collectChunks(t, out)

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk (the error chunk), got none")
	}
	// First chunk should carry ctx.Err().
	if chunks[0].Err == nil {
		t.Fatal("expected Err chunk on cancelled context, got nil Err")
	}
	if !errors.Is(chunks[0].Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", chunks[0].Err)
	}
}

func TestConsumeStream_ChannelClosed(t *testing.T) {
	stream := &fakeStream{
		events: []anthropic.MessageStreamEventUnion{
			makeMessageStartEvent(5),
			makeContentBlockStartEvent(0, `{"type":"text","text":""}`),
			makeTextDeltaEvent(0, "done"),
			makeContentBlockStopEvent(0),
			makeMessageDeltaEvent("end_turn", 3),
			makeMessageStopEvent(),
		},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), stream, out, nil)

	// Drain all chunks.
	for range out {
	}

	// Channel must be closed after drain.
	_, ok := <-out
	if ok {
		t.Error("expected channel to be closed, but received a value")
	}
}
